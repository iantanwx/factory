package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/rpc"
)

// MirrorDir holds one bare mirror for each project in the instance
// directory (KTD14).
const MirrorDir = "mirrors"

// GitHub wires the GitHub actor and reactor into a server. Pass
// Setup in Options.Setup and Reconcile in Options.Reconcile:
//
//	gh := &server.GitHub{}
//	opts.Setup = append(opts.Setup, gh.Setup)
//	opts.Reconcile = append(opts.Reconcile, gh.Reconcile)
//
// Setup registers the GitHub verbs, and it adds the mirror and branch-rules
// check to `project add`. Reconcile runs after the machine's reconcile,
// before the socket binds. The scheduler ticks Reactor.
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
	svc, err := github.New(github.Config{
		Apps: func() ([]github.AppKey, error) {
			// Host config again, so an App that `loop register` adds
			// reaches this running server.
			cfg, err := config.LoadHost(s.HostPath)
			if err != nil {
				return nil, err
			}
			var keys []github.AppKey
			for _, a := range cfg.Credentials.Apps() {
				name := a.PrivateKeySecret
				keys = append(keys, github.AppKey{Owner: a.Owner, ID: a.AppID, Key: func(ctx context.Context) ([]byte, error) {
					if name == "" {
						return nil, github.ErrNotConfigured
					}
					return s.Secrets.Get(ctx, config.HostScope, name)
				}})
			}
			return keys, nil
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
	if s.Inbox != nil {
		// A sign-off answer merges the document PR (ADR 0010).
		s.Inbox.SetGitHub(svc)
	}
	if s.Machine != nil {
		// review submit posts its body on the item's PR before the
		// verdict is recorded.
		s.Machine.SetReviewCommenter(svc.ReviewComment)
		// review submit --head compares the head with the PR's live
		// head before the verdict is recorded.
		s.Machine.SetPRHeadReader(svc.PRHead)
		// A code pass needs green CI at the head it names.
		s.Machine.SetPassGate(svc.PassCI)
		// work supersede reads its evidence and the ticket's pull
		// request back before the close is recorded.
		s.Machine.SetSupersedeChecker(svc.SupersedeEvidence)
	}
	if s.Sched != nil {
		// A repair's brief carries the parsed log of its red build.
		s.Sched.SetRedBuildReader(svc.RedBuild)
	}
	if s.Admission != nil {
		// The PR head enters a code review's retry identity, so a push
		// offers a held review again.
		s.Admission.SetPRHeadReader(svc.PRHead)
	}
	return s.RPC.Handle("doctor-github", g.doctorVerb(s))
}

// doctorVerb answers `loop doctor-github`: the checks of `loop doctor`
// that need the platform credentials, which never leave the server. The
// client-side doctor calls it. A host verb, so only the host-admin caller
// outside every checkout reaches it.
func (g *GitHub) doctorVerb(s *Server) rpc.Handler {
	return func(ctx context.Context, _ *rpc.Call) cli.Result {
		out, err := g.Doctor(ctx, s.Projects)
		if err != nil {
			return cli.Internal(err)
		}
		return cli.List(out)
	}
}

// Reconcile is the reactor's start-up step. It does nothing before Setup.
func (g *GitHub) Reconcile(ctx context.Context) error {
	if g.Reactor == nil {
		return nil
	}
	return g.Reactor.Reconcile(ctx)
}
