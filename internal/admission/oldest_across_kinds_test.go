package admission

import (
	"context"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
)

// A stage that claims from several states of equal rank takes the item that
// has waited longest across them all, not every item of the state its table
// row lists first. The doc-reviewer claims PRD reviews and spec reviews. A
// spec review that waits longer than a PRD review goes first, so a run of
// new PRDs cannot starve the specs (parity 2.15).
func TestPeekTakesTheOldestDocReviewAcrossPRDAndSpec(t *testing.T) {
	w := newWorld(t)
	spec := w.put(machine.KindEpic, machine.SpecReview, "spec")
	time.Sleep(5 * time.Millisecond) // the order dates in milliseconds
	prd := w.put(machine.KindEpic, machine.PRDReview, "prd")
	c, r := w.a.Peek(context.Background(), prj, machine.StageDocReviewer)
	if r != nil || c == nil || c.Item != spec || c.Status != machine.SpecReview {
		t.Fatalf("peek = %+v %+v, want the spec review %s, which waited longer than the PRD review %s", c, r, spec, prd)
	}
}

// The amender claims PRD and spec amendments by the same rule.
func TestPeekTakesTheOldestAmendmentAcrossPRDAndSpec(t *testing.T) {
	w := newWorld(t)
	spec := w.put(machine.KindEpic, machine.SpecAmending, "spec")
	time.Sleep(5 * time.Millisecond)
	prd := w.put(machine.KindEpic, machine.PRDAmending, "prd")
	c, r := w.a.Peek(context.Background(), prj, machine.StageAmender)
	if r != nil || c == nil || c.Item != spec || c.Status != machine.SpecAmending {
		t.Fatalf("peek = %+v %+v, want the spec amendment %s, which waited longer than the PRD amendment %s", c, r, spec, prd)
	}
}

// The oldest-first order does not undo repair-first (F-19): a todo ticket
// that waited longer still comes after a ticket in repair.
func TestPeekKeepsRepairFirstOverAnOlderTodo(t *testing.T) {
	w := newWorld(t)
	todo := w.ticket(machine.Todo)
	time.Sleep(5 * time.Millisecond)
	repair := w.ticket(machine.Repair)
	c, r := w.a.Peek(context.Background(), prj, machine.StageImplementer)
	if r != nil || c == nil || c.Item != repair || c.Status != machine.Repair {
		t.Fatalf("peek = %+v %+v, want the repair ticket %s before the older todo ticket %s", c, r, repair, todo)
	}
}
