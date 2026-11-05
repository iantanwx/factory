package server

import (
	"context"
	"errors"
	"maps"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/direct"
	"github.com/iantanwx/loop/internal/merge/ghqueue"
	"github.com/iantanwx/loop/internal/merge/mergify"
	"github.com/iantanwx/loop/internal/rpc"
)

// Merge wires the merge interface into a server. Pass Setup in
// Options.Setup after the GitHub setup, because it uses the GitHub
// service and its `project add` hook must run after the GitHub hook,
// which fetches the default branch:
//
//	gh := &server.GitHub{}
//	mg := &server.Merge{GitHub: gh}
//	opts.Setup = append(opts.Setup, gh.Setup, mg.Setup)
//
// Setup composes the three mechanisms and the merge-handoff job, adds the
// mechanism check to `project add` and serves `merge status`. The
// scheduler registers
// HandOff.TickProject as the merge-handoff native job. Each tick lands the
// project's settings change first, through `settings set`'s service.
type Merge struct {
	GitHub *GitHub
	// Mechanisms replaces the composed mechanism of each name it holds. A
	// test puts the fake there.
	Mechanisms map[config.MergeMechanism]merge.Mechanism

	// HandOff is set by Setup. It is nil on a server without beads.
	HandOff *merge.HandOff
}

// Setup composes the hand-off job over the server's store, machine,
// projects and GitHub service.
func (m *Merge) Setup(s *Server) error {
	if m.GitHub == nil || m.GitHub.Service == nil {
		return errors.New("server: the merge setup needs the GitHub setup first")
	}
	if s.Machine == nil {
		return nil
	}
	svc := m.GitHub.Service
	loader := &config.Loader{LKG: config.StoreLKG(s.Store), Alerts: config.StoreAlerter(s.Store),
		Check: config.StageCheck(&config.HostLoader{Path: s.HostPath}), Kernel: s.opts.Sched.Pack.Skills}
	// The source reads the default branch in the project's mirror, which
	// the reactor's tick and the GitHub add hook keep current (KTD20). It
	// reads the mirror by path, so it serves `project add` before the
	// project is registered.
	source := func(ctx context.Context, p config.Project) (*config.ProjectSnapshot, error) {
		base, err := svc.DefaultBranch(ctx, p)
		if err != nil {
			return nil, err
		}
		mirror := svc.Mirror(p)
		l, err := loader.Load(ctx, p.ID, config.Refs{Read: mirror.Show, Tree: mirror.Tree}, base)
		if err != nil {
			return nil, err
		}
		return &l.ProjectSnapshot, nil
	}
	mechs := map[config.MergeMechanism]merge.Mechanism{
		config.MergeMergify:     mergify.New(svc),
		config.MergeGitHubQueue: ghqueue.New(svc),
		config.MergeDirect:      direct.New(svc),
	}
	maps.Copy(mechs, m.Mechanisms)
	lander, err := s.wireSettingsSet(svc, mechs)
	if err != nil {
		return err
	}
	job, err := merge.New(merge.Config{
		GitHub: svc, Machine: s.Machine, Store: s.Store, Projects: s.Projects, Source: source,
		Mechanisms: mechs, Settings: lander, Logger: s.RPC.Logger(),
	})
	if err != nil {
		return err
	}
	s.Projects.AddHooks = append(s.Projects.AddHooks, job.AddHook)
	m.HandOff = job
	return s.RPC.Handle("merge status", mergeStatus(s))
}

// mergeStatus answers `loop merge status` from the hand-off job's record.
// It reads no GitHub, so it answers while GitHub is down or rate limited.
func mergeStatus(s *Server) rpc.Handler {
	return func(ctx context.Context, call *rpc.Call) cli.Result {
		out, err := merge.Status(ctx, s.Store, call.Inv.Flags["project"], call.Inv.Flags["all"] == "true")
		if err != nil {
			return cli.Internal(err)
		}
		if out, err = reachable(ctx, out, func(t merge.Tracked) coord.Record {
			return coord.Record{Kind: coord.KindItem, ID: t.Item, Project: t.Project}
		}); err != nil {
			return cli.Internal(err)
		}
		return cli.List(out)
	}
}
