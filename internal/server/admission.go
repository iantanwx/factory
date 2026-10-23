package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

// composeAdmission builds admission over the host config file and the
// machine's beads resolver, unless Options.Claims names another guard.
func (s *Server) composeAdmission() (*admission.Admission, error) {
	base := s.opts.BaseFiles
	if base == nil {
		base = GitBase{Projects: s.Projects, Branch: sched.DefaultBaseBranch}
	}
	return admission.New(admission.Config{
		Store:   s.Store,
		Host:    &config.HostLoader{Path: s.HostPath, Alerts: config.StoreAlerter(s.Store)},
		Beads:   s.opts.Beads,
		Base:    base,
		Deps:    s.opts.Dependencies,
		Release: cli.Version,
	})
}

// workPeek is `loop work peek --stage`: the stage's work query, with no
// claim (R20). A run asks for its own project.
func (s *Server) workPeek(ctx context.Context, call *rpc.Call) cli.Result {
	project, ref := call.Project("a run asks for the work of its own project only")
	if ref != nil {
		return ref.Result()
	}
	// A run asks for its own work, so its own live run fills no cap here.
	var caller string
	if call.Principal.Kind == cli.PrincipalRun {
		caller = call.Principal.RunID
	}
	c, ref := s.Admission.PeekAs(ctx, project, call.Inv.Flags["stage"], caller)
	if ref != nil {
		return ref.Result()
	}
	if c == nil {
		return cli.Nothing()
	}
	return cli.Found(c)
}

// GitBase reads a project's base branch from the project's local directory
// with `git ls-tree`, at origin/<Branch>. It runs no hook and reads no user
// or system git config. A server with the GitHub mirror reads the bare
// mirror instead, which knows each project's base branch.
type GitBase struct {
	Projects *config.Projects
	Branch   string
}

// Files implements admission.BaseReader. The content id is the blob id.
func (g GitBase) Files(ctx context.Context, project string, prefixes []string) (map[string]string, error) {
	p, err := g.Projects.Show(project)
	if err != nil {
		return nil, err
	}
	out, err := gitx.Run(ctx, p.Path, "ls-tree", "-r", "-z", "--full-tree", "origin/"+g.Branch)
	if ge, ok := errors.AsType[*gitx.Error](err); ok {
		return nil, fmt.Errorf("git ls-tree origin/%s: %w: %s", g.Branch, ge.Err, strings.TrimSpace(ge.Stderr))
	}
	return gitx.TreeFiles(out, prefixes), nil
}
