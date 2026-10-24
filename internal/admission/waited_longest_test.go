package admission

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
)

// Within one claim state, the work query takes the item that has waited
// there longest, not the item whose id sorts first (F-20). The fake numbers
// ids in base 36, so tk-10 sorts before tk-2, tk-3 and tk-4.
//
// tk-3 entered todo first. tk-10 entered todo next. tk-2 and tk-4 were made
// in backlog and entered todo last. So id order picks tk-10, reversed id
// order picks tk-4, and creation order picks tk-2. Only the time an item
// entered its state picks tk-3.
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
	late2 := mk(machine.Backlog, "tk-2")
	oldest := mk(machine.Todo, "tk-3")
	late4 := mk(machine.Backlog, "tk-4")
	for n := 5; n < 36; n++ {
		w.ticket(machine.Backlog)
	}
	newer := mk(machine.Todo, "tk-10")
	for _, id := range []string{late2, late4} {
		res := w.m.Apply(ctx, machine.Request{Project: prj, Item: id, Verb: machine.VerbUnblock,
			Command: "work unblock", Actor: machine.Server("test")})
		if res.Refusal != nil {
			t.Fatalf("unblock %s: %+v", id, res.Refusal)
		}
	}
	c, r := w.a.Peek(ctx, prj, machine.StageImplementer)
	if r != nil || c == nil || c.Item != oldest || c.Status != machine.Todo {
		t.Fatalf("peek = %+v %+v, want %s, which entered todo before %s, %s and %s", c, r, oldest, newer, late2, late4)
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
