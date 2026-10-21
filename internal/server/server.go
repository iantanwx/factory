// Package server composes one loop server process (U4): the instance lock,
// host config, the store, the secret store and its redactor, run tokens,
// the RPC server and its socket. Later units add verb handlers through
// Options.Setup and start-up reconcile steps through Options.Reconcile.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// File names in the instance directory.
const (
	DatabaseFile = "loop.db"
	KeyFile      = "keys/kek"
	// PreviousVersionFile names the release this one replaced. U16's
	// upgrade writes it. A run started under that release may keep calling.
	PreviousVersionFile = "previous_version"
)

// DefaultShutdownTimeout bounds GracefulStop on SIGTERM.
const DefaultShutdownTimeout = 30 * time.Second

// Step is one start-up reconcile step or one stop step.
type Step func(ctx context.Context) error

// Options configure Run.
type Options struct {
	// Instance is the instance directory.
	Instance string
	// Reconcile runs in order after the store opens and before the socket
	// binds. The socket accepts no call until every step returns nil. U5
	// adds the real reconcile.
	Reconcile []Step
	// OnStop runs in order after the last in-flight call finished, before
	// the store closes. It persists what memory holds (R16).
	OnStop []Step
	// Setup runs once the server is composed and before reconcile. A later
	// unit registers its handlers here with s.RPC.Handle.
	Setup []func(s *Server) error
	// Logger receives the server's lines. JSON on stderr when nil. The
	// server wraps it in the redactor.
	Logger *slog.Logger
	// ShutdownTimeout bounds GracefulStop. DefaultShutdownTimeout when zero.
	ShutdownTimeout time.Duration
	// Ready is closed when the socket accepts calls. Optional.
	Ready chan<- struct{}
	// HoldsItem, Checkouts and Proc override the rpc defaults. Optional.
	HoldsItem rpc.HoldsItem
	Checkouts rpc.CheckoutLister
	Proc      rpc.ProcInspector
	// ManifestReader reads a project's secret manifest for secret import.
	// GitManifest when nil.
	ManifestReader ManifestReader
	// OpPath is the 1Password CLI. "op" on PATH when empty.
	OpPath string
	// Beads resolves each project's beads client. When set, the server
	// composes the state machine (U5): it serves the work, review and
	// decision verbs, and its reconcile runs first, before the socket
	// binds. A server without beads answers those verbs NOT_IMPLEMENTED.
	Beads machine.BeadsResolver
	// Claims is the admission hook of claims (U6). Optional.
	Claims machine.ClaimGuard
}

// Server is one composed instance.
type Server struct {
	Instance string
	HostPath string
	Store    *store.Store
	Secrets  *secrets.Store
	Redactor *secrets.Redactor
	Tokens   *rpc.Tokens
	RPC      *rpc.Server
	Projects *config.Projects
	// Machine is the state machine, or nil when Options.Beads is nil.
	Machine *machine.Machine
	Started time.Time

	opts Options
	log  *slog.Logger
}

