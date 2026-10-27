package server

import (
	"context"
	"fmt"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/settings"
)

// settingsService is the settings service over the server's host config,
// store and projects.
func (s *Server) settingsService() *settings.Service {
	host := &config.HostLoader{Path: s.HostPath}
	return &settings.Service{
		Host: host.Load,
		Loader: &config.Loader{LKG: config.StoreLKG(s.Store), Alerts: config.StoreAlerter(s.Store),
			Check: config.StageCheck(host)},
		Defaults: sched.Defaults,
		Repo:     s.settingsRepo,
		Store:    s.Store,
		Redactor: s.Redactor,
	}
}

// wireSettings registers settings show.
func (s *Server) wireSettings() error {
	svc := s.settingsService()
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

// wireSettingsSet registers settings set, which proposes each change
// through gh and checks a new merge mechanism against mechs. Merge.Setup
// calls it, and its merge-handoff job lands each change through the
// service it returns.
func (s *Server) wireSettingsSet(gh *github.Service, mechs map[config.MergeMechanism]merge.Mechanism) (*settings.Service, error) {
	svc := s.settingsService()
	svc.GitHub, svc.Forget = gh, s.caches.forget
	svc.Propose = func(ctx context.Context, project string, f github.FileChange) (int, string, error) {
		p, err := s.Projects.Show(project)
		if err != nil {
			return 0, "", err
		}
		pr, head, err := gh.ProposeFile(ctx, p, f)
		return pr.Number, head, err
	}
	svc.CheckMechanism = func(ctx context.Context, project string, name config.MergeMechanism) error {
		p, err := s.Projects.Show(project)
		if err != nil {
			return err
		}
		m, ok := mechs[name]
		if !ok {
			return fmt.Errorf("this server holds no merge mechanism %q", name)
		}
		return m.Check(ctx, p)
	}
	return svc, s.RPC.Handle("settings set", func(ctx context.Context, call *rpc.Call) cli.Result {
		project := call.Inv.Arg("project")
		if _, err := s.Projects.Show(project); err != nil {
			return projectFault(err)
		}
		v, set := call.Inv.Flags["value"]
		if set == (call.Inv.Flags["unset"] == "true") {
			return cli.Reject(cli.CodeUsage, "value_or_unset", "pass exactly one of --value <v> and --unset")
		}
		var value *string
		if set {
			value = &v
		}
		c, ref := svc.Set(ctx, settings.SetRequest{Project: project, Key: call.Inv.Arg("key"), Value: value,
			Reason: call.Inv.Flags["reason"], Actor: call.Principal.Kind})
		if ref != nil {
			return ref.Result()
		}
		return cli.Found(c)
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
