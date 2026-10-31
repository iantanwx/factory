package sched

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// The effort a stage's config names reaches the harness's argv.
func TestStageConfigEffortReachesTheArgv(t *testing.T) {
	w := newWorld(t)
	w.project.set(snapshot(t, "stages:\n  implementer:\n    effort: max\nmerge:\n  mechanism: direct\n", ""))
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	if st.Profile.Effort != "max" {
		t.Fatalf("profile = %+v", st.Profile)
	}
	argv := w.host.Launches()[0].Argv
	if i := slices.Index(argv, "--effort"); i < 0 || argv[i+1] != "max" {
		t.Fatalf("argv = %q", argv)
	}
}

// A stage whose config names no effort runs at the host's default effort
// for that stage, ported from ROW_PINS in alpha's stage-models.ts.
func TestStageWithNoEffortRunsAtTheStageDefault(t *testing.T) {
	w := newWorld(t)
	w.ticket(machine.Todo)
	w.dispatch(machine.StageImplementer)
	argv := w.host.Launches()[0].Argv
	if i := slices.Index(argv, "--effort"); i < 0 || argv[i+1] != "high" {
		t.Fatalf("argv = %q", argv)
	}
}

// C8: the default Claude model is a full id, so a stage that names no
// model requests the id the harness records, and never an alias.
func TestC8EachDefaultModelIsAFullID(t *testing.T) {
	m := DefaultModels[agenthost.Claude]
	if m != "claude-opus-5-5" || !config.ClaudeModelID(m) {
		t.Fatalf("default Claude model = %q, want the full id claude-opus-5-5", m)
	}
}

// Each profile carries an effort: the stage's own on its configured
// harness, the stage default on each fallback harness.
func TestProfilesCarryTheEffort(t *testing.T) {
	pc := &config.ProjectConfig{Stages: map[string]config.StageConfig{
		"implementer": {Effort: "max"},
	}}
	hc := &config.HostConfig{Harnesses: []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}}
	got := profiles("implementer", pc, hc)
	want := []Profile{
		{Harness: agenthost.Claude, Model: "claude-opus-5-5", Effort: "max"},
		{Harness: agenthost.Codex, Model: "gpt-5-codex", Effort: "high"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("implementer profiles = %+v", got)
	}
	cases := map[string]string{
		"implementer": "high", "code-reviewer": "high", "doc-reviewer": "medium",
		"spec-author": "high", "decomposer": "high", "reviser": "medium",
		"fixer": "medium", "reproducer": "medium", "retro": "medium", "intake": "medium", "nightly-qa": "",
	}
	for stage, effort := range cases {
		if got := profiles(stage, &config.ProjectConfig{}, &config.HostConfig{}); got[0].Effort != effort {
			t.Errorf("%s effort = %q, want %q", stage, got[0].Effort, effort)
		}
	}
}

// rejectTwice sends a ticket in review back to todo twice, as two rounds
// of the code-reviewer's review_reject do.
func (w *world) rejectTwice(id string) {
	w.t.Helper()
	ctx := context.Background()
	for round := range 2 {
		if round > 0 {
			if _, err := w.bx.Update(ctx, id, beadsx.UpdateSpec{IfStatus: machine.Todo, Status: machine.InReview}); err != nil {
				w.t.Fatal(err)
			}
		}
		res := w.m.Apply(ctx, machine.Request{Project: prj, Item: id, Verb: "review_reject", Actor: machine.Server("test")})
		if res.Exit != cli.ExitDone {
			w.t.Fatalf("review_reject %s: %+v", id, res.Refusal)
		}
	}
}

// Parity 1.15: after two code-review rejects, the next implementer run
// starts on the stage's escalated profile. A ticket with no second reject
// keeps the stage's own profile. Both starts wait for a receipt that the
// harness runs the requested model.
func TestTwoRejectsStartTheNextImplementerRunOnTheEscalatedModel(t *testing.T) {
	w := newWorld(t)
	plain := w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	spec := w.host.Launches()[0].Spec
	if st.Item != plain || spec.Model != "claude-opus-5-5" || spec.Effort != "high" || !spec.RequireVerifiedModel {
		t.Fatalf("a ticket with no reject starts %+v", spec)
	}

	twice := w.ticket(machine.InReview)
	w.rejectTwice(twice)
	st = w.dispatch(machine.StageImplementer)
	spec = w.host.Launches()[1].Spec
	if st.Item != twice || spec.Model != "claude-opus-5-5" || spec.Effort != "high" || !spec.RequireVerifiedModel {
		t.Fatalf("a ticket after two rejects starts %+v", spec)
	}
	if st.Profile.Model != spec.Model {
		t.Fatalf("the run records profile %+v, and the session runs %s", st.Profile, spec.Model)
	}
}

// A stage's escalated profile in project config wins over the default
// escalated model, and its effort reaches the start.
func TestStageConfigNamesTheEscalatedProfile(t *testing.T) {
	w := newWorld(t)
	w.project.set(snapshot(t, "stages:\n  implementer:\n    model: claude-sonnet-4-5\n    escalated:\n      model: claude-fable-5-1\n      effort: max\nmerge:\n  mechanism: direct\n", ""))
	id := w.ticket(machine.InReview)
	w.rejectTwice(id)
	w.dispatch(machine.StageImplementer)
	spec := w.host.Launches()[0].Spec
	if spec.Harness != agenthost.Claude || spec.Model != "claude-fable-5-1" || spec.Effort != "max" || !spec.RequireVerifiedModel {
		t.Fatalf("escalated start = %+v", spec)
	}
}

// The escalated profile has no fallback harness. A ticket that needs the
// stronger model waits while its harness is paused, and never starts on
// another harness's default model.
func TestEscalatedTicketWaitsForItsHarness(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(hc *config.HostConfig) {
		hc.Harnesses = []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}
	})
	w.rebuild()
	id := w.ticket(machine.InReview)
	w.rejectTwice(id)
	w.s.pauseHarness(context.Background(), agenthost.Claude, "test", w.clock.Now().Add(time.Hour))
	st, ref := w.s.Dispatch(context.Background(), prj, machine.StageImplementer)
	if st != nil || ref == nil || ref.Code != cli.CodeCapFull {
		t.Fatalf("an escalated ticket with claude paused: started %+v, refusal %+v", st, ref)
	}
	if n := len(w.host.Launches()); n != 0 {
		t.Fatalf("%d sessions started", n)
	}
}
