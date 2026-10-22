package beadsx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// maxSocketPath is the shortest unix socket path limit of the target
// platforms (104 bytes on macOS, 108 on Linux, both with the terminator).
const maxSocketPath = 103

// DoltConfig configures the supervisor of one instance's Dolt sql-server.
type DoltConfig struct {
	Binary string // path to the pinned dolt
	// Dir holds data/ (the databases), root/ (dolt's own global config),
	// the socket, dolt.log and dolt.pid. Layout.DoltDir names it.
	Dir            string
	Socket         string        // empty means Dir/sql.sock
	LogPath        string        // empty means Dir/dolt.log
	HealthInterval time.Duration // zero means 1s
	HealthFailures int           // consecutive failed checks before a kill; zero means 3
	StartTimeout   time.Duration // zero means 60s
	MinBackoff     time.Duration // zero means 200ms
	MaxBackoff     time.Duration // zero means 30s
}

// RestartEvent reports one restart the supervisor made. Reconcile re-verifies
// the intents applied since its last verified mark after each one (KTD6).
type RestartEvent struct {
	N      int // restarts so far, counting this one
	PID    int // the new server's PID
	At     time.Time
	Reason string
}

// Supervisor runs `dolt sql-server` on a unix socket, health-checks it,
// restarts it with backoff when it dies, and stops it by its own PID.
type Supervisor struct {
	cfg      DoltConfig
	restarts chan RestartEvent

	mu       sync.Mutex
	password string // the root password; never logged or printed
	cmd      *exec.Cmd
	exited   chan struct{} // closed when cmd exits
	count    int
	started  bool
	stop     chan struct{}
	done     chan struct{}
}

// NewSupervisor validates the configuration. It starts nothing.
func NewSupervisor(cfg DoltConfig) (*Supervisor, error) {
	if cfg.Binary == "" || cfg.Dir == "" {
		return nil, fmt.Errorf("%w: dolt binary and dir are required", ErrInvalid)
	}
	if !filepath.IsAbs(cfg.Dir) {
		return nil, fmt.Errorf("%w: dolt dir %q is not absolute", ErrInvalid, cfg.Dir)
	}
	if cfg.Socket == "" {
		cfg.Socket = filepath.Join(cfg.Dir, "sql.sock")
	}
	if len(cfg.Socket) > maxSocketPath {
		return nil, fmt.Errorf("%w: socket path %q is longer than %d bytes", ErrInvalid, cfg.Socket, maxSocketPath)
	}
	if cfg.LogPath == "" {
		cfg.LogPath = filepath.Join(cfg.Dir, "dolt.log")
	}
	if cfg.HealthInterval == 0 {
		cfg.HealthInterval = time.Second
	}
	if cfg.HealthFailures == 0 {
		cfg.HealthFailures = 3
	}
	if cfg.StartTimeout == 0 {
		cfg.StartTimeout = 60 * time.Second
	}
	if cfg.MinBackoff == 0 {
		cfg.MinBackoff = 200 * time.Millisecond
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	return &Supervisor{cfg: cfg, restarts: make(chan RestartEvent, 64)}, nil
}

// Socket is the unix socket path bd connects to.
func (s *Supervisor) Socket() string { return s.cfg.Socket }

// PasswordFile is the file that holds the Dolt root password. Start writes
// it, and bd reads it through BDConfig.PasswordFile.
func (s *Supervisor) PasswordFile() string { return filepath.Join(s.cfg.Dir, PasswordFileName) }

// Restarts delivers one event for each restart. The channel holds 64 events.
// When it is full the supervisor drops the event, and RestartCount still
// counts it, so a slow reader compares counts.
func (s *Supervisor) Restarts() <-chan RestartEvent { return s.restarts }

// RestartCount is the number of restarts so far.
func (s *Supervisor) RestartCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// PID is the current server's PID, or 0 when none runs.
func (s *Supervisor) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	select {
	case <-s.exited:
		return 0
	default:
		return s.cmd.Process.Pid
	}
}

func (s *Supervisor) dataDir() string { return filepath.Join(s.cfg.Dir, "data") }
func (s *Supervisor) rootDir() string { return filepath.Join(s.cfg.Dir, "root") }

