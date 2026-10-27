package machine

import "testing"

// review rounds names the head each verdict was submitted at, a reject's
// too, so a later round reads the last reviewed head (F-49).
func TestReviewRoundsNamesTheHeadOfARejectRound(t *testing.T) {
	w := newWorld(t)
	id, rev := w.inReview()
	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "head": shaA, "body": "Fix the test."}))

	res := w.call("review rounds", w.runOn(id, StageCodeReviewer, "rev-2"), []string{id}, nil)
	mustOK(t, res)
	rounds, _ := res.Data.([]Round)
	if len(rounds) != 1 {
		t.Fatalf("rounds = %+v", res.Data)
	}
	if r := rounds[0]; r.Verdict != "reject" || r.Head != shaA {
		t.Fatalf("round 1 = %+v, want verdict reject at head %s", r, shaA)
	}
}

// C-bug-1: a reject keeps reviewed_head clear, so the merge guards read no
// pass, and a reject with no --head lists the head "".
func TestReviewRejectHeadLeavesReviewedHeadClear(t *testing.T) {
	w := newWorld(t)
	id, rev := w.inReview()
	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "head": shaA, "body": "Fix the test."}))
	if got := w.show(id).Fact(FactReviewedHead); got != "" {
		t.Fatalf("reviewed_head after a reject = %q", got)
	}

	plain, rev := w.inReview()
	mustOK(t, w.call("review submit", rev, []string{plain}, map[string]string{"verdict": "reject", "body": "Fix the test."}))
	res := w.call("review rounds", w.runOn(plain, StageCodeReviewer, "rev-2"), []string{plain}, nil)
	mustOK(t, res)
	if rounds, _ := res.Data.([]Round); len(rounds) != 1 || rounds[0].Head != "" {
		t.Fatalf("a reject with no head: rounds = %+v", res.Data)
	}
}

// C-bug-1: a document reject, of a PRD or of a spec, lists its head too.
func TestReviewRoundsNamesTheHeadOfADocumentReject(t *testing.T) {
	w := newWorld(t)
	prd := w.create(KindEpic, "prd_submit", nil)
	spec := w.create(KindEpic, "prd_submit", nil)
	w.force(spec, SpecReview, nil)
	for _, ep := range []string{prd, spec} {
		w.claim(ep, StageDocReviewer, "doc-rev")
		mustOK(t, w.call("review submit", w.runOn(ep, StageDocReviewer, "doc-rev"), []string{ep}, map[string]string{"verdict": "reject", "head": shaA, "body": "Name the user."}))
		res := w.call("review rounds", w.runOn(ep, StageDocReviewer, "doc-rev"), []string{ep}, nil)
		mustOK(t, res)
		if rounds, _ := res.Data.([]Round); len(rounds) != 1 || rounds[0].Verdict != "reject" || rounds[0].Head != shaA {
			t.Fatalf("%s: rounds = %+v, want one reject at head %s", ep, res.Data, shaA)
		}
	}
}
