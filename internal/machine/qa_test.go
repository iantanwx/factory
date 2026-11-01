package machine

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// qaOn makes review submit read the QA stage of the world's project as on
// or off, and records each project it was asked about.
func (w *world) qaOn(on bool) *[]string {
	asked := &[]string{}
	w.m.SetQAEnabled(func(_ context.Context, project string) bool {
		*asked = append(*asked, project)
		return on
	})
	return asked
}

// inQA walks a new ticket through a code review pass at shaA with the QA
// stage on, the way the loop does.
func (w *world) inQA() string {
	w.t.Helper()
	w.qaOn(true)
	id, rev := w.inReview()
	mustOK(w.t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "pass", "head": shaA}))
	if got := w.show(id).Status; got != InQA {
		w.t.Fatalf("the reviewed ticket is in %s, want %s", got, InQA)
	}
	return id
}

// C5: with the stage on, review submit's pass moves the ticket from
// in_review to in_qa and sets reviewed_head. With the stage off, or with
// no stage setting, it lands on ready_to_merge, as before. The setting is
// read for the ticket's own project.
func TestC5AReviewPassLandsOnInQAWithTheStageOn(t *testing.T) {
	cases := []struct {
		name string
		set  func(w *world) *[]string
		want string
	}{
		{"stage on", func(w *world) *[]string { return w.qaOn(true) }, InQA},
		{"stage off", func(w *world) *[]string { return w.qaOn(false) }, ReadyToMerge},
		{"no setting", func(*world) *[]string { return nil }, ReadyToMerge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			asked := c.set(w)
			id, rev := w.inReview()
			mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "pass", "head": shaA}))
			it := w.show(id)
			if it.Status != c.want || it.Fact(FactReviewedHead) != shaA || it.Owner != "" {
				t.Fatalf("after the pass: %s reviewed_head %q owner %q; want %s at %s with no owner",
					it.Status, it.Fact(FactReviewedHead), it.Owner, c.want, shaA)
			}
			if asked != nil && (len(*asked) != 1 || (*asked)[0] != prj) {
				t.Fatalf("the stage setting was read for %v; want [%s]", *asked, prj)
			}
		})
	}
}

// C5: the machine's own rows pick the state by the qa parameter.
func TestC5TheReviewPassRowReadsTheQAParameter(t *testing.T) {
	w := newWorld(t)
	on := w.ticketInReview(shaA)
	w.must(on, "review_pass", map[string]string{"head": shaA, "qa": "on"})
	off := w.ticketInReview(shaA)
	w.must(off, "review_pass", map[string]string{"head": shaA, "qa": "off"})
	if a, b := w.show(on).Status, w.show(off).Status; a != InQA || b != ReadyToMerge {
		t.Fatalf("qa=on landed on %s and qa=off on %s; want %s and %s", a, b, InQA, ReadyToMerge)
	}
}

// C6: only the QA stage claims from in_qa, and the QA stage claims from
// nowhere else. A refused claim moves no lease.
func TestC6OnlyTheQAStageClaimsFromInQA(t *testing.T) {
	w := newWorld(t)
	for _, state := range []string{InReview, Todo, ReadyToMerge} {
		id := w.create(KindTicket, "create", nil)
		w.force(id, state, map[string]string{FactReviewedHead: shaA})
		wantCode(t, w.tryClaim(id, StageQA, "run-qa", ""), cli.CodeTransitionNotAllowed)
		if it := w.show(id); it.Status != state || it.Owner != "" {
			t.Fatalf("a QA claim from %s wrote: %s owner %q", state, it.Status, it.Owner)
		}
	}
	id := w.inQA()
	for _, stage := range []string{StageCodeReviewer, StageImplementer} {
		wantCode(t, w.tryClaim(id, stage, "run-"+stage, ""), cli.CodeTransitionNotAllowed)
	}
	if it := w.show(id); it.Owner != "" {
		t.Fatalf("a refused claim took the lease: owner %q", it.Owner)
	}
	before := w.show(id).Epoch
	mustOK(t, w.tryClaim(id, StageQA, "run-qa", InQA))
	it := w.show(id)
	if it.Status != InQA || it.Owner != "run-qa" || it.Fact(FactStage) != StageQA || it.Epoch != before+1 {
		t.Fatalf("after the QA claim: %s owner %q stage %q epoch %d; want in_qa, run-qa, qa, %d",
			it.Status, it.Owner, it.Fact(FactStage), it.Epoch, before+1)
	}
}

