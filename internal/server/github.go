package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/retro"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/secrets"
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
	// GitConfig is github.Config.GitConfig. A test sets gittest.Config.
	GitConfig [][2]string

	// ProjectConfig reads each project's config for `loop doctor`. Setup
	// sets the scheduler's source when it is nil.
	ProjectConfig sched.ProjectSource

	// Service, Reactor and Secrets are set by Setup.
	Service *github.Service
	Reactor *github.Reactor
	Secrets *secrets.Store
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
		API: g.API, GitURL: g.GitURL, Protocols: g.Protocols, HTTP: g.HTTP, Sleep: g.Sleep, GitConfig: g.GitConfig,
		MirrorRoot: filepath.Join(s.Instance, MirrorDir),
		Store:      s.Store, Redactor: s.Redactor, Projects: s.Projects, Machine: s.Machine,
		Logger: s.RPC.Logger(), UserSkills: s.userSkills,
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
	g.Service, g.Reactor, g.Secrets = svc, github.NewReactor(svc), s.Secrets
	if g.ProjectConfig == nil {
		g.ProjectConfig = s.projectSource()
	}
	// The overwatch parts pulls, main and audit read through it.
	s.setOverwatchGitHub(svc)
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
		// qa report checks the report against the plan of its head over
		// the mirror, stores it, and enqueues its loop/qa check run.
		s.Machine.SetQAGate(svc.QAGate)
		s.Machine.SetQAReported(svc.QAReported)
		// work supersede reads its evidence and the ticket's pull
		// request back before the close is recorded.
		s.Machine.SetSupersedeChecker(svc.SupersedeEvidence)
		// A canceled epic's open document PR closes with no merge.
		s.Machine.SetDocPRCloser(g.Reactor.CloseDocPR)
	}
	if s.Retro != nil {
		// retro record reads the comments on each pull request of the
		// retro's items.
		s.Retro.Comments = retroComments(s.Projects, svc)
	}
	if s.Sched != nil {
		// A repair's brief carries the parsed log of its red build.
		s.Sched.SetRedBuildReader(svc.RedBuild)
		// The settlement of a simplify run drops the commit the server
		// holds for it.
		s.Sched.SetHeldDropper(svc.DropHeld)
	}
	if s.Admission != nil {
		// The PR head enters a code review's retry identity, so a push
		// offers a held review again.
		s.Admission.SetPRHeadReader(svc.PRHead)
	}
	if err := s.RPC.Handle("pr pending", g.prPending(s)); err != nil {
		return fmt.Errorf("server: github: %w", err)
	}
	return s.RPC.Handle("doctor-github", g.doctorVerb(s))
}

// userSkills reads the pin names of project's last good config, never the
// config of a pushed branch (ADR 0013 decision 1). A project with no last
// good config has none.
func (s *Server) userSkills(ctx context.Context, project string) ([]string, error) {
	raw, _, err := config.StoreLKG(s.Store).LoadLKG(ctx, project)
	if err != nil || raw == nil {
		return nil, err
	}
	cfg, err := config.ParseProjectConfig(raw)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cfg.Skills))
	for _, p := range cfg.Skills {
		names = append(names, p.Name)
	}
	return names, nil
}

// retroComments reads the comments on an item's pull requests for the
// retro's record. A GitHub error is its refusal, so an outage answers WAIT.
func retroComments(projects *config.Projects, svc *github.Service) retro.CommentReader {
	return func(ctx context.Context, project string, it machine.Item) ([]retro.Comment, *cli.Refusal) {
		p, err := projects.Show(project)
		if err != nil {
			r := github.Refusal(err)
			return nil, &r
		}
		cs, err := svc.ItemComments(ctx, p, it)
		if err != nil {
			r := github.Refusal(err)
			return nil, &r
		}
		out := make([]retro.Comment, len(cs))
		for i, c := range cs {
			out[i] = retro.Comment{ID: c.ID, Kind: c.Kind, Author: c.Author, Body: c.Body, At: c.CreatedAt}
		}
		return out, nil
	}
}

// prPending answers `loop pr pending`: the item pull requests of each
// project, or of the one --project names, that owe a person a reply,
// oldest first. A project whose GitHub read fails answers one row with
// its error, and the others still answer.
func (g *GitHub) prPending(s *Server) rpc.Handler {
	return func(ctx context.Context, call *rpc.Call) cli.Result {
		var projects []config.Project
		if id := call.Inv.Flags["project"]; id != "" {
			p, err := s.Projects.Show(id)
			if err != nil {
				return projectFault(err)
			}
			projects = []config.Project{p}
		} else {
			var err error
			if projects, err = s.Projects.List(); err != nil {
				return cli.Internal(err)
			}
		}
		var out []github.Pending
		for _, p := range projects {
			rows, err := g.Service.Pending(ctx, p)
			if err != nil {
				rows = []github.Pending{{Project: p.ID, Error: err.Error()}}
			}
			out = append(out, rows...)
		}
		slices.SortStableFunc(out, func(a, b github.Pending) int { return a.Since.Compare(b.Since) })
		return cli.List(out)
	}
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
