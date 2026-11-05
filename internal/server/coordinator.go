package server

import (
	"context"
	"errors"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

// coordinatorRestart is `loop coordinator restart --project <p>`, which
// only the operator's shell runs. The scheduler closes p's coordinator
// session, makes its checkout again when it is missing, and starts a fresh
// session with the work the last one left open.
func (s *Server) coordinatorRestart(ctx context.Context, call *rpc.Call) cli.Result {
	if s.Sched == nil {
		return cli.Reject(cli.CodeNotFound, "no_coordinator", "this server runs no scheduler, so it starts no coordinator")
	}
	project := call.Inv.Flags["project"]
	cs, err := s.Sched.RestartCoordinator(ctx, project)
	switch {
	case errors.Is(err, config.ErrProjectNotFound):
		return cli.Reject(cli.CodeNotFound, "unknown_project", "no project "+project+" is registered; `loop project list` lists each one")
	case errors.Is(err, sched.ErrNoProjectConfig):
		return cli.Reject(cli.CodeNotFound, "no_project_config", "the default branch of "+project+" holds no "+config.ProjectConfigPath+", so it has no coordinator")
	case err != nil:
		return cli.Internal(err)
	}
	return cli.Found(map[string]any{"project": project, "run_id": cs.RunID, "checkout": cs.Checkout})
}
