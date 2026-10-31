package machine

// Parity cases from the alpha suites (scripts/sdlc/*.test.ts), in their
// new form. Each test names its source. Cases that test Linear, GitHub
// reads, comments or admission policy are listed in the U5 report with
// the reason they are skipped.

import (
	"context"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

func (w *world) tryClaim(id, stage, run, expected string) cli.Result {
	w.t.Helper()
	_, res := w.m.Claim(context.Background(), ClaimRequest{Project: prj, Item: id, Stage: stage, RunID: run, ExpectedStatus: expected})
	return res
}

// claim.test.ts: two loop runs cannot both claim.
func TestParityTwoRunsCannotBothClaim(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	mustOK(t, w.tryClaim(id, StageImplementer, "run-a", ""))
	wantCode(t, w.tryClaim(id, StageImplementer, "run-b", ""), cli.CodeTransitionNotAllowed)

	// A claim that keeps the state is fenced by the lease owner.
	rv := w.ticketInReview("h1")
	mustOK(t, w.tryClaim(rv, StageCodeReviewer, "run-a", ""))
	ref := wantCode(t, w.tryClaim(rv, StageCodeReviewer, "run-b", ""), cli.CodeLeaseHeld)
	if w.show(rv).Owner != "run-a" || ref.Item != rv {
		t.Fatalf("owner changed: %+v", w.show(rv))
	}
}

// claim.test.ts: a failed assignment write retains its exact local owner.
// New form: a claim whose beads write cannot land keeps its intent, and a
// second run's claim finishes that intent first and finds the lease held.
func TestParityFailedClaimWriteKeepsItsOwner(t *testing.T) {
	w := newWorld(t)
	id := w.ticketInReview("h1")
	w.m.hook = func(p string) bool {
		if p == pointRecorded {
			w.bx.SetUnavailable(true)
		}
		return false
	}
	wantCode(t, w.tryClaim(id, StageCodeReviewer, "run-a", ""), cli.CodeBeadsUnavailable)
	w.m.hook = nil
	wantCode(t, w.tryClaim(id, StageCodeReviewer, "run-b", ""), cli.CodeBeadsUnavailable)
	w.bx.SetUnavailable(false)
	ref := wantCode(t, w.tryClaim(id, StageCodeReviewer, "run-b", ""), cli.CodeLeaseHeld)
	if w.show(id).Owner != "run-a" {
		t.Fatalf("owner %q, want run-a: %+v", w.show(id).Owner, ref)
	}
}

// claim.test.ts: a stale review row cannot claim todo, while its current
// review state can.
func TestParityReviewerClaimsOnlyItsState(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	wantCode(t, w.tryClaim(id, StageCodeReviewer, "run-r", ""), cli.CodeTransitionNotAllowed)
	if l, _ := w.m.Lease(context.Background(), prj, id); l.Owner != "" || l.Epoch != 0 {
		t.Fatalf("a refused claim moved the lease: %+v", l)
	}
	rv := w.ticketInReview("h1")
	mustOK(t, w.tryClaim(rv, StageCodeReviewer, "run-r", ""))
	if it := w.show(rv); it.Fact(FactStage) != StageCodeReviewer || it.Status != InReview {
		t.Fatalf("after the review claim: %+v", it)
	}
}

// claim.test.ts: a same-owner repair lease cannot become a review lease.
// New form: repair is its own state, and only the implementer claims it.
func TestParityRepairLeaseIsNoReviewLease(t *testing.T) {
	w := newWorld(t)
	id := w.ticketInReview("h1")
	w.must(id, "ci_red", nil)
	wantCode(t, w.tryClaim(id, StageCodeReviewer, "run-a", ""), cli.CodeTransitionNotAllowed)
	mustOK(t, w.tryClaim(id, StageImplementer, "run-a", ""))
	if it := w.show(id); it.Fact(FactStage) != StageImplementer || it.Status != Repair {
		t.Fatalf("repair lease: %+v", it)
	}
}

// claim.test.ts: each state is claimed by its explicit stage.
func TestParityEachStateHasItsClaimingStage(t *testing.T) {
	cases := []struct {
		kind  Kind
		state string
		stage string
		to    string
	}{
		{KindTicket, Todo, StageImplementer, InProgress},
		{KindTicket, Repair, StageImplementer, Repair},
		{KindTicket, InReview, StageCodeReviewer, InReview},
		{KindEpic, PRDReview, StageDocReviewer, PRDReview},
		{KindEpic, PRDAmending, StageAmender, PRDAmending},
		{KindEpic, SpecNeeded, StagePlanner, SpecDrafting},
		{KindEpic, SpecReview, StageDocReviewer, SpecReview},
		{KindEpic, SpecAmending, StageAmender, SpecAmending},
		{KindEpic, SpecApproved, StageDecomposer, Decomposing},
		{KindBug, Reported, StageReproducer, Reproducing},
	}
	w := newWorld(t)
	for _, c := range cases {
		tbl, _ := TableFor(c.kind)
		id := w.create(c.kind, firstCreate(tbl), nil)
		// The planner starts only after the operator's sign-off.
		w.force(id, c.state, map[string]string{FactPRDMerged: "m1"})
		mustOK(t, w.tryClaim(id, c.stage, "run-1", c.state))
		it := w.show(id)
		if it.Status != c.to || it.Owner != "run-1" || it.Fact(FactStage) != c.stage || it.Epoch != 1 {
			t.Fatalf("%s %s by %s: %+v", c.kind, c.state, c.stage, it)
		}
	}
}

// claim.test.ts: a review state that changes during claim refuses before
// reservation.
func TestParityStateThatChangedDuringClaimRefuses(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	before := w.count(`SELECT count(*) FROM intents`)
	ref := wantCode(t, w.tryClaim(id, StageCodeReviewer, "run-r", InReview), cli.CodeStatusChanged)
	if ref.CurrentStatus != Todo || w.count(`SELECT count(*) FROM intents`) != before {
		t.Fatalf("refusal %+v, or an intent was recorded", ref)
	}
}

// claim.test.ts: the reproducer claims a raw bug report, and nothing else.
func TestParityReproducerClaimsOnlyARawReport(t *testing.T) {
	w := newWorld(t)
	bug := w.create(KindBug, "file", nil)
	mustOK(t, w.tryClaim(bug, StageReproducer, "run-1", ""))
	tk := w.create(KindTicket, "create", nil)
	wantCode(t, w.tryClaim(tk, StageReproducer, "run-2", ""), cli.CodeTransitionNotAllowed)
	shaped := w.create(KindBug, "file", nil)
	w.must(shaped, "shape", map[string]string{"ticket": tk})
	wantCode(t, w.tryClaim(shaped, StageReproducer, "run-3", ""), cli.CodeTransitionNotAllowed)
}

// done-guard.test.ts: Done has exactly one writer for a ticket, the merge
// the server observes (R9), and no CLI verb writes it.
func TestParityDoneHasOneWriter(t *testing.T) {
	tbl, _ := TableFor(KindTicket)
	var writers []string
	for _, r := range tbl.Rows {
		if r.To == Done {
			writers = append(writers, r.Verb)
			if r.Command != "" {
				t.Fatalf("%s writes done from the CLI verb %s", r.Verb, r.Command)
			}
		}
	}
	if !slices.Equal(writers, []string{"merged"}) {
		t.Fatalf("done writers = %v", writers)
	}
	// A decision's done means a person answered, through decision settle.
	dt, _ := TableFor(KindDecision)
	for _, r := range dt.Rows {
		if r.To == Done && (r.Verb != "answer" || r.Command != "decision settle") {
			t.Fatalf("decision row %s writes done", r.Verb)
		}
	}
	// No create row lands on a terminal state.
	for _, t2 := range Tables() {
		for _, r := range t2.Rows {
			if slices.Contains(r.From, Initial) && t2.Terminal(r.To) {
				t.Fatalf("%s %s creates a closed item", t2.Kind, r.Verb)
			}
		}
	}
}

// handoff.test.ts: every in-flight state returns to a queue a stage
// claims from, and escalate stores a target answer can land on.
func TestParityResumeTargets(t *testing.T) {
	inFlight := map[Kind][]string{
		KindTicket: {InProgress}, KindEpic: {PRDDrafting, SpecDrafting, Decomposing}, KindBug: {Reproducing},
	}
	for _, tbl := range Tables() {
		claimable := map[string]bool{}
		for _, r := range tbl.Rows {
			if r.Verb == "claim" {
				for _, f := range r.From {
					claimable[f] = true
				}
			}
		}
		for _, s := range inFlight[tbl.Kind] {
			for _, r := range tbl.Rows {
				if r.Verb == "release" && slices.Contains(r.From, s) && !claimable[r.To] {
					t.Fatalf("%s %s releases to %s, which no stage claims", tbl.Kind, s, r.To)
				}
			}
		}
		var answer *Row
		for i, r := range tbl.Rows {
			if r.Verb == "answer" && r.To == ToResume {
				answer = &tbl.Rows[i]
			}
		}
		for _, r := range tbl.Rows {
			if r.Verb != "escalate" {
				continue
			}
			for _, s := range r.From {
				c := &change{item: Item{Status: s, Facts: map[string]string{}}, set: map[string]string{}}
				if ref := r.set(c); ref != nil {
					t.Fatal(ref)
				}
				target := c.set[factPrefix+FactResume]
				if answer == nil || !slices.Contains(answer.ResumeTo, target) {
					t.Fatalf("%s escalate from %s stores %q, which answer cannot land on", tbl.Kind, s, target)
				}
				if slices.Contains(inFlight[tbl.Kind], target) || tbl.Terminal(target) {
					t.Fatalf("%s escalate from %s resumes into %s", tbl.Kind, s, target)
				}
			}
		}
		for _, s := range tbl.States {
			if s.Terminal && tbl.Allows("escalate", s.Name) {
				t.Fatalf("%s escalates from terminal %s", tbl.Kind, s.Name)
			}
		}
	}
	// No two queues are claimed into one in-flight state.
	tt, _ := TableFor(KindEpic)
	into := map[string]string{}
	for _, r := range tt.Rows {
		if r.Verb == "claim" && r.To != ToSame {
			if prev, dup := into[r.To]; dup {
				t.Fatalf("%s and %s both claim into %s", prev, r.From[0], r.To)
			}
			into[r.To] = r.From[0]
		}
	}
}

// document-states.test.ts: a document that outran its review lands on
// sign-off and never past it, another row carries it on, and the move
// revokes the review lease.
func TestParityOutranLandsOnSignoff(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.create(KindEpic, "prd_submit", nil)
	epoch := w.claim(ep, StageDocReviewer, "run-rev")
	mustOK(t, w.m.MergeObserved(ctx, MergeEvent{Project: prj, Item: ep, Base: "main", Into: "main"}))
	it := w.show(ep)
	if it.Status != PRDSignoff || it.Owner != "" || it.Epoch != epoch+1 {
		t.Fatalf("after the outran merge: %+v", it)
	}
	wantCode(t, w.runWrite(ep, "run-rev", epoch, "note", map[string]string{"body": "late review"}), cli.CodeLeaseNotHeld)
	mustOK(t, w.m.MergeObserved(ctx, MergeEvent{Project: prj, Item: ep, Base: "main", Into: "main"}))
	if got := w.show(ep).Status; got != SpecNeeded {
		t.Fatalf("the sign-off row carries it on to %s", got)
	}
}

// decision-states.test.ts: every waiting state closes in one move, only
// fire sends a decision to a person, each move makes its own claim, and
// only an answer reads as done.
func TestParityDecisionMoves(t *testing.T) {
	tbl, _ := TableFor(KindDecision)
	for _, s := range []string{DecisionPending, NeedsHuman} {
		if !slices.ContainsFunc(tbl.Rows, func(r Row) bool { return slices.Contains(r.From, s) && tbl.Terminal(r.To) }) {
			t.Fatalf("%s reaches no terminal state in one move", s)
		}
	}
	says := map[string]bool{}
	for _, r := range tbl.Rows {
		if r.To == NeedsHuman && r.Verb != "fire" {
			t.Fatalf("%s sends a decision to a person", r.Verb)
		}
		if says[r.Says] || r.Says == "" {
			t.Fatalf("%s makes no claim of its own: %q", r.Verb, r.Says)
		}
		says[r.Says] = true
		if tbl.Terminal(r.To) && r.Command != "decision settle" && r.Command != "decision moot" {
			t.Fatalf("closing move %s names %q", r.Verb, r.Command)
		}
	}
	w := newWorld(t)
	d := w.create(KindDecision, "file", nil)
	w.must(d, "moot", map[string]string{"note": "the question went away"})
	if h := w.history(d); h[len(h)-1].Says != "It stopped being a question." {
		t.Fatalf("the history does not record the claim: %+v", h)
	}
}

// epic-states.test.ts: an epic closes only from execution. A person's
// epic, a document state, the decomposer's states and a terminal state
// are left alone.
func TestParityEpicClosesOnlyFromExecution(t *testing.T) {
	tbl, _ := TableFor(KindEpic)
	var from []string
	for _, r := range tbl.Rows {
		if r.To == Done {
			from = append(from, r.From...)
		}
	}
	if !slices.Equal(from, []string{Execution}) {
		t.Fatalf("an epic closes from %v", from)
	}
	w := newWorld(t)
	for _, s := range []string{Escalated, PRDReview, SpecSignoff, SpecApproved, Decomposing, Done} {
		ep := w.create(KindEpic, "create_architecture", nil)
		w.force(ep, s, nil)
		wantCode(t, w.apply(ep, "complete", nil), cli.CodeTransitionNotAllowed)
	}
}

// queuedTicket makes a ticket whose PR passed review at head and entered
// the merge queue.
func (w *world) queuedTicket(head string) string {
	w.t.Helper()
	id := w.ticketInReview(head)
	w.must(id, "review_pass", map[string]string{"head": head})
	w.must(id, VerbEnqueue, map[string]string{"head": head})
	return id
}

// premerge-gate.test.ts: the rehearsal refuses a ticket in In Review, and
// a ticket in Ready to Merge passes. land-queue.test.ts: a moved source
// head never authorizes the replacement. New form: only a ready_to_merge
// ticket enters the queue, and only at the head its review passed.
func TestParityOnlyAReviewPassedHeadEntersTheQueue(t *testing.T) {
	w := newWorld(t)
	for _, s := range []string{Todo, InProgress, InReview, Repair, Escalated} {
		id := w.create(KindTicket, "create", nil)
		w.force(id, s, map[string]string{FactReviewedHead: "h1"})
		wantCode(t, w.apply(id, VerbEnqueue, map[string]string{"head": "h1"}), cli.CodeTransitionNotAllowed)
		if got := w.show(id).Status; got != s {
			t.Fatalf("a refused enqueue moved %s to %s", s, got)
		}
	}
	id := w.ticketInReview("h1")
	w.must(id, "review_pass", map[string]string{"head": "h1"})
	ref := wantCode(t, w.apply(id, VerbEnqueue, map[string]string{"head": "h2"}), cli.CodeTransitionNotAllowed)
	if ref.Rule != "head_not_reviewed" || w.show(id).Status != ReadyToMerge {
		t.Fatalf("a moved head entered the queue: %+v, %+v", ref, w.show(id))
	}
	w.must(id, VerbEnqueue, map[string]string{"head": "h1"})
	if got := w.show(id).Status; got != Queued {
		t.Fatalf("the reviewed head did not enter the queue: %s", got)
	}
}

// land-queue.test.ts: policy refusal and dequeue stay explicit, and closed
// is not merged. land-queue-retry.test.ts: a dequeued PR that policy
// refuses gets no retry. New form: each way out of the queue but a merge
// lands on its own state, never done, and a dequeue drops the review
// pass, so the old head cannot enter the queue again.
func TestParityQueuedOutcomesStayExplicit(t *testing.T) {
	w := newWorld(t)
	closed := w.queuedTicket("h1")
	w.must(closed, VerbPRClosed, nil)
	if it := w.show(closed); it.Status != Escalated || it.Fact(FactResume) != Todo || it.Fact(FactResumeEntry) != EntryRework {
		t.Fatalf("a closed PR: %+v", it)
	}
	red := w.queuedTicket("h1")
	w.must(red, "dequeue_red", map[string]string{"reason": "ci_red"})
	if it := w.show(red); it.Status != Repair || it.Fact(FactReviewedHead) != "" {
		t.Fatalf("a red dequeue: %+v", it)
	}
	conflict := w.queuedTicket("h1")
	w.must(conflict, "dequeue_conflict", map[string]string{"reason": "conflict"})
	if it := w.show(conflict); it.Status != Todo || it.Fact(FactEntry) != EntryRework || it.Fact(FactReviewedHead) != "" {
		t.Fatalf("a conflict dequeue: %+v", it)
	}
	// Even put back in ready_to_merge, neither dequeued ticket holds a pass.
	for _, id := range []string{red, conflict} {
		w.force(id, ReadyToMerge, nil)
		wantCode(t, w.apply(id, VerbEnqueue, map[string]string{"head": "h1"}), cli.CodeTransitionNotAllowed)
	}
}
