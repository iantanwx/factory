package admission

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
)

// Within one claim state, the work query takes the item that has waited
// there longest, not the item whose id sorts first (F-20). The implementer
// ranks its tickets by D10, so the code-reviewer's queue shows the order.
// The fake numbers ids in base 36, so tk-10 sorts before tk-2, tk-3 and
// tk-4.
//
// tk-3 entered in_review first. tk-10 entered in_review next. tk-2 and tk-4
// entered in_review last. So id order picks tk-10, reversed id order picks
// tk-4, and creation order picks tk-2. Only the time an item entered its
// state picks tk-3.
func TestPeekTakesTheItemThatWaitedLongestInItsState(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	mk := func(status, want string) string {
		t.Helper()
		id := w.ticket(status)
		if id != want {
			t.Fatalf("made %s, want %s", id, want)
		}
		return id
	}
	mk(machine.Backlog, "tk-1")
	late2 := mk(machine.InReview, "tk-2")
	oldest := mk(machine.InReview, "tk-3")
	late4 := mk(machine.InReview, "tk-4")
	for n := 5; n < 36; n++ {
		w.ticket(machine.Backlog)
	}
	newer := mk(machine.InReview, "tk-10")
	start := time.Now().Add(-time.Hour)
	w.history(oldest, "pr_open", machine.InProgress, machine.InReview, start)
	w.history(newer, "pr_open", machine.InProgress, machine.InReview, start.Add(time.Minute))
	for _, id := range []string{late2, late4} {
		w.history(id, "pr_open", machine.InProgress, machine.InReview, start.Add(2*time.Minute))
	}
	c, r := w.a.Peek(ctx, prj, machine.StageCodeReviewer)
	if r != nil || c == nil || c.Item != oldest || c.Status != machine.InReview {
		t.Fatalf("peek = %+v %+v, want %s, which entered in_review before %s, %s and %s", c, r, oldest, newer, late2, late4)
	}
}

// Items that entered their state in the same millisecond keep a stable
// order: the earlier bead write first, then the lower bead id. An item that
// no transition dates entered its state at its last bead write.
func TestWaitOrderBreaksTiesByBeadWriteThenID(t *testing.T) {
	at := time.UnixMilli(1000)
	items := []machine.Item{
		{ID: "tk-c", Status: machine.Todo, Updated: at.Add(200 * time.Microsecond)},
		{ID: "tk-b", Status: machine.Todo, Updated: at},
		{ID: "tk-a", Status: machine.Todo, Updated: at},
		{ID: "tk-d", Status: machine.Todo, Updated: at.Add(-time.Second)},
		{ID: "tk-e", Status: machine.Todo, Updated: at.Add(time.Second)},
	}
	waitOrder(items, map[string]int64{"tk-d": 1000, "tk-e": 999})
	var got []string
	for _, it := range items {
		got = append(got, it.ID)
	}
	want := []string{"tk-e", "tk-d", "tk-a", "tk-b", "tk-c"}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// An item's place in its state comes from the transition that moved it
// there, not from its last bead write. A note writes the bead and keeps the
// status, so it must not send the item to the back of its state.
//
// tk-2 entered in_review before tk-1, then got a note. Id order picks tk-1,
// and last-write order picks tk-1. Only the time an item entered its state
// picks tk-2.
func TestPeekDatesEntryByTransitionNotByLastBeadWrite(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	newer := w.ticket(machine.InReview)
	oldest := w.ticket(machine.InReview)
	if newer != "tk-1" || oldest != "tk-2" {
		t.Fatalf("made %s and %s, want tk-1 and tk-2", newer, oldest)
	}
	start := time.Now().Add(-time.Hour)
	w.history(oldest, "pr_open", machine.InProgress, machine.InReview, start)
	w.history(newer, "pr_open", machine.InProgress, machine.InReview, start.Add(time.Minute))
	res := w.m.Apply(ctx, machine.Request{Project: prj, Item: oldest, Verb: machine.VerbNote,
		Command: "work note", Params: map[string]string{"body": "x"}, Actor: machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("note: %+v", res.Refusal)
	}
	c, r := w.a.Peek(ctx, prj, machine.StageCodeReviewer)
	if r != nil || c == nil || c.Item != oldest || c.Status != machine.InReview {
		t.Fatalf("peek = %+v %+v, want %s, which entered in_review before %s", c, r, oldest, newer)
	}
}
