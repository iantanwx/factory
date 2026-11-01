package sched

import (
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// C2: with the simplify stage registered and StageEnabled unchanged, a
// project config that does not list simplify gets its firing, disabled, on
// the stage's default schedule (docs/plans/factorio-1r94-simplify-stage-spec.md).
func TestC2AConfigWithNoSimplifyEntryFiresNoSimplifyRun(t *testing.T) {
	sn := snapshot(t, "stages:\n  implementer: {}\n  code-reviewer: {}\nmerge:\n  mechanism: direct\n", "")
	firings := Firings(config.DefaultHost(t.TempDir()), []config.Project{{ID: prj}},
		func(string) *config.ProjectSnapshot { return sn }, Jobs{})
	var got []Firing
	for _, f := range firings {
		if f.Stage == "simplify" {
			got = append(got, f)
		}
	}
	want := Firing{Project: prj, Stage: "simplify", Kind: FiringRun, Enabled: false, Schedule: "1,6,11,16,21,26,31,36,41,46,51,56 * * * *"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("simplify firings = %+v, want [%+v]", got, want)
	}
}

// The simplify run edits code that ships, so it runs at high effort, as
// every code author does.
func TestSimplifyRunsAtHighEffortByDefault(t *testing.T) {
	pc, err := config.ParseProjectConfig([]byte("merge:\n  mechanism: direct\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := profiles("simplify", pc, config.DefaultHost(t.TempDir()))[0].Effort; got != "high" {
		t.Fatalf("simplify effort = %q, want high", got)
	}
}