// Start checks the dolt version, prepares the instance directory, starts the
// server and waits until it accepts connections. It refuses a socket that
// another process already serves.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("%w: supervisor already started", ErrInvalid)
	}
	s.started = true
	s.mu.Unlock()

	if err := s.prepare(); err != nil {
		return err
	}
	if err := CheckDoltVersion(ctx, s.cfg.Binary, s.doltEnv(), s.dataDir()); err != nil {
		return err
	}
	if probe(s.cfg.Socket) == nil {
		return fmt.Errorf("%w: socket %s is already served by another process", ErrRefused, s.cfg.Socket)
	}
	pw, err := s.ensurePassword(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.password = pw
	s.mu.Unlock()
	if err := s.spawn(); err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, s.cfg.StartTimeout)
	defer cancel()
	if err := s.waitReady(rctx); err != nil {
		s.killCurrent()
		return err
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.loop()
	return nil
}

// Stop stops the server by its own PID: SIGTERM, then SIGKILL when ctx ends.
// It removes the socket file the server leaves. A ctx with no deadline can
// wait forever on a server that ignores SIGTERM, so a caller bounds it.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.mu.Unlock()
	if stop == nil {
		return nil
	}
	select {
	case <-stop:
	default:
		close(stop)
	}
	select {
	case <-done:
	case <-ctx.Done():
		// The watch loop is still inside a start. Kill that server, so the
		// loop sees it exit and returns.
		s.killCurrent()
		<-done
	}
	s.mu.Lock()
	cmd, exited := s.cmd, s.exited
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-exited
		}
	}
	_ = os.Remove(s.cfg.Socket)
	_ = os.Remove(filepath.Join(s.cfg.Dir, "dolt.pid"))
	return nil
}

// prepare makes the directories and seeds dolt's global config with metrics
// and the online version check turned off.
func (s *Supervisor) prepare() error {
	for _, d := range []string{s.dataDir(), filepath.Join(s.rootDir(), ".dolt")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	path := filepath.Join(s.rootDir(), ".dolt", "config_global.json")
	cfg := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrRefused, path, err)
		}
	}
	cfg["metrics.disabled"] = "true"
	cfg["versioncheck.disabled"] = "true"
	if cfg["user.name"] == "" {
		cfg["user.name"] = "loop"
	}
	if cfg["user.email"] == "" {
		cfg["user.email"] = "loop@localhost"
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}

// PasswordFileName names the root password file in the supervisor's
// directory. The file lives outside data/, so no Dolt database holds it.
const PasswordFileName = "root.password"

var passwordPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ensurePassword returns the root password, and makes one on the first
// start. Dolt 2.2.0 always opens a TCP listener on loopback, and any local
// user can reach it. A root without a password lets that user read and
// write every beads database.
//
// A new data directory takes the password from DOLT_ROOT_PASSWORD when the
// server creates root. A server that ran before keeps root in its
// privileges file and ignores that variable, so an offline ALTER USER sets
// the password there first.
func (s *Supervisor) ensurePassword(ctx context.Context) (string, error) {
	path := s.PasswordFile()
	b, err := os.ReadFile(path)
	if err == nil {
		pw := strings.TrimSpace(string(b))
		if !passwordPattern.MatchString(pw) {
			return "", fmt.Errorf("%w: %s holds no valid password; delete it to make a new one", ErrRefused, path)
		}
		return pw, os.Chmod(path, 0o600)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	pw := hex.EncodeToString(raw)
	if _, err := os.Stat(filepath.Join(s.dataDir(), ".doltcfg", "privileges.db")); err == nil {
		if err := s.setRootPassword(ctx, pw); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := writeFileAtomic(path, []byte(pw+"\n"), 0o600); err != nil {
		return "", err
	}
	return pw, nil
}

// setRootPassword runs `dolt sql` with no server. The statement reaches dolt
// on stdin, so no argv holds the password. Dolt echoes a failed statement,
// so its output is discarded.
func (s *Supervisor) setRootPassword(ctx context.Context, pw string) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.StartTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.Binary, "sql")
	cmd.Dir = s.dataDir()
	cmd.Env = s.doltEnv()
	cmd.Stdin = strings.NewReader("ALTER USER 'root'@'localhost' IDENTIFIED BY '" + pw + "';\n")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: dolt sql could not set the root password: %v", ErrUnavailable, err)
	}
	return nil
}

func (s *Supervisor) doltEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + s.rootDir(),
		"DOLT_ROOT_PATH=" + s.rootDir(),
		"DOLT_DISABLE_EVENT_FLUSH=1",
		"TZ=UTC",
	}
}

