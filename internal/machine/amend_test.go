package machine

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// 1.12: a person's comment on a document in review or in sign-off moves
// the epic to the amender, for the PRD and for the spec alike. A
// revision returns the document to review. A reply that changed nothing
// returns the document to the state the comment found it in.
func TestPersonCommentMovesTheDocumentToTheAmender(t *testing.T) {
	cases := []struct {
		from, amending, unchanged, verb, review string
	}{
		{PRDReview, PRDAmending, PRDReview, "prd_amended", PRDReview},
		{PRDSignoff, PRDAmending, PRDSignoff, "prd_amended", PRDReview},
		{SpecReview, SpecAmending, SpecReview, "spec_amended", SpecReview},
		{SpecSignoff, SpecAmending, SpecSignoff, "spec_amended", SpecReview},
	}
	for _, c := range cases {
		t.Run(c.from, func(t *testing.T) {
			w := newWorld(t)
			for _, revised := range []bool{false, true} {
				ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
				w.force(ep, c.from, map[string]string{FactPRDMerged: "m1"})
				w.must(ep, VerbPersonComment, map[string]string{"head": "h1"})
				it := w.show(ep)
				if it.Status != c.amending || it.Fact(FactAmendFrom) != c.from || it.Fact(FactAmendHead) != "h1" {
					t.Fatalf("after the comment: %s %v", it.Status, it.Facts)
				}
				w.claim(ep, StageAmender, "r-amend")
				head, want := "h1", c.unchanged
				if revised {
					head, want = "h2", c.review
				}
				w.must(ep, c.verb, map[string]string{"head": head})
				it = w.show(ep)
				if it.Status != want || it.Owner != "" || it.Fact(FactAmendFrom) != "" || it.Fact(FactAmendHead) != "" {
					t.Fatalf("revised=%v: %s owner %q %v, want %s", revised, it.Status, it.Owner, it.Facts, want)
				}
			}
		})
	}
}

// A person's comment does not count as a doc-reviewer's reject: the
// first reject after it still goes to the amender, not to a person.
func TestPersonCommentIsNoReject(t *testing.T) {
	w := newWorld(t)
	ep := w.create(KindEpic, "prd_submit", nil)
	w.must(ep, VerbPersonComment, map[string]string{"head": "h1"})
	w.must(ep, "prd_amended", map[string]string{"head": "h2"})
	w.must(ep, "review_reject", nil)
	if it := w.show(ep); it.Status != PRDAmending || it.Fact(FactPRDRejects) != "1" {
		t.Fatalf("the first reject after a comment: %s %v", it.Status, it.Facts)
	}
	// The amender's revision after a reject returns to review, whatever
	// head it pushed.
	w.must(ep, "prd_amended", map[string]string{"head": "h2"})
	if got := w.show(ep).Status; got != PRDReview {
		t.Fatalf("the revision after a reject landed on %s", got)
	}
}

// The operator can merge a document while the amender answers a comment.
// The merge is the sign-off, and the epic moves on from it.
func TestDocumentMergedWhileAmendingIsTheSignoff(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.create(KindEpic, "prd_submit", nil)
	w.must(ep, "review_pass", map[string]string{"head": "h1"})
	w.must(ep, VerbPersonComment, map[string]string{"head": "h1"})
	l := w.claim(ep, StageAmender, "r-amend")
	ev := MergeEvent{Project: prj, Item: ep, Base: "main", Into: "main", SHA: "m1"}
	mustOK(t, w.m.MergeObserved(ctx, ev))
	mustOK(t, w.m.MergeObserved(ctx, ev))
	it := w.show(ep)
	if it.Status != SpecNeeded || it.Owner != "" || it.Epoch <= l || it.Fact(FactPRDMerged) != "m1" || it.Fact(FactAmendFrom) != "" {
		t.Fatalf("after the merge while amending: %s owner %q %v", it.Status, it.Owner, it.Facts)
	}

	w.force(ep, SpecSignoff, nil)
	w.must(ep, VerbPersonComment, map[string]string{"head": "h3"})
	mustOK(t, w.m.MergeObserved(ctx, ev))
	mustOK(t, w.m.MergeObserved(ctx, ev))
	if got := w.show(ep).Status; got != SpecApproved {
		t.Fatalf("the spec merged while amending landed on %s", got)
	}
}

// A run that holds the epic keeps it: a person's comment moves no held
// document, so the run's next write finds the state it claimed. The
// comment waits for the next tick after the run ends.
func TestPersonCommentWaitsForTheRunThatHoldsTheEpic(t *testing.T) {
	for _, from := range []string{PRDReview, SpecReview} {
		t.Run(from, func(t *testing.T) {
			w := newWorld(t)
			ep := w.create(KindEpic, "prd_submit", nil)
			w.force(ep, from, map[string]string{FactPRDMerged: "m1"})
			w.claim(ep, StageDocReviewer, "r-rev")
			wantCode(t, w.apply(ep, VerbPersonComment, map[string]string{"head": "h1"}), cli.CodeLeaseHeld)
			if it := w.show(ep); it.Status != from || it.Owner != "r-rev" || it.Fact(FactAmendFrom) != "" {
				t.Fatalf("a held epic: %s owner %q %v", it.Status, it.Owner, it.Facts)
			}
		})
	}
}
