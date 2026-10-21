package server

import (
	"context"
	"errors"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/direct"
	"github.com/iantanwx/loop/internal/merge/ghqueue"
	"github.com/iantanwx/loop/internal/merge/mergify"
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
// Setup composes the three mechanisms and the merge-handoff job, and it
// adds the mechanism check to `project add`. The scheduler registers
// HandOff.TickProject as the merge-handoff native job.
type Merge struct {
	GitHub *GitHub

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
	loader := &config.Loader{LKG: config.StoreLKG(s.Store), Alerts: config.StoreAlerter(s.Store)}
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
		l, err := loader.Load(ctx, p.ID, func(ctx context.Context, ref, path string) ([]byte, error) {
			return mirror.Show(ctx, ref, path)
		}, base)
		if err != nil {
			return nil, err
		}
		return &l.ProjectSnapshot, nil
	}
	job, err := merge.New(merge.Config{
		GitHub: svc, Machine: s.Machine, Store: s.Store, Projects: s.Projects, Source: source,
		Mechanisms: map[config.MergeMechanism]merge.Mechanism{
			config.MergeMergify:     mergify.New(svc),
			config.MergeGitHubQueue: ghqueue.New(svc),
			config.MergeDirect:      direct.New(svc),
		},
		Logger: s.RPC.Logger(),
	})
	if err != nil {
		return err
	}
	s.Projects.AddHooks = append(s.Projects.AddHooks, job.AddHook)
	m.HandOff = job
	return nil
}
