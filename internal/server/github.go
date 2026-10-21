package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
)

// MirrorDir holds one bare mirror for each project in the instance
// directory (KTD14).
const MirrorDir = "mirrors"

// GitHub wires the GitHub actor and reactor (U10) into a server. Pass
// Setup in Options.Setup and Reconcile in Options.Reconcile:
//
//	gh := &server.GitHub{}
//	opts.Setup = append(opts.Setup, gh.Setup)
//	opts.Reconcile = append(opts.Reconcile, gh.Reconcile)
//
// Setup registers the GitHub verbs, and it adds the mirror and branch-rules
// check to `project add`. Reconcile runs after the machine's reconcile,
// before the socket binds. The scheduler (U8) ticks Reactor.
type GitHub struct {
	// API, GitURL, Protocols and HTTP override GitHub's endpoints. A test
	// points them at local servers. Production leaves them empty.
	API       string
	GitURL    func(repo string) string
	Protocols []string
	HTTP      *http.Client
	Sleep     func(ctx context.Context, d time.Duration) error

	// Service and Reactor are set by Setup.
	Service *github.Service
	Reactor *github.Reactor
}

// Setup composes the service over the server's store, redactor, secret
// store, projects and machine.
func (g *GitHub) Setup(s *Server) error {
	cfg, err := config.LoadHost(s.HostPath)
	if err != nil {
		return err
	}
	app := cfg.Credentials.GitHubApp
	svc, err := github.New(github.Config{
		AppID: app.AppID,
		PrivateKey: func(ctx context.Context) ([]byte, error) {
			if app.PrivateKeySecret == "" {
				return nil, github.ErrNotConfigured
			}
			return s.Secrets.Get(ctx, config.HostScope, app.PrivateKeySecret)
		},
		API: g.API, GitURL: g.GitURL, Protocols: g.Protocols, HTTP: g.HTTP, Sleep: g.Sleep,
		MirrorRoot: filepath.Join(s.Instance, MirrorDir),
		Store:      s.Store, Redactor: s.Redactor, Projects: s.Projects, Machine: s.Machine,
		Logger: s.RPC.Logger(),
	})
	if err != nil {
		return err
	}
	for path, h := range svc.Handlers() {
		if err := s.RPC.Handle(path, h); err != nil {
			return fmt.Errorf("server: github: %w", err)
		}
	}
	s.Projects.AddHooks = append(s.Projects.AddHooks, svc.AddHook)
	g.Service, g.Reactor = svc, github.NewReactor(svc)
	return nil
}

// Reconcile is the reactor's start-up step. It does nothing before Setup.
func (g *GitHub) Reconcile(ctx context.Context) error {
	if g.Reactor == nil {
		return nil
	}
	return g.Reactor.Reconcile(ctx)
}
