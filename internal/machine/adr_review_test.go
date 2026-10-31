package machine

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// adrInReview makes a PRD epic in architecture, opens the review of ADR
// pull request #12 at head, and claims it for a doc-reviewer. The pull
// request's live head reads live.
func (w *world) adrInReview(head string, live *string) (string, rpc.Principal) {
	w.t.Helper()
	w.m.SetPRHeadReader(func(context.Context, string, string) (string, *cli.Refusal) { return *live, nil })
	ep := w.create(KindEpic, "create_prd", nil)
	w.force(ep, Architecture, map[string]string{FactPRDMerged: "m1"})
	w.must(ep, "adr_opened", map[string]string{"pr": "12", "head": head})
	return ep, w.claimAs(ep, StageDocReviewer, "rev")
}

// C26: adr_opened moves an epic in architecture to adr_review with the
// pull request and its head, and the doc-reviewer claims it. Its verdict
// at that head returns the epic to architecture, and records a round at
// the head.
func TestC26ADRPullRequestGetsOneReviewAtItsHead(t *testing.T) {
	for _, verdict := range []string{"pass", "reject"} {
		t.Run(verdict, func(t *testing.T) {
			w := newWorld(t)
			live := shaA
			ep, rev := w.adrInReview(shaA, &live)
			if it := w.show(ep); it.Status != "adr_review" || it.Fact("adr_pr") != "12" || it.Fact("adr_head") != shaA || it.Owner != "rev" {
				t.Fatalf("after adr_opened and the claim: %s owner %q %v", it.Status, it.Owner, it.Facts)
			}
			mustOK(t, w.call("review submit", rev, []string{ep}, map[string]string{"verdict": verdict, "head": shaA, "body": "The ADR binds two epics."}))
			if it := w.show(ep); it.Status != Architecture || it.Owner != "" {
				t.Fatalf("after the %s: %s owner %q", verdict, it.Status, it.Owner)
			}
			rounds := Rounds(w.history(ep))
			if len(rounds) != 1 || rounds[0].Verdict != verdict || rounds[0].Head != shaA || rounds[0].From != "adr_review" {
				t.Fatalf("rounds after the %s: %+v", verdict, rounds)
			}
		})
	}
}

// C26: a verdict on an ADR names the head it read, so a later tick can
// tell that head reviewed.
func TestC26ADRVerdictNamesItsHead(t *testing.T) {
	w := newWorld(t)
	live := shaA
	ep, rev := w.adrInReview(shaA, &live)
	res := w.call("review submit", rev, []string{ep}, map[string]string{"verdict": "reject", "body": "The ADR binds one epic."})
	if r := wantCode(t, res, cli.CodeUsage); r.Rule != "missing_head" {
		t.Errorf("a verdict with no head: rule %q, want missing_head", r.Rule)
	}
	if it := w.show(ep); it.Status != "adr_review" || it.Owner != "rev" {
		t.Fatalf("a verdict with no head moved the epic: %s owner %q", it.Status, it.Owner)
	}
}

// C27: a verdict at a head the ADR pull request left is refused with rule
// head_changed, and the epic stays in adr_review.
func TestC27VerdictAtAHeadTheADRPullRequestLeftIsRefused(t *testing.T) {
	w := newWorld(t)
	live := shaB
	ep, rev := w.adrInReview(shaA, &live)
	before := w.show(ep)
	res := w.call("review submit", rev, []string{ep}, map[string]string{"verdict": "pass", "head": shaA, "body": "The ADR holds."})
	if r := wantCode(t, res, cli.CodeStatusChanged); r.Rule != "head_changed" || r.CurrentStatus != "adr_review" {
		t.Fatalf("a verdict at a left head: %+v", *r)
	}
	if it := w.show(ep); it.Status != "adr_review" || it.Seq != before.Seq || it.Owner != "rev" {
		t.Fatalf("a refused verdict changed the epic: %s seq %d owner %q", it.Status, it.Seq, it.Owner)
	}
}

// C28: a merge that adds the ADR during the review moves the epic to
// spec_needed and drops the reviewer's lease. A close with no merge
// returns it to architecture and drops the lease. Each fences the
// reviewer's run.
func TestC28MergeOrCloseEndsTheADRReview(t *testing.T) {
	for _, c := range []struct {
		verb   string
		params map[string]string
		to     string
	}{
		{VerbADRLanded, map[string]string{"path": "docs/adr/0013-x.md", "added": "a1"}, SpecNeeded},
		{"adr_withdrawn", map[string]string{"pr": "12"}, Architecture},
	} {
		t.Run(c.verb, func(t *testing.T) {
			w := newWorld(t)
			live := shaA
			ep, _ := w.adrInReview(shaA, &live)
			held := w.show(ep)
			w.must(ep, c.verb, c.params)
			if it := w.show(ep); it.Status != c.to || it.Owner != "" || it.Epoch <= held.Epoch {
				t.Fatalf("%s: %s owner %q epoch %d, want %s with no owner past epoch %d", c.verb, it.Status, it.Owner, it.Epoch, c.to, held.Epoch)
			}
		})
	}
}