// Run serves the instance until ctx ends, then stops gracefully. It
// returns an error that matches rpc.ErrInstanceLocked when another server
// holds the instance.
func Run(ctx context.Context, opts Options) (err error) {
	dir, err := filepath.Abs(opts.Instance)
	if err != nil {
		return err
	}
	opts.Instance = dir
	lock, err := rpc.LockInstance(dir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	s, err := compose(ctx, opts)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := s.Store.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	for _, setup := range opts.Setup {
		if err := setup(s); err != nil {
			return fmt.Errorf("server: setup: %w", err)
		}
	}

	socket := cli.SocketPath(dir)
	if err := rpc.PrepareSocket(socket); err != nil {
		return err
	}
	// Bind only after reconcile: until then a CLI finds no socket and
	// waits (KTD13). The machine's reconcile runs first (R17).
	steps := opts.Reconcile
	if s.Machine != nil {
		steps = append([]Step{s.Machine.Reconcile}, steps...)
	}
	for i, step := range steps {
		if err := step(ctx); err != nil {
			return fmt.Errorf("server: start-up reconcile step %d: %w", i+1, err)
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	l, err := rpc.Bind(socket)
	if err != nil {
		return err
	}
	served := make(chan error, 1)
	go func() { served <- s.RPC.Serve(l) }()
	s.log.InfoContext(ctx, "serving", "socket", socket, "version", cli.Version, "instance", dir)
	if err := rpc.NotifyReady(); err != nil {
		s.log.WarnContext(ctx, "systemd notify failed", "err", err)
	}
	if opts.Ready != nil {
		close(opts.Ready)
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
	}
	_ = rpc.NotifyStopping()
	timeout := opts.ShutdownTimeout
	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	s.RPC.GracefulStop(stopCtx)
	for i, step := range opts.OnStop {
		if err := step(stopCtx); err != nil {
			s.log.ErrorContext(stopCtx, "stop step failed", "step", i+1, "err", err)
		}
	}
	s.log.InfoContext(stopCtx, "stopped")
	return serveErr
}

func compose(ctx context.Context, opts Options) (*Server, error) {
	dir := opts.Instance
	hostPath := filepath.Join(dir, config.HostConfigFile)
	if _, err := os.Stat(hostPath); errors.Is(err, fs.ErrNotExist) {
		// `loop install` (U16) writes host config. Until then a first start
		// writes the minimal one.
		if err := config.SaveHost(hostPath, &config.HostConfig{StateDir: dir}); err != nil {
			return nil, err
		}
	}
	cfg, err := config.LoadHost(hostPath)
	if err != nil {
		return nil, err
	}
	dbPath := cfg.Store.Path
	if dbPath == "" {
		dbPath = filepath.Join(dir, DatabaseFile)
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = st.Close()
		}
	}()
	keyPath := cfg.KeyFile
	if keyPath == "" {
		keyPath = filepath.Join(dir, KeyFile)
	}
	if err := secrets.EnsureKeyFile(keyPath); err != nil {
		return nil, err
	}
	keys, err := secrets.NewFileKeyProvider(keyPath)
	if err != nil {
		return nil, err
	}
	sec := secrets.New(st, keys)
	red := secrets.NewRedactor()
	if err := red.Refresh(ctx, sec); err != nil {
		return nil, err
	}
	if cred, err := cli.ReadAdminCredential(dir); err == nil {
		red.SetLive("host/admin_credential", cred)
	}
	tokens := &rpc.Tokens{Store: st, Redactor: red}
	if err := tokens.LoadLive(ctx); err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	srv, err := rpc.New(rpc.Options{
		Registry:        cli.Surface(),
		Store:           st,
		Redactor:        red,
		Tokens:          tokens,
		Instance:        dir,
		PreviousVersion: readPreviousVersion(dir),
		Logger:          logger,
		HoldsItem:       opts.HoldsItem,
		Checkouts:       opts.Checkouts,
		Proc:            opts.Proc,
	})
	if err != nil {
		return nil, err
	}
	s := &Server{
		Instance: dir, HostPath: hostPath, Store: st, Secrets: sec, Redactor: red,
		Tokens: tokens, RPC: srv, Started: time.Now(), opts: opts, log: srv.Logger(),
	}
	s.Projects = &config.Projects{
		HostPath:    hostPath,
		AddHooks:    []config.AddHook{config.StoreProjectRow(st)},
		RemoveHooks: []config.RemoveHook{sec.PurgeProject},
	}
	if err := s.wire(); err != nil {
		return nil, err
	}
	if opts.Beads != nil {
		s.Machine, err = machine.New(machine.Config{Store: st, Beads: opts.Beads, Redactor: red, Claims: opts.Claims,
			Registry: cli.Surface(), Logger: srv.Logger()})
		if err != nil {
			return nil, err
		}
		if err := s.Machine.Register(srv); err != nil {
			return nil, err
		}
	}
	ok = true
	return s, nil
}

func readPreviousVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, PreviousVersionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// RefreshRedactor reloads every stored secret into the redactor. The
// server calls it after secret set, secret import and project remove.
func (s *Server) RefreshRedactor(ctx context.Context) error {
	return s.Redactor.Refresh(ctx, s.Secrets)
}