// spawn starts one server. The old process, if any, is ours and has exited,
// so its socket file is stale: dolt 2.2.0 does not replace a stale socket
// file, and serves TCP only when one exists.
func (s *Supervisor) spawn() error {
	if err := os.Remove(s.cfg.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(s.cfg.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	// dolt 2.2.0 always opens a TCP listener and refuses port 0, so the
	// supervisor picks a free loopback port.
	cmd := exec.Command(s.cfg.Binary, "sql-server",
		"--socket="+s.cfg.Socket, "--host=127.0.0.1", "--port="+strconv.Itoa(port),
		"--data-dir="+s.dataDir())
	s.mu.Lock()
	pw := s.password
	s.mu.Unlock()
	cmd.Dir = s.dataDir()
	// The environment, unlike argv, is readable only by this OS user.
	cmd.Env = append(s.doltEnv(), "DOLT_ROOT_PASSWORD="+pw)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = logf.Close()
		close(exited)
	}()
	s.mu.Lock()
	s.cmd, s.exited = cmd, exited
	s.mu.Unlock()
	_ = writeFileAtomic(filepath.Join(s.cfg.Dir, "dolt.pid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600)
	return nil
}

// waitReady polls the socket until the server answers, the process exits or
// ctx ends.
func (s *Supervisor) waitReady(ctx context.Context) error {
	s.mu.Lock()
	exited := s.exited
	s.mu.Unlock()
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		if probe(s.cfg.Socket) == nil {
			return nil
		}
		select {
		case <-exited:
			return fmt.Errorf("%w: dolt sql-server exited during start; see %s", ErrUnavailable, s.cfg.LogPath)
		case <-ctx.Done():
			return fmt.Errorf("%w: dolt sql-server not ready: %w", ErrUnavailable, ctx.Err())
		case <-t.C:
		}
	}
}

// killCurrent sends SIGKILL to the current server, by its own PID only.
func (s *Supervisor) killCurrent() {
	s.mu.Lock()
	cmd, exited := s.cmd, s.exited
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	<-exited
}

// loop watches the current server and restarts it when it exits or fails
// HealthFailures checks in a row.
func (s *Supervisor) loop() {
	defer close(s.done)
	tick := time.NewTicker(s.cfg.HealthInterval)
	defer tick.Stop()
	failures := 0
	for {
		s.mu.Lock()
		exited := s.exited
		s.mu.Unlock()
		reason := ""
		select {
		case <-s.stop:
			return
		case <-exited:
			reason = "exited"
		case <-tick.C:
			if probe(s.cfg.Socket) == nil {
				failures = 0
				continue
			}
			failures++
			if failures < s.cfg.HealthFailures {
				continue
			}
			reason = fmt.Sprintf("failed %d health checks", failures)
			s.killCurrent()
		}
		failures = 0
		if !s.restart(reason) {
			return
		}
	}
}

// restart starts a new server with exponential backoff until one is ready.
// It returns false when Stop interrupts it.
func (s *Supervisor) restart(reason string) bool {
	backoff := s.cfg.MinBackoff
	for {
		select {
		case <-s.stop:
			return false
		case <-time.After(backoff):
		}
		if err := s.spawn(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StartTimeout)
			// Stop ends the wait at once, so no stop waits for a whole start.
			go func() {
				select {
				case <-s.stop:
					cancel()
				case <-ctx.Done():
				}
			}()
			err = s.waitReady(ctx)
			cancel()
			if err == nil {
				s.mu.Lock()
				s.count++
				ev := RestartEvent{N: s.count, PID: s.cmd.Process.Pid, At: time.Now(), Reason: reason}
				s.mu.Unlock()
				select {
				case s.restarts <- ev:
				default:
				}
				return true
			}
			s.killCurrent()
		}
		backoff *= 2
		if backoff > s.cfg.MaxBackoff {
			backoff = s.cfg.MaxBackoff
		}
	}
}

// probe connects to the socket and reads the start of the MySQL handshake.
func probe(socket string) error {
	c, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil {
		return err
	}
	// The handshake's payload starts with protocol version 10.
	if buf[4] != 10 {
		return fmt.Errorf("unexpected handshake byte %d", buf[4])
	}
	return nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

var doltVersionRe = regexp.MustCompile(`(?m)^dolt version (\S+)`)

// CheckDoltVersion refuses any dolt that is not exactly PinnedDolt.
func CheckDoltVersion(ctx context.Context, bin string, env []string, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Env = env
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%w: dolt version: %v", ErrVersion, err)
	}
	m := doltVersionRe.FindSubmatch(out)
	if m == nil {
		return fmt.Errorf("%w: dolt version printed %q", ErrVersion, bytes.TrimSpace(out))
	}
	if string(m[1]) != PinnedDolt {
		return fmt.Errorf("%w: dolt %s at %s, want %s", ErrVersion, m[1], bin, PinnedDolt)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
