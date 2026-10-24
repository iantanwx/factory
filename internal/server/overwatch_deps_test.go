package server

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/machine"
)

// ticketIn creates one ticket in the fake beads, in status.
func (w *humanWorld) ticketIn(status string) string {
	w.t.Helper()
	ctx := context.Background()
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "Fix the thing", Description: "No paths.", Type: "task"})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: status}); err != nil {
		w.t.Fatal(err)
	}
	return is.ID
}

// C14, spec Work 5: the production World.Dependencies read returns an
// item's beads edges of every kind, each with its status. An item with no
// edge returns an empty list.
func TestC14TheDependenciesReadHoldsEveryEdgeKindWithItsStatus(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	ticket, done, open := w.ticketIn(machine.Backlog), w.ticketIn(machine.Done), w.ticketIn(machine.InProgress)
	if err := w.bx.AddDependency(ctx, ticket, done, beadsx.DepBlocks); err != nil {
		t.Fatal(err)
	}
	if err := w.bx.AddDependency(ctx, ticket, open, beadsx.DepRelated); err != nil {
		t.Fatal(err)
	}
	got, err := w.srv.overwatch.world.Dependencies(ctx, "alpha", ticket)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got, func(a, b beadsx.Dependency) int { return cmp.Compare(a.ID, b.ID) })
	want := []beadsx.Dependency{
		{ID: done, Kind: beadsx.DepBlocks, Status: machine.Done},
		{ID: open, Kind: beadsx.DepRelated, Status: machine.InProgress},
	}
	slices.SortFunc(want, func(a, b beadsx.Dependency) int { return cmp.Compare(a.ID, b.ID) })
	if !slices.Equal(got, want) {
		t.Fatalf("Dependencies = %+v, want %+v", got, want)
	}
	if got, err := w.srv.overwatch.world.Dependencies(ctx, "alpha", done); err != nil || len(got) != 0 {
		t.Fatalf("an item with no edge = %+v %v", got, err)
	}
}

// C14 through the server: with the shipped parts, a backlog ticket whose
// only edge is related makes `loop overwatch sweep` report backlog work.
func TestC14TheSweepReportsABacklogTicketWithNoBlocksEdge(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	ticket, other := w.ticketIn(machine.Backlog), w.ticketIn(machine.InProgress)
	if err := w.bx.AddDependency(ctx, ticket, other, beadsx.DepRelated); err != nil {
		t.Fatal(err)
	}
	d := w.sweepData()
	for _, p := range d["parts"].([]any) {
		part := p.(map[string]any)
		if part["name"] != "backlog" {
			continue
		}
		lines := part["lines"].([]any)
		if part["code"] != float64(1) || len(lines) != 1 || !strings.Contains(lines[0].(string), "overwatch:backlog:"+ticket) {
			t.Fatalf("backlog = %v", part)
		}
		return
	}
	t.Fatalf("the sweep holds no backlog part: %v", d)
}
