package server

import (
	"context"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/settings"
)

// wireSettings registers the settings verbs.
func (s *Server) wireSettings() error {
	host := &config.HostLoader{Path: s.HostPath}
	svc := &settings.Service{
		Host: host.Load,
		Loader: &config.Loader{LKG: config.StoreLKG(s.Store), Alerts: config.StoreAlerter(s.Store),
			Check: config.StageCheck(host)},
		Defaults: sched.Defaults,
		Repo:     s.settingsRepo,
	}
	return s.RPC.Handle("settings show", func(ctx context.Context, call *rpc.Call) cli.Result {
		project := call.Inv.Arg("project")
		if _, err := s.Projects.Show(project); err != nil {
			return projectFault(err)
		}
		v, ref := svc.Show(ctx, project)
		if ref != nil {
			return ref.Result()
		}
		return cli.Found(v)
	})
}

// settingsRepo fetches the project's default branch with the base head
// source a new checkout starts from. That source copies the head into the
// project's clone as origin/<branch>, so the clone holds its history.
func (s *Server) settingsRepo(ctx context.Context, project string) (settings.Repo, error) {
	p, err := s.Projects.Show(project)
	if err != nil {
		return settings.Repo{}, err
	}
	refs := s.opts.Sched.Refs
	if refs == nil {
		refs = LocalRefs
	}
	_, branch, err := refs(ctx, p)
	if err != nil {
		return settings.Repo{}, err
	}
	head, err := s.baseHead()(ctx, p.ID, branch)
	if err != nil {
		return settings.Repo{}, err
	}
	return settings.Repo{Dir: p.Path, Branch: branch, Head: head}, nil
}
