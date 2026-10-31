package admission

import (
	"context"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/machine"
)

// A stage that claims from several states of equal rank takes the item that
// has waited longest across them all, not every item of the state its table
// row lists first. The doc-reviewer claims PRD reviews and spec reviews.
// Whichever review waits longer goes first, so neither kind can starve the
// other (parity 2.15). Each case alone passes a queue that always puts one
// kind first; the pair does not.
func TestPeekTakesTheOldestDocReviewAcrossPRDAndSpec(t *testing.T) {
	t.Run("spec waited longer", func(t *testing.T) {
		w := newWorld(t)
		spec := w.put(machine.KindEpic, machine.SpecReview, "spec")
		time.Sleep(5 * time.Millisecond) // the order dates in milliseconds
		prd := w.put(machine.KindEpic, machine.PRDReview, "prd")
		c, r := w.a.Peek(context.Background(), prj, machine.StageDocReviewer)
		if r != nil || c == nil || c.Item != spec || c.Status != machine.SpecReview {
			t.Fatalf("peek = %+v %+v, want the spec review %s, which waited longer than the PRD review %s", c, r, spec, prd)
		}
	})
	// The spec item is made first, so it has the lower bead id, and it
	// enters spec_review last. Only the time of entry picks the PRD review.
	t.Run("PRD waited longer", func(t *testing.T) {
		w := newWorld(t)
		spec := w.put(machine.KindEpic, machine.SpecNeeded, "spec")
		time.Sleep(5 * time.Millisecond)
		prd := w.put(machine.KindEpic, machine.PRDReview, "prd")
		time.Sleep(5 * time.Millisecond)
		w.move(spec, machine.SpecNeeded, machine.SpecReview)
		c, r := w.a.Peek(context.Background(), prj, machine.StageDocReviewer)
		if r != nil || c == nil || c.Item != prd || c.Status != machine.PRDReview {
			t.Fatalf("peek = %+v %+v, want the PRD review %s, which waited longer than the spec review %s", c, r, prd, spec)
		}
	})
}

// The amender claims PRD and spec amendments by the same rule.
func TestPeekTakesTheOldestAmendmentAcrossPRDAndSpec(t *testing.T) {
	t.Run("spec waited longer", func(t *testing.T) {
		w := newWorld(t)
		spec := w.put(machine.KindEpic, machine.SpecAmending, "spec")
		time.Sleep(5 * time.Millisecond)
		prd := w.put(machine.KindEpic, machine.PRDAmending, "prd")
		c, r := w.a.Peek(context.Background(), prj, machine.StageReviser)
		if r != nil || c == nil || c.Item != spec || c.Status != machine.SpecAmending {
			t.Fatalf("peek = %+v %+v, want the spec amendment %s, which waited longer than the PRD amendment %s", c, r, spec, prd)
		}
	})
	t.Run("PRD waited longer", func(t *testing.T) {
		w := newWorld(t)
		spec := w.put(machine.KindEpic, machine.SpecReview, "spec")
		time.Sleep(5 * time.Millisecond)
		prd := w.put(machine.KindEpic, machine.PRDAmending, "prd")
		time.Sleep(5 * time.Millisecond)
		w.move(spec, machine.SpecReview, machine.SpecAmending)
		c, r := w.a.Peek(context.Background(), prj, machine.StageReviser)
		if r != nil || c == nil || c.Item != prd || c.Status != machine.PRDAmending {
			t.Fatalf("peek = %+v %+v, want the PRD amendment %s, which waited longer than the spec amendment %s", c, r, prd, spec)
		}
	})
}

// move writes the bead of item from status from to status to, with no
// transition, so the item enters to at the time of this bead write.
func (w *world) move(item, from, to string) {
	w.t.Helper()
	if _, err := w.bx.Update(context.Background(), item, beadsx.UpdateSpec{IfStatus: from, Status: to}); err != nil {
		w.t.Fatal(err)
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
