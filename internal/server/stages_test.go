package server

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// pauseCounts counts the pause rows and the pause audit rows.
func (w *humanWorld) pauseCounts() (rows, audits int) {
	return w.count(`SELECT count(*) FROM stage_pauses`),
		w.count(`SELECT count(*) FROM audit WHERE action IN ('stage_paused', 'stage_resumed')`)
}

// C8: each bad pause or resume refuses with its code and rule, and writes
// no pause row and no audit row.
func TestC8BadPauseOrResumeChangesNothing(t *testing.T) {
	w := newHumanWorld(t, "")
	if env, code := w.coordinator("stage", "pause", machine.StageImplementer, "--project", "alpha", "--reason", "the first reason"); code != 0 {
		t.Fatalf("stage pause = %d %v", code, env)
	}
	for _, tc := range []struct {
		name       string
		args       []string
		code, rule string
		hint       string
	}{
		{"no reason", []string{"stage", "pause", machine.StageCodeReviewer}, cli.CodeUsage, "no_reason", ""},
		{"a reason of spaces", []string{"stage", "pause", machine.StageCodeReviewer, "--reason", "   "}, cli.CodeUsage, "no_reason", ""},
		{"an unknown project", []string{"stage", "pause", machine.StageCodeReviewer, "--project", "nope", "--reason", "why"},
			cli.CodeNotFound, "project_not_found", ""},
		{"a native job", []string{"stage", "pause", "merge-handoff", "--reason", "why"}, cli.CodeUsage, "not_a_run_stage", ""},
		{"no stage", []string{"stage", "pause", "nope", "--reason", "why"}, cli.CodeUsage, "not_a_run_stage", ""},
		{"a second pause of one scope", []string{"stage", "pause", machine.StageImplementer, "--project", "alpha", "--reason", "a second reason"},
			cli.CodeAlreadyExists, "already_paused", "the first reason"},
		{"a resume of a scope with no row", []string{"stage", "resume", machine.StageImplementer},
			cli.CodeNotFound, "no_pause", "alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := w.coordinator(tc.args...)
			if env["code"] != tc.code || env["rule"] != tc.rule {
				t.Fatalf("%v = %v, want %s %s", tc.args, env, tc.code, tc.rule)
			}
			if hint, _ := env["hint"].(string); !strings.Contains(hint, tc.hint) {
				t.Fatalf("hint %q does not name %q", hint, tc.hint)
			}
			if rows, audits := w.pauseCounts(); rows != 1 || audits != 1 {
				t.Fatalf("%d pause rows and %d audit rows, want the first pause's 1 and 1", rows, audits)
			}
		})
	}
}

// C8: an agent job of host config pauses, as a run stage does.
func TestAgentJobPauses(t *testing.T) {
	w := newHumanWorld(t, "")
	cfg, err := config.LoadHost(w.srv.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentJobs = map[string]config.AgentJob{"digest": {Cap: 1, Schedule: "0 9 * * *", Runbook: "docs/digest.md"}}
	if err := config.SaveHost(w.srv.HostPath, cfg); err != nil {
		t.Fatal(err)
	}
	env, code := w.coordinator("stage", "pause", "digest", "--reason", "the digest is noisy")
	if code != 0 {
		t.Fatalf("stage pause digest = %d %v", code, env)
	}
	if got := env["data"].(map[string]any); got["stage"] != "digest" || got["project"] != "" || got["reason"] != "the digest is noisy" {
		t.Fatalf("stage pause answered %v", got)
	}
}

// C9: a pause with no --project holds the stage in every project, and one
// with --project holds it in that project only. A project resume deletes
// its own row, and the stage stays paused in that project.
func TestC9PauseScopesStayApartThroughTheVerbs(t *testing.T) {
	ctx := context.Background()
	w := newHumanWorld(t, "")
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "beta", Repo: "iantanwx/beta", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	paused := func(project string) bool {
		t.Helper()
		ok, err := w.srv.Admission.Paused(ctx, project, machine.StageImplementer)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if env, code := w.coordinator("stage", "pause", machine.StageImplementer, "--project", "alpha", "--reason", "alpha only"); code != 0 {
		t.Fatalf("stage pause --project alpha = %d %v", code, env)
	}
	if !paused("alpha") || paused("beta") {
		t.Fatal("a pause of alpha does not hold alpha alone")
	}
	if env, code := w.coordinator("stage", "pause", machine.StageImplementer, "--reason", "every project"); code != 0 {
		t.Fatalf("stage pause = %d %v", code, env)
	}
	if !paused("beta") {
		t.Fatal("a pause of every project does not hold beta")
	}
	env, code := w.coordinator("stage", "resume", machine.StageImplementer, "--project", "alpha")
	if code != 0 {
		t.Fatalf("stage resume --project alpha = %d %v", code, env)
	}
	if got := env["data"].(map[string]any); got["project"] != "alpha" || got["reason"] != "alpha only" || got["resumed"] != true {
		t.Fatalf("stage resume answered %v", got)
	}
	if !paused("alpha") {
		t.Fatal("a resume of alpha ended the pause of every project")
	}
	if n := w.count(`SELECT count(*) FROM stage_pauses WHERE project = '' AND stage = ?`, machine.StageImplementer); n != 1 {
		t.Fatalf("%d pauses of every project, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM audit WHERE actor = 'coordinator:coord-1'
		AND action IN ('stage_paused', 'stage_resumed') AND json_extract(detail, '$.stage') = ?`, machine.StageImplementer); n != 3 {
		t.Fatalf("%d pause audit rows by the coordinator, want 3", n)
	}
}

// C7: a pause survives a server restart. It reads back with its reason,
// and a fire of the stage after the restart starts no run.
func TestC7PauseSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	w := newHumanWorld(t, "")
	todoTicket(t, w.bx)
	if env, code := w.coordinator("stage", "pause", machine.StageImplementer, "--project", "alpha", "--reason", "hold for the upgrade"); code != 0 {
		t.Fatalf("stage pause = %d %v", code, env)
	}
	w.restart()
	if n := w.count(`SELECT count(*) FROM stage_pauses WHERE project = 'alpha' AND stage = ? AND reason = 'hold for the upgrade'`,
		machine.StageImplementer); n != 1 {
		t.Fatalf("%d pause rows after the restart, want 1", n)
	}
	launches := len(w.host.Launches())
	if st, ref := w.srv.Sched.Dispatch(ctx, "alpha", machine.StageImplementer); st != nil || ref != nil {
		t.Fatalf("dispatch after the restart = %+v %+v, want nothing", st, ref)
	}
	if env, code := w.coordinator("work", "peek", "--stage", machine.StageImplementer, "--project", "alpha"); code != 1 {
		t.Fatalf("work peek after the restart = %d %v, want exit 1", code, env)
	}
	if n := len(w.host.Launches()); n != launches {
		t.Fatalf("the paused stage launched %d sessions", n-launches)
	}
}