// C7: the merge interface never takes a ticket in in_qa, even at the head
// the review passed.
func TestC7NoTicketInQAEntersTheQueue(t *testing.T) {
	w := newWorld(t)
	id := w.inQA()
	r := wantCode(t, w.apply(id, VerbEnqueue, map[string]string{"head": shaA}), cli.CodeTransitionNotAllowed)
	if r.CurrentStatus != InQA {
		t.Fatalf("refusal: %+v", *r)
	}
	if got := w.show(id).Status; got != InQA {
		t.Fatalf("the refused enqueue moved the ticket to %s", got)
	}
}

// C8: the ticket that a bug's shape creates takes the same path, and its
// review pass with the stage on lands on in_qa.
func TestC8ABugsShapedTicketReachesInQA(t *testing.T) {
	w := newWorld(t)
	w.qaOn(true)
	bug, repro := w.shapeRun("repro")
	res := w.call("work shape", repro, []string{bug}, map[string]string{"title": "Fix it", "body": shapedBug, "priority": "2"})
	mustOK(t, res)
	ticket := res.Data.(ShapeResult).Ticket
	w.claim(ticket, StageImplementer, "impl")
	w.must(ticket, "pr_open", map[string]string{"head": shaA})
	rev := w.claimAs(ticket, StageCodeReviewer, "rev")
	mustOK(t, w.call("review submit", rev, []string{ticket}, map[string]string{"verdict": "pass", "head": shaA, "body": "C-bug-1 pass."}))
	if it := w.show(ticket); it.Status != InQA || it.Fact(FactBug) != bug {
		t.Fatalf("the shaped ticket after its review pass: %s bug %q; want %s from %s", it.Status, it.Fact(FactBug), InQA, bug)
	}
}

