package machine

import (
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// openedForSimplify walks a new ticket through the implementer's pr_open
// at head, and checks that it waits in in_simplify.
func (w *world) openedForSimplify(head string) string {
	w.t.Helper()
	id := w.create(KindTicket, "create", nil)
	w.claim(id, StageImplementer, "r-impl")
	w.must(id, "pr_open", map[string]string{"head": head})
	if got := w.show(id).Status; got != "in_simplify" {
		w.t.Fatalf("after pr_open: %s, want in_simplify", got)
	}
	return id
}

// simplifiedAt claims a ticket in in_simplify for a simplify run, and
// hands it to review.
func (w *world) simplifiedAt(id string) {
	w.t.Helper()
	w.claim(id, "simplify", "r-simp")
	w.must(id, "simplified", nil)
}

// C3: a new ticket enters in_simplify at the implementer's pr_open, and
// the code-reviewer cannot take it there. A simplify run can.
func TestC3NewTicketEntersInSimplify(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	w.claim(id, StageImplementer, "r-impl")
	w.must(id, "pr_open", map[string]string{"head": shaA})
	it := w.show(id)
	if it.Status != "in_simplify" || it.Fact("simplify_from") != shaA || it.Fact(FactHead) != shaA ||
		it.Fact("simplify_due") != "" || it.Owner != "" {
		t.Fatalf("after pr_open: %s owner %q facts %v", it.Status, it.Owner, it.Facts)
	}
	_, res := w.m.Claim(t.Context(), ClaimRequest{Project: prj, Item: id, Stage: StageCodeReviewer, RunID: "r-rev"})
	wantCode(t, res, cli.CodeTransitionNotAllowed)
	if got := w.claim(id, "simplify", "r-simp"); got <= it.Epoch {
		t.Fatalf("the simplify claim kept epoch %d", got)
	}
	w.must(id, "simplified", nil)
	it = w.show(id)
	if it.Status != InReview || it.Owner != "" || it.Fact(FactHead) != shaA {
		t.Fatalf("after simplified: %s owner %q facts %v", it.Status, it.Owner, it.Facts)
	}
}

// C4: the ticket that a reproducer shapes from a bug enters in_simplify at
// its first pr_open.
func TestC4ShapedTicketEntersInSimplify(t *testing.T) {
	w := newWorld(t)
	bug := w.create(KindBug, "file", nil)
	run := w.claimAs(bug, StageReproducer, "repro")
	res := w.call("work shape", run, []string{bug}, map[string]string{"title": "Fix the crash", "body": shapedBug, "priority": "2"})
	mustOK(t, res)
	ticket := res.Data.(ShapeResult).Ticket
	if got := w.show(ticket).Fact("simplify_due"); got != "true" {
		t.Fatalf("the shaped ticket's simplify_due = %q, want true", got)
	}
	w.claim(ticket, StageImplementer, "r-impl")
	w.must(ticket, "pr_open", map[string]string{"head": shaA})
	if got := w.show(ticket).Status; got != "in_simplify" {
		t.Fatalf("after pr_open: %s, want in_simplify", got)
	}
}

// C5: only a review reject starts a second simplify round. A repair, a
// head change and a dequeue conflict return to in_review.
func TestC5OnlyARejectStartsASecondSimplifyRound(t *testing.T) {
	t.Run("review reject", func(t *testing.T) {
		w := newWorld(t)
		id := w.openedForSimplify(shaA)
		w.simplifiedAt(id)
		w.claim(id, StageCodeReviewer, "r-rev")
		w.must(id, "review_reject", map[string]string{"head": shaA, "body": "split it"})
		it := w.show(id)
		if it.Status != Todo || it.Fact("simplify_due") != "true" || it.Fact("simplify_from") != "" {
			t.Fatalf("after review_reject: %s %v", it.Status, it.Facts)
		}
		w.claim(id, StageImplementer, "r-impl2")
		w.must(id, "pr_open", map[string]string{"head": shaB})
		if it := w.show(id); it.Status != "in_simplify" || it.Fact("simplify_from") != shaB {
			t.Fatalf("after the rework's pr_open: %s %v", it.Status, it.Facts)
		}
	})
	t.Run("repair", func(t *testing.T) {
		w := newWorld(t)
		id := w.openedForSimplify(shaA)
		w.simplifiedAt(id)
		w.must(id, VerbCIRed, nil)
		w.claim(id, StageImplementer, "r-fix")
		w.must(id, "fix_pushed", map[string]string{"head": shaB})
		if it := w.show(id); it.Status != InReview || it.Fact("simplify_from") != "" {
			t.Fatalf("after fix_pushed: %s %v", it.Status, it.Facts)
		}
	})
	t.Run("head changed", func(t *testing.T) {
		w := newWorld(t)
		id := w.openedForSimplify(shaA)
		w.simplifiedAt(id)
		w.claim(id, StageCodeReviewer, "r-rev")
		w.must(id, "review_pass", map[string]string{"head": shaA})
		w.must(id, "head_changed", map[string]string{"head": shaB})
		if it := w.show(id); it.Status != InReview || it.Fact("simplify_from") != "" {
			t.Fatalf("after head_changed: %s %v", it.Status, it.Facts)
		}
	})
	t.Run("dequeue conflict", func(t *testing.T) {
		w := newWorld(t)
		id := w.openedForSimplify(shaA)
		w.simplifiedAt(id)
		w.claim(id, StageCodeReviewer, "r-rev")
		w.must(id, "review_pass", map[string]string{"head": shaA})
		w.must(id, VerbEnqueue, map[string]string{"head": shaA})
		w.must(id, "dequeue_conflict", nil)
		if it := w.show(id); it.Status != Todo || it.Fact("simplify_due") != "" || it.Fact("simplify_from") != "" {
			t.Fatalf("after dequeue_conflict: %s %v", it.Status, it.Facts)
		}
		w.claim(id, StageImplementer, "r-merge")
		w.must(id, "pr_open", map[string]string{"head": shaB})
		if got := w.show(id).Status; got != InReview {
			t.Fatalf("after the merge's pr_open: %s, want in_review", got)
		}
	})
}

// C8: a simplify run cannot escalate its ticket. The ticket stays in
// in_simplify, and the run keeps its lease.
func TestC8ASimplifyRunCannotEscalate(t *testing.T) {
	w := newWorld(t)
	id := w.openedForSimplify(shaA)
	epoch := w.claim(id, "simplify", "r-simp")
	wantCode(t, w.runWrite(id, "r-simp", epoch, VerbEscalate, nil), cli.CodeTransitionNotAllowed)
	if it := w.show(id); it.Status != "in_simplify" || it.Owner != "r-simp" {
		t.Fatalf("after the refused escalate: %s owner %q", it.Status, it.Owner)
	}
}

// Every end of a simplify run that does not hand off passes the ticket to
// in_review: a release, a reclaim, and the server's simplify_off. The
// server's pass waits while a run holds the lease.
func TestEveryOtherEndOfInSimplifyLandsOnInReview(t *testing.T) {
	for _, verb := range []string{"release", "reclaim"} {
		w := newWorld(t)
		id := w.openedForSimplify(shaA)
		w.claim(id, "simplify", "r-simp")
		w.must(id, verb, nil)
		if it := w.show(id); it.Status != InReview || it.Owner != "" || it.Fact(FactHead) != shaA {
			t.Fatalf("after %s: %s owner %q facts %v", verb, it.Status, it.Owner, it.Facts)
		}
	}
	w := newWorld(t)
	held := w.openedForSimplify(shaA)
	w.claim(held, "simplify", "r-simp")
	wantCode(t, w.apply(held, "simplify_off", nil), cli.CodeLeaseHeld)
	free := w.openedForSimplify(shaB)
	w.must(free, "simplify_off", nil)
	if it := w.show(free); it.Status != InReview || it.Fact(FactHead) != shaB {
		t.Fatalf("after simplify_off: %s %v", it.Status, it.Facts)
	}
}
