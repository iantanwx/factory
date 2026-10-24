package sched

import (
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
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

// Each profile carries an effort: the stage's own on its configured
// harness, the stage default on each fallback harness.
func TestProfilesCarryTheEffort(t *testing.T) {
	pc := &config.ProjectConfig{Stages: map[string]config.StageConfig{
		"implementer": {Effort: "max"},
	}}
	hc := &config.HostConfig{Harnesses: []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}}
	got := profiles("implementer", pc, hc)
	want := []Profile{
		{Harness: agenthost.Claude, Model: "opus", Effort: "max"},
		{Harness: agenthost.Codex, Model: "gpt-5-codex", Effort: "high"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("implementer profiles = %+v", got)
	}
	cases := map[string]string{
		"implementer": "high", "code-reviewer": "high", "doc-reviewer": "medium",
		"spec-author": "high", "decomposer": "high", "amender": "medium",
		"fixer": "medium", "reproducer": "medium", "intake": "medium", "nightly-qa": "",
	}
	for stage, effort := range cases {
		if got := profiles(stage, &config.ProjectConfig{}, &config.HostConfig{}); got[0].Effort != effort {
			t.Errorf("%s effort = %q, want %q", stage, got[0].Effort, effort)
		}
	}
}