// C9: a QA pass at the reviewed head lands on ready_to_merge, sets qa_head
// and review, clears qa_carried_from and drops the lease. The ticket went
// from in_review through in_qa with one QA claim. A pass at any other head
// refuses with head_not_reviewed and writes nothing.
func TestC9AQAPassLandsOnReadyToMergeAtTheReviewedHead(t *testing.T) {
	w := newWorld(t)
	id := w.inQA()
	w.force(id, InQA, map[string]string{FactQACarriedFrom: shaB})
	epoch := w.claim(id, StageQA, "run-qa")

	r := wantCode(t, w.runWrite(id, "run-qa", epoch, VerbQAPass, map[string]string{"head": shaB, "body": "report"}), cli.CodeTransitionNotAllowed)
	if r.Rule != "head_not_reviewed" {
		t.Fatalf("a pass at another head: %+v", *r)
	}
	if it := w.show(id); it.Status != InQA || it.Fact(FactQAHead) != "" || it.Owner != "run-qa" {
		t.Fatalf("the refused pass wrote: %s qa_head %q owner %q", it.Status, it.Fact(FactQAHead), it.Owner)
	}

	mustOK(t, w.runWrite(id, "run-qa", epoch, VerbQAPass, map[string]string{"head": shaA, "body": "QA passed: web drove."}))
	it := w.show(id)
	if it.Status != ReadyToMerge || it.Owner != "" {
		t.Fatalf("after the QA pass: %s owner %q; want ready_to_merge, no owner", it.Status, it.Owner)
	}
	if it.Fact(FactQAHead) != shaA || it.Fact(FactReviewedHead) != shaA || it.Fact(FactReview) != "QA passed: web drove." ||
		it.Fact(FactQACarriedFrom) != "" {
		t.Fatalf("after the QA pass: facts %v", it.Facts)
	}
	claims := 0
	for _, tr := range w.history(id) {
		if tr.Verb == "claim" && tr.From == InQA {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("the ticket took %d QA claims; want 1", claims)
	}
}

// C10: a QA fail is a reject. It lands on todo with the entry rework,
// counts a reject, raises the model tier at the second reject of either
// kind, clears reviewed_head and qa_head, sets rejected_head, review and
// simplify_due, and clears simplify_from.
func TestC10AQAFailIsAReject(t *testing.T) {
	w := newWorld(t)
	id := w.inQA()
	w.force(id, InQA, map[string]string{FactQAHead: shaB, FactSimplifyFrom: shaB})
	epoch := w.claim(id, StageQA, "run-qa")
	mustOK(t, w.runWrite(id, "run-qa", epoch, VerbQAFail, map[string]string{"head": shaA, "body": "QA failed: the save button leaves the screen."}))
	it := w.show(id)
	if it.Status != Todo || it.Owner != "" || it.Fact(FactEntry) != EntryRework {
		t.Fatalf("after the QA fail: %s owner %q entry %q; want todo, no owner, rework", it.Status, it.Owner, it.Fact(FactEntry))
	}
	want := map[string]string{
		FactRejects: "1", FactModelTier: "", FactReviewedHead: "", FactQAHead: "", FactRejectedHead: shaA,
		FactReview: "QA failed: the save button leaves the screen.", FactSimplifyDue: "true", FactSimplifyFrom: "",
	}
	for f, v := range want {
		if got := it.Fact(f); got != v {
			t.Fatalf("after the QA fail: %s = %q, want %q", f, got, v)
		}
	}

	// The next round's review rejects: the second reject of either kind
	// raises the model tier.
	w.claim(id, StageImplementer, "impl-2")
	w.must(id, "pr_open", map[string]string{"head": shaB})
	rev := w.claimAs(id, StageCodeReviewer, "rev-2")
	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "head": shaB, "body": "C1 fail."}))
	if it := w.show(id); it.Fact(FactRejects) != "2" || it.Fact(FactModelTier) != ModelTierEscalated {
		t.Fatalf("after a QA fail and a reject: rejects %q tier %q", it.Fact(FactRejects), it.Fact(FactModelTier))
	}
}

// C10: review rounds lists a QA fail where the implementer reads a reject,
// with the stage of each round, its head and its report.
func TestC10ReviewRoundsListsTheQARound(t *testing.T) {
	w := newWorld(t)
	id := w.inQA()
	epoch := w.claim(id, StageQA, "run-qa")
	mustOK(t, w.runWrite(id, "run-qa", epoch, VerbQAFail, map[string]string{"head": shaA, "body": "the report"}))
	rounds := w.rounds(id, StageImplementer)
	if len(rounds) != 2 {
		t.Fatalf("rounds = %+v; want a review pass and a QA fail", rounds)
	}
	pass, fail := rounds[0], rounds[1]
	if pass.Verdict != "pass" || pass.Stage != StageCodeReviewer || pass.Head != shaA {
		t.Fatalf("the review round: %+v", pass)
	}
	if fail.Round != 2 || fail.Verdict != "reject" || fail.Stage != StageQA || fail.Head != shaA || fail.Body != "the report" ||
		fail.From != InQA || fail.To != Todo || fail.RunID != "run-qa" {
		t.Fatalf("the QA round: %+v", fail)
	}
}

// A QA run that settles with no report returns the ticket to in_qa for the
// next QA run, and a ticket in in_qa holds an open pull request: an
// escalation resumes it as rework.
func TestAQARunWithNoReportLeavesTheTicketInQA(t *testing.T) {
	w := newWorld(t)
	for _, verb := range []string{"release", "reclaim"} {
		id := w.inQA()
		w.claim(id, StageQA, "run-qa")
		w.must(id, verb, nil)
		if it := w.show(id); it.Status != InQA || it.Owner != "" {
			t.Fatalf("after %s: %s owner %q; want in_qa, no owner", verb, it.Status, it.Owner)
		}
	}
	id := w.inQA()
	w.must(id, VerbEscalate, nil)
	if it := w.show(id); it.Fact(FactResume) != Todo || it.Fact(FactResumeEntry) != EntryRework {
		t.Fatalf("an escalation from in_qa resumes to %q with entry %q; want todo, rework", it.Fact(FactResume), it.Fact(FactResumeEntry))
	}
}

