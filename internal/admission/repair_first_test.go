package admission

import (
	"context"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// The implementer's work query takes a ticket in repair before any ticket
// in todo, so work in flight finishes before new work starts (F-19).
func TestPeekTakesRepairBeforeTodo(t *testing.T) {
	w := newWorld(t)
	repair := w.ticket(machine.Repair)
	todo := w.ticket(machine.Todo)
	c, r := w.a.Peek(context.Background(), prj, machine.StageImplementer)
	if r != nil || c == nil || c.Item != repair || c.Status != machine.Repair {
		t.Fatalf("peek = %+v %+v, want the repair ticket %s before the todo ticket %s", c, r, repair, todo)
	}
}

// A repair ticket that admission would refuse does not stop the work query:
// past a held, a blocked, a leased one and one at its retry limit, it takes
// the next admissible repair ticket, and with none left, the todo ticket.
func TestPeekPassesAnInadmissibleRepair(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	limited := w.ticket(machine.Repair)
	for range 2 {
		w.settle(w.mustAdmit(machine.StageImplementer, limited), Settlement{Finish: "failed"})
	}
	blocked := w.ticket(machine.Repair)
	w.deps.open[blocked] = []string{"tk-x"}
	leased := w.ticket(machine.Repair)
	w.mustAdmit(machine.StageImplementer, leased)
	held := w.ticket(machine.Repair)
	if err := w.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO holds (project, item, holder, kind, created_at) VALUES (?, ?, 'room-1', 'room', 0)`, prj, held)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	todo := w.ticket(machine.Todo)
	free := w.ticket(machine.Repair)
	c, r := w.a.Peek(ctx, prj, machine.StageImplementer)
	if r != nil || c == nil || c.Item != free || c.Status != machine.Repair {
		t.Fatalf("peek = %+v %+v, want the admissible repair ticket %s before the todo ticket %s", c, r, free, todo)
	}
	w.mustAdmit(machine.StageImplementer, free)
	c, r = w.a.Peek(ctx, prj, machine.StageImplementer)
	if r != nil || c == nil || c.Item != todo || c.Status != machine.Todo {
		t.Fatalf("peek = %+v %+v, want the todo ticket %s past the repair tickets %s %s %s %s",
			c, r, todo, limited, blocked, leased, held)
	}
}

// Every stage lists its claim states in machine table order. The
// implementer's rank, not the order of its states, puts repair first.
func TestClaimStatesKeepTableOrder(t *testing.T) {
	tableOrder := map[string][]stateOf{}
	for _, tb := range machine.Tables() {
		for _, r := range tb.Rows {
			if r.Verb != "claim" {
				continue
			}
			for _, f := range r.From {
				tableOrder[r.Stage] = append(tableOrder[r.Stage], stateOf{tb.Kind, f})
			}
		}
	}
	for stage, want := range tableOrder {
		if got := claimStates(stage); !slices.Equal(got, want) {
			t.Errorf("claimStates(%s) = %v, want %v", stage, got, want)
		}
	}
}
