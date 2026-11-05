package server

import (
	"context"
	"errors"
	"strconv"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

// run list answers this many runs when --limit is absent, and at most
// maxRunList.
const (
	defaultRunList = 50
	maxRunList     = 500
)

// wireRuns registers the run reads. They read the store only, so every
// server serves them.
func (s *Server) wireRuns() error {
	for path, h := range map[string]rpc.Handler{
		"run show":   s.runShow,
		"run list":   s.runList,
		"run events": s.runEvents,
	} {
		if err := s.RPC.Handle(path, h); err != nil {
			return err
		}
	}
	return nil
}

func runFault(err error) cli.Result {
	if errors.Is(err, sched.ErrNoRun) {
		return cli.Reject(cli.CodeNotFound, "no_run", err.Error())
	}
	return cli.Internal(err)
}

func (s *Server) runShow(ctx context.Context, call *rpc.Call) cli.Result {
	v, err := sched.ShowRun(ctx, s.Store, call.Inv.Arg("run"))
	if err != nil {
		return runFault(err)
	}
	return cli.Found(v)
}

func (s *Server) runList(ctx context.Context, call *rpc.Call) cli.Result {
	f := sched.RunFilter{Project: call.Inv.Flags["project"], Stage: call.Inv.Flags["stage"],
		Live: call.Inv.Flags["live"] == "true", Limit: defaultRunList}
	if raw := call.Inv.Flags["limit"]; raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxRunList {
			return cli.Reject(cli.CodeUsage, "bad_limit", "--limit takes 1 to 500, got "+raw)
		}
		f.Limit = n
	}
	if c, ok := rpc.CallerFrom(ctx); ok && c.Reach == coord.ReachProject && f.Project == "" {
		f.Project = c.Project
	}
	out, err := sched.ListRuns(ctx, s.Store, f)
	if err != nil {
		return cli.Internal(err)
	}
	if out, err = reachable(ctx, out, func(r sched.RunView) coord.Record {
		return coord.Record{Kind: coord.KindRun, ID: r.RunID, Project: r.Project}
	}); err != nil {
		return cli.Internal(err)
	}
	return cli.List(out)
}

func (s *Server) runEvents(ctx context.Context, call *rpc.Call) cli.Result {
	out, err := sched.RunEvents(ctx, s.Store, call.Inv.Arg("run"))
	if err != nil {
		return runFault(err)
	}
	return cli.List(out)
}