const shaC = "cccccccccccccccccccccccccccccccccccccccc"

// C11: a new head in in_qa returns the ticket to review. It clears
// reviewed_head and qa_head, drops the QA run's lease and bumps the epoch,
// so the run's next write with its old lease refuses. The reviewed head
// itself is no change.
func TestC11ANewHeadInQAReturnsTheTicketToReview(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.inQA()
	w.force(id, InQA, map[string]string{FactQAHead: shaC})
	epoch := w.claim(id, StageQA, "run-qa")

	if res := w.m.HeadChanged(ctx, prj, id, shaA); res.Exit != cli.ExitNothing {
		t.Fatalf("the reviewed head is no change: %+v", res)
	}
	mustOK(t, w.m.HeadChanged(ctx, prj, id, shaB))
	it := w.show(id)
	if it.Status != InReview || it.Owner != "" || it.Epoch != epoch+1 {
		t.Fatalf("after head %s: %s owner %q epoch %d; want in_review, no owner, epoch %d", shaB, it.Status, it.Owner, it.Epoch, epoch+1)
	}
	if it.Fact(FactReviewedHead) != "" || it.Fact(FactQAHead) != "" || it.Fact(FactHead) != shaB {
		t.Fatalf("after head %s: facts %v; want no reviewed_head, no qa_head, head %s", shaB, it.Facts, shaB)
	}
	r := wantCode(t, w.runWrite(id, "run-qa", epoch, VerbQAPass, map[string]string{"head": shaA, "body": "report"}), cli.CodeLeaseNotHeld)
	if r.CurrentStatus != InReview {
		t.Fatalf("the old lease's report: %+v", *r)
	}
}

// queuedAfterQA walks a ticket through a QA pass at shaA into the queue.
func (w *world) queuedAfterQA() string {
	w.t.Helper()
	id := w.inQA()
	epoch := w.claim(id, StageQA, "run-qa")
	mustOK(w.t, w.runWrite(id, "run-qa", epoch, VerbQAPass, map[string]string{"head": shaA, "body": "QA passed."}))
	w.must(id, VerbEnqueue, map[string]string{"head": shaA})
	return id
}

// C12: base_merged carries the QA pass of the reviewed head to the merge
// commit, and names the head it carried the pass from. A queued ticket
// with no QA pass keeps none.
func TestC12BaseMergedCarriesTheQAPass(t *testing.T) {
	w := newWorld(t)
	passed := w.queuedAfterQA()
	unpassed := w.ticketInReview(shaA)
	w.must(unpassed, "review_pass", map[string]string{"head": shaA})
	w.must(unpassed, VerbEnqueue, map[string]string{"head": shaA})
	cases := []struct {
		id   string
		want map[string]string
	}{
		{passed, map[string]string{FactReviewedHead: shaC, FactQAHead: shaC, FactQACarriedFrom: shaA, FactHead: shaC}},
		{unpassed, map[string]string{FactReviewedHead: shaC, FactQAHead: "", FactQACarriedFrom: "", FactHead: shaC}},
	}
	for _, c := range cases {
		w.must(c.id, VerbBaseMerged, map[string]string{"from": shaA, "head": shaC})
		it := w.show(c.id)
		if it.Status != Queued {
			t.Fatalf("after base_merged: %s, want %s", it.Status, Queued)
		}
		for f, v := range c.want {
			if got := it.Fact(f); got != v {
				t.Fatalf("after base_merged on %s: %s = %q, want %q", c.id, f, got, v)
			}
		}
	}
}
