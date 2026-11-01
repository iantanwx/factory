package sched

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/retro"
	"github.com/iantanwx/loop/internal/store"
)

const selectStages = "stages:\n  implementer: {}\n  planner: {}\n  reproducer: {}\n  retro: {}\nmerge:\n  mechanism: direct\n"

// selectWorld is a world whose scheduler holds impeccablePack, in a
// project whose config is cfg.
func selectWorld(t *testing.T, cfg string) *world {
	t.Helper()
	w := newWorld(t)
	w.project.set(snapshot(t, cfg+selectStages, ""))
	w.cfg.Pack = impeccablePack()
	w.rebuild()
	return w
}

// specEpic makes an epic that waits for its planner.
func (w *world) specEpic() string {
	w.t.Helper()
	return w.item("epic", machine.SpecNeeded, map[string]string{machine.FactPRDMerged: "abc123"})
}

// pathTicket makes a todo ticket whose "May change" list holds may.
func (w *world) pathTicket(may ...string) string {
	w.t.Helper()
	ctx := context.Background()
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "Build the screen", Description: filesBody(may, nil), Type: "task"})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: machine.Todo}); err != nil {
		w.t.Fatal(err)
	}
	return is.ID
}

// closedBug makes a canceled bug and queues its retro, as the machine's
// Terminal hook does when the bug closes.
func (w *world) closedBug() {
	w.t.Helper()
	bug := w.bug()
	w.move(bug, machine.Canceled)
	if err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		return retro.Enqueue(tx, machine.TerminalEvent{Project: prj, Item: bug, Kind: machine.KindBug, Status: machine.Canceled})
	}); err != nil {
		w.t.Fatal(err)
	}
}

// held reads what a started run's checkout holds: its brief and the
// loop-* directories under its skill root.
func held(t *testing.T, st *Started) (b brief.Brief, raw []byte, dirs []string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(st.Checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(st.Checkout, brief.SkillRoot(agenthost.Claude)))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		dirs = append(dirs, e.Name())
	}
	return b, raw, dirs
}

// assertNoImpeccable fails the test when the run holds loop-impeccable in
// its checkout or names it in its brief.
func assertNoImpeccable(t *testing.T, what string, st *Started) {
	t.Helper()
	b, _, dirs := held(t, st)
	if slices.Contains(dirs, "loop-impeccable") || slices.Contains(b.Skills, "loop-impeccable") || b.DesignSkill != "" {
		t.Fatalf("%s holds %v, lists %v, design_skill %q; want no loop-impeccable", what, dirs, b.Skills, b.DesignSkill)
	}
	if !slices.Contains(dirs, "loop-implementer") {
		t.Fatalf("%s holds %v; the kernel skills are gone", what, dirs)
	}
}

// assertImpeccable fails the test unless the run holds loop-impeccable,
// lists it and names it its design skill.
func assertImpeccable(t *testing.T, what string, st *Started) {
	t.Helper()
	b, _, dirs := held(t, st)
	if !slices.Contains(dirs, "loop-impeccable") || !slices.Contains(b.Skills, "loop-impeccable") || b.DesignSkill != "loop-impeccable" {
		t.Fatalf("%s holds %v, lists %v, design_skill %q; want loop-impeccable", what, dirs, b.Skills, b.DesignSkill)
	}
}

// C11: in a project with no ui key, a stage run on an epic and one on a
// ticket whose "May change" list holds a UI path hold no loop-impeccable.
func TestC11AProjectWithNoUIKeyGivesNoCheckoutLoopImpeccable(t *testing.T) {
	w := selectWorld(t, "")
	w.specEpic()
	assertNoImpeccable(t, "a planner run on an epic", w.dispatch(machine.StagePlanner))
	w.pathTicket("web/App.tsx")
	assertNoImpeccable(t, "an implementer run on a ticket", w.dispatch(machine.StageImplementer))
}

// C12: in a project that declares ui, a stage run on an epic holds
// loop-impeccable and names it its design skill.
func TestC12TheEpicLevelHoldsTheSkillWhenUIIsDeclared(t *testing.T) {
	w := selectWorld(t, uiConfig)
	w.specEpic()
	assertImpeccable(t, "a planner run on an epic", w.dispatch(machine.StagePlanner))
}

// The ticket's done means: a ticket run whose "May change" list holds a
// path under ui.paths holds loop-impeccable, and one outside holds none.
func TestATicketRunHoldsTheSkillOnlyWhenItsPathsMeetTheUI(t *testing.T) {
	w := selectWorld(t, uiConfig)
	w.pathTicket("web/src/App.tsx")
	assertImpeccable(t, "a ticket run on web/src/App.tsx", w.dispatch(machine.StageImplementer))
	w.pathTicket("internal/x.go")
	assertNoImpeccable(t, "a ticket run on internal/x.go", w.dispatch(machine.StageImplementer))
}

// C15: a ui block that merges while a run lives changes nothing the run
// holds. The next run on an epic holds the skill.
func TestC15AMergeDuringARunChangesNoSkillItHolds(t *testing.T) {
	w := selectWorld(t, "")
	w.specEpic()
	st := w.dispatch(machine.StagePlanner)
	_, before, _ := held(t, st)

	w.project.set(snapshot(t, uiConfig+selectStages, ""))
	w.tick()

	assertNoImpeccable(t, "the live run after the merge", st)
	if _, after, _ := held(t, st); !bytes.Equal(before, after) {
		t.Fatalf("the live run's brief changed after the merge:\nbefore %s\nafter  %s", before, after)
	}
	w.host.Exit(st.Session.ID, 0)
	w.tick()
	w.specEpic()
	assertImpeccable(t, "the next run on an epic", w.dispatch(machine.StagePlanner))
}

// C18: in a project that declares ui, a reproducer run on a bug and a
// retro run on a bug hold no level, so neither holds loop-impeccable.
func TestC18RunsWithNoLevelHoldNoConditionalSkill(t *testing.T) {
	w := selectWorld(t, uiConfig)
	w.bug()
	assertNoImpeccable(t, "a reproducer run on a bug", w.dispatch(machine.StageReproducer))
	w.closedBug()
	assertNoImpeccable(t, "a retro run on a bug", w.dispatch(retro.Stage))
}
