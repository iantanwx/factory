package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

// wireStages registers the stage verbs.
func (s *Server) wireStages() error {
	if err := s.RPC.Handle("stage pause", s.stagePause); err != nil {
		return err
	}
	if err := s.RPC.Handle("stage resume", s.stageResume); err != nil {
		return err
	}
	return s.RPC.Handle("stage list", s.stageList)
}

// stageList is `loop stage list [--project]`. It reads the scheduler's own
// firings, so each row prints the schedule the scheduler fires on.
func (s *Server) stageList(ctx context.Context, call *rpc.Call) cli.Result {
	project := call.Inv.Flags["project"]
	if project != "" {
		if _, err := s.Projects.Show(project); err != nil {
			return projectFault(err)
		}
	}
	if s.Sched == nil {
		return cli.Reject(cli.CodeNotImplemented, "no_scheduler", "this server runs no scheduler, so no stage fires")
	}
	rows, err := s.Sched.StageList(ctx, project)
	if err != nil {
		return cli.Internal(err)
	}
	if rows, err = reachable(ctx, rows, func(r sched.StageRow) coord.Record {
		return coord.Record{Kind: coord.KindProject, ID: r.Project, Project: r.Project}
	}); err != nil {
		return cli.Internal(err)
	}
	return cli.List(rows)
}

// pauseProject is the project of a pause or a resume: --project, or else
// the caller's own project for a project's coordinator. The loop
// project's coordinator, the old session and the host pause every project
// with no --project (ADR 0015 decision 4).
func pauseProject(ctx context.Context, call *rpc.Call) string {
	if p := call.Inv.Flags["project"]; p != "" {
		return p
	}
	if c, ok := rpc.CallerFrom(ctx); ok && c.Reach == coord.ReachProject {
		return c.Project
	}
	return ""
}

// stagePause is `loop stage pause <stage> [--project] --reason`. It holds
// a run stage or an agent job in one project, or in every project. A
// native job passes no admission, so it does not pause.
func (s *Server) stagePause(ctx context.Context, call *rpc.Call) cli.Result {
	stage, project := call.Inv.Arg("stage"), pauseProject(ctx, call)
	reason := strings.TrimSpace(call.Inv.Flags["reason"])
	if reason == "" {
		return cli.Reject(cli.CodeUsage, "no_reason", "--reason says why the stage pauses; the inbox quotes it until `loop stage resume`")
	}
	if r := s.Redactor.Refusal("secret_in_reason", "reason", reason); r != nil {
		return r.Result()
	}
	if project != "" {
		if _, err := s.Projects.Show(project); err != nil {
			return projectFault(err)
		}
	}
	hc, err := config.LoadHost(s.HostPath)
	if err != nil {
		return cli.Internal(err)
	}
	if _, job := hc.AgentJob(stage); !job && !slices.Contains(config.RunStages(), stage) {
		return cli.Reject(cli.CodeUsage, "not_a_run_stage",
			fmt.Sprintf("%q is no run stage and no agent job of host config; a native job does not pause", stage))
	}
	p, err := admission.Pause(ctx, s.Store, admission.StagePause{Project: project, Stage: stage, Reason: reason, By: call.Principal.Actor()})
	if errors.Is(err, admission.ErrAlreadyPaused) {
		return cli.Reject(cli.CodeAlreadyExists, "already_paused",
			fmt.Sprintf("%s is already paused in %s: %q; `loop stage resume` ends that pause first", stage, pauseScope(project), p.Reason))
	}
	if err != nil {
		return cli.Internal(err)
	}
	return cli.Found(p)
}

// stageResume is `loop stage resume <stage> [--project]`. It ends the
// pause of its own scope only, so a project resume leaves a pause of every
// project in place.
func (s *Server) stageResume(ctx context.Context, call *rpc.Call) cli.Result {
	stage, project := call.Inv.Arg("stage"), pauseProject(ctx, call)
	p, err := admission.Resume(ctx, s.Store, project, stage, call.Principal.Actor())
	if errors.Is(err, admission.ErrNotPaused) {
		return s.noPause(ctx, project, stage)
	}
	if err != nil {
		return cli.Internal(err)
	}
	return cli.Found(struct {
		admission.StagePause
		Resumed bool `json:"resumed"`
	}{p, true})
}

// noPause refuses a resume of a scope with no pause, and names each pause
// that holds the stage.
func (s *Server) noPause(ctx context.Context, project, stage string) cli.Result {
	var all []admission.StagePause
	if err := s.Store.Read(ctx, func(tx *sql.Tx) (err error) {
		all, err = admission.Pauses(ctx, tx)
		return err
	}); err != nil {
		return cli.Internal(err)
	}
	var held []string
	for _, p := range all {
		if p.Stage == stage {
			held = append(held, fmt.Sprintf("%s (%q)", pauseScope(p.Project), p.Reason))
		}
	}
	hint := fmt.Sprintf("no pause holds %s in %s", stage, pauseScope(project))
	if len(held) > 0 {
		hint += "; it is paused in " + strings.Join(held, ", ")
	}
	return cli.Reject(cli.CodeNotFound, "no_pause", hint)
}

// pauseScope names the scope of a pause.
func pauseScope(project string) string {
	if project == "" {
		return "every project"
	}
	return "project " + project
}
