package sched

import (
	"slices"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// qaWorld is a world whose pack holds the QA runbook and loop-impeccable,
// in a project whose config is cfg and runs the QA stage.
func qaWorld(t *testing.T, cfg string) *world {
	t.Helper()
	w := newWorld(t)
	w.project.set(snapshot(t, cfg+"stages:\n  qa: {}\nmerge:\n  mechanism: direct\n", ""))
	p := impeccablePack()
	p.FS.(fstest.MapFS)["loop-qa/SKILL.md"] = &fstest.MapFile{Data: []byte("# runbook\n")}
	w.cfg.Pack = p
	w.rebuild()
	return w
}

// C13: the brief of an admitted QA run names stage qa, the QA runbook and
// the design skill sched.Select gives it, and the run's row records that
// design skill, or "" with none.
func TestC13TheQARunsBriefNamesTheStageTheRunbookAndItsDesignSkill(t *testing.T) {
	for _, tc := range []struct {
		name, cfg, design string
	}{
		{"a ui block whose paths meet the ticket", uiConfig, "loop-impeccable"},
		{"ui.design_skill house-style", uiConfig + "  design_skill: house-style\n", "house-style"},
		{"no ui block", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := qaWorld(t, tc.cfg)
			id := w.pathTicket("web/src/App.tsx")
			w.move(id, machine.InQA)
			st := w.dispatch(config.StageQA)

			b, _, dirs := held(t, st)
			if b.Stage != config.StageQA || b.Runbook != ".claude/skills/loop-qa/SKILL.md" {
				t.Fatalf("brief stage %q, runbook %q; want qa and .claude/skills/loop-qa/SKILL.md", b.Stage, b.Runbook)
			}
			if b.DesignSkill != tc.design {
				t.Fatalf("brief design_skill %q, want %q", b.DesignSkill, tc.design)
			}
			if impeccable := slices.Contains(dirs, "loop-impeccable"); impeccable != (tc.design == "loop-impeccable") {
				t.Fatalf("checkout holds %v; loop-impeccable held = %v, want %v", dirs, impeccable, !impeccable)
			}
			if got := w.str(`SELECT design_skill FROM runs WHERE run_id = ?`, st.RunID); got != tc.design {
				t.Fatalf("runs.design_skill = %q, want %q", got, tc.design)
			}
		})
	}
}
