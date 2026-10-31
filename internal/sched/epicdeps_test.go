package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/machine"
)

// item makes a bead of beadType in status, with each fact.
func (w *world) item(beadType, status string, facts map[string]string) string {
	w.t.Helper()
	ctx := context.Background()
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "A " + beadType, Type: beadType})
	if err != nil {
		w.t.Fatal(err)
	}
	meta := map[string]string{}
	for k, v := range facts {
		meta["loop."+k] = v
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: status, SetMetadata: meta}); err != nil {
		w.t.Fatal(err)
	}
	return is.ID
}

// epic makes an epic in status. A nonempty entry is its epic_entry fact.
func (w *world) epic(status, entry string) string {
	w.t.Helper()
	facts := map[string]string{}
	if entry != "" {
		facts[machine.FactEpicEntry] = entry
	}
	return w.item("epic", status, facts)
}

func (w *world) decision(status string) string {
	w.t.Helper()
	return w.item("decision", status, nil)
}

// setStatus moves a bead with no row, as another writer does.
func (w *world) setStatus(id, from, to string) {
	w.t.Helper()
	if _, err := w.bx.Update(context.Background(), id, beadsx.UpdateSpec{IfStatus: from, Status: to}); err != nil {
		w.t.Fatal(err)
	}
}

// C8: a blocked epic leaves backlog only when every blocker landed. Each
// kind of blocker is tried in each of its landed and open states. The
// epics enter on architecture, which the brainstorm cap never holds.
func TestC8DependencyAdvanceReleasesAnEpicOnlyWhenEveryBlockerLanded(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		blocker func(w *world, p *pulls) string
		landed  bool
	}{
		{"done ticket", func(w *world, _ *pulls) string { return w.ticket(machine.Done) }, true},
		{"canceled ticket with no open pull", func(w *world, _ *pulls) string { return w.ticket(machine.Canceled) }, true},
		{"done epic", func(w *world, _ *pulls) string { return w.epic(machine.Done, "") }, true},
		{"done decision", func(w *world, _ *pulls) string { return w.decision(machine.Done) }, true},
		{"canceled decision", func(w *world, _ *pulls) string { return w.decision(machine.Canceled) }, true},
		{"todo ticket", func(w *world, _ *pulls) string { return w.ticket(machine.Todo) }, false},
		{"ticket in review", func(w *world, _ *pulls) string { return w.ticket(machine.InReview) }, false},
		{"canceled ticket with an open pull", func(w *world, p *pulls) string {
			id := w.ticket(machine.Canceled)
			p.heads = append(p.heads, admission.PullHead{Number: 9, Branch: "loop/" + id})
			return id
		}, false},
		{"epic in execution", func(w *world, _ *pulls) string { return w.epic(machine.Execution, "") }, false},
		{"epic in backlog", func(w *world, _ *pulls) string { return w.epic(machine.Backlog, machine.Architecture) }, false},
		{"pending decision", func(w *world, _ *pulls) string { return w.decision(machine.DecisionPending) }, false},
		{"decision that needs a human", func(w *world, _ *pulls) string { return w.decision(machine.NeedsHuman) }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			p := &pulls{heads: []admission.PullHead{{Number: 1, Branch: "loop/tk-other"}}}
			w.readPulls(p)
			e := w.epic(machine.Backlog, machine.Architecture)
			w.dependsOn(e, w.ticket(machine.Done), beadsx.DepBlocks)
			w.dependsOn(e, c.blocker(w, p), beadsx.DepBlocks)
			if err := w.s.advanceDependencies(ctx, prj); err != nil {
				t.Fatal(err)
			}
			want := machine.Backlog
			if c.landed {
				want = machine.Architecture
			}
			if got := w.status(e); got != want {
				t.Fatalf("epic blocked by a %s = %s, want %s", c.name, got, want)
			}
		})
	}
	t.Run("no blocking edge", func(t *testing.T) {
		w := newWorld(t)
		e := w.epic(machine.Backlog, machine.Architecture)
		w.dependsOn(e, w.ticket(machine.Done), beadsx.DepRelated)
		if err := w.s.advanceDependencies(ctx, prj); err != nil {
			t.Fatal(err)
		}
		if got := w.status(e); got != machine.Backlog {
			t.Fatalf("epic with no blocks edge = %s, want backlog", got)
		}
	})
}

// C8, the coordinator's parked route: a done epic and a moot decision
// release the epic. A pending decision holds its neighbour.
func TestC8DependencyAdvanceReleasesAnEpicOnADoneEpicAndAMootDecision(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.readPulls(&pulls{})
	moved := w.epic(machine.Backlog, machine.PRDNeeded)
	w.dependsOn(moved, w.epic(machine.Done, ""), beadsx.DepBlocks)
	w.dependsOn(moved, w.decision(machine.Canceled), beadsx.DepBlocks)
	held := w.epic(machine.Backlog, machine.PRDNeeded)
	w.dependsOn(held, w.decision(machine.DecisionPending), beadsx.DepBlocks)
	if err := w.s.advanceDependencies(ctx, prj); err != nil {
		t.Fatal(err)
	}
	if got := w.status(moved); got != machine.PRDNeeded {
		t.Fatalf("epic after a done epic and a moot decision = %s, want prd_needed", got)
	}
	if got := w.status(held); got != machine.Backlog {
		t.Fatalf("epic after a pending decision = %s, want backlog", got)
	}
}

// C9: a full brainstorm cap holds a ready PRD epic in backlog, and the tick
// reports no error. The first tick after a slot frees moves it. An
// architecture epic moves whatever the count.
func TestC9TheBrainstormCapHoldsAReadyPRDEpic(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.writeHost(map[string]int{"brainstorm": 1}, nil)
	slot := w.epic(machine.PRDReview, "")
	prd := w.epic(machine.Backlog, machine.PRDNeeded)
	w.dependsOn(prd, w.ticket(machine.Done), beadsx.DepBlocks)
	arch := w.epic(machine.Backlog, machine.Architecture)
	w.dependsOn(arch, w.ticket(machine.Done), beadsx.DepBlocks)

	if err := w.s.advanceDependencies(ctx, prj); err != nil {
		t.Fatalf("a tick at a full cap reported %v", err)
	}
	if got := w.status(prd); got != machine.Backlog {
		t.Fatalf("PRD epic at a full cap = %s, want backlog", got)
	}
	if got := w.status(arch); got != machine.Architecture {
		t.Fatalf("architecture epic at a full cap = %s, want architecture", got)
	}

	w.setStatus(slot, machine.PRDReview, machine.PRDSignoff)
	if err := w.s.advanceDependencies(ctx, prj); err != nil {
		t.Fatal(err)
	}
	if got := w.status(prd); got != machine.PRDNeeded {
		t.Fatalf("PRD epic after a slot freed = %s, want prd_needed", got)
	}
}
