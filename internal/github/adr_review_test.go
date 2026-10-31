package github

import (
	"context"
	"strconv"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

const docRun = "run-" + machine.StageDocReviewer

// adrReviewEnv is a world whose machine reads the live head and posts the
// verdict comment through the service, as the server wires it.
func adrReviewEnv(t *testing.T) (*env, *gt.World, *Reactor) {
	t.Helper()
	e := newEnv(t)
	w := e.withWorld()
	e.m.SetPRHeadReader(e.svc.PRHead)
	e.m.SetReviewCommenter(e.svc.ReviewComment)
	return e, w, e.reactor()
}

// openADRPull opens a pull request on branch whose head adds the ADR at path.
func openADRPull(w *gt.World, branch, head, path, text string) *gt.PR {
	w.SetFile(head, path, text)
	return w.Add(&gt.PR{Head: branch, HeadSHA: head, Added: []string{path}})
}

// claimDoc claims epic for a doc-reviewer run.
func (e *env) claimDoc(epic string) {
	e.t.Helper()
	_, res := e.m.Claim(context.Background(), machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageDocReviewer, RunID: docRun})
	mustOK(e.t, res)
}

// review submits a doc-reviewer verdict on epic at head, as the run that
// holds it.
func (e *env) review(epic, verdict, head string) cli.Result {
	e.t.Helper()
	v, _ := cli.Surface().Verb("review submit")
	call := &rpc.Call{
		Inv: cli.Invocation{Verb: v, Args: []string{epic}, Extra: map[string]string{}, RequestID: "req-review-" + head,
			Flags: map[string]string{"verdict": verdict, "head": head, "body": "The ADR binds two epics."}},
		Principal: rpc.Principal{Kind: cli.PrincipalRun, RunID: docRun, Project: testProject, Item: epic,
			Stage: machine.StageDocReviewer, Epoch: e.status(epic).Epoch},
	}
	return e.m.Handlers()["review submit"](context.Background(), call)
}

// wantReview fails unless epic is in adr_review of pull request n at head.
func (e *env) wantReview(epic string, n int, head string) {
	e.t.Helper()
	it := e.status(epic)
	if it.Status != "adr_review" || it.Fact("adr_pr") != strconv.Itoa(n) || it.Fact("adr_head") != head {
		e.t.Fatalf("epic %s: %s with adr_pr=%q adr_head=%q, want adr_review of #%d at %s", epic, it.Status,
			it.Fact("adr_pr"), it.Fact("adr_head"), n, head)
	}
}

// C26: an open pull request that adds an ADR whose Ticket line names an
// epic in architecture moves the epic to adr_review, wherever the branch
// lives. The doc-reviewer reads that pull request, and its verdict at the
// head returns the epic to architecture with the comment on the pull
// request. A later tick at the same head moves nothing. A new head opens
// the review again.
func TestC26EachADRPullRequestGetsOneReviewAtItsHead(t *testing.T) {
	e, w, r := adrReviewEnv(t)
	ep := e.prdEpicIn(machine.Architecture, map[string]string{machine.FactPRDMerged: "m1"})
	// The operator wrote this ADR by hand, on a branch that names no epic.
	pr := openADRPull(w, "ian/store-adr", shaA, adrPath(13), adrText(ep))
	e.tick(r)
	e.wantReview(ep, pr.Number, shaA)

	e.claimDoc(ep)
	res := e.runCall("pr show", ep, machine.StageDocReviewer, nil, nil)
	if v, ok := res.Data.(PRView); res.Refusal != nil || !ok || v.Number != pr.Number || v.HeadSHA != shaA {
		t.Fatalf("pr show on the epic in adr_review: %+v", res)
	}
	mustOK(t, e.review(ep, "pass", shaA))
	if it := e.status(ep); it.Status != machine.Architecture || it.Owner != "" {
		t.Fatalf("after the verdict: %s owner %q", it.Status, it.Owner)
	}
	if n := len(w.Comments(pr.Number)); n != 1 {
		t.Fatalf("the ADR pull request holds %d comments, want the verdict's", n)
	}

	e.tick(r)
	if it := e.status(ep); it.Status != machine.Architecture {
		t.Fatalf("a tick at the reviewed head moved the epic to %s", it.Status)
	}

	w.SetFile(shaB, adrPath(13), adrText(ep))
	w.Update(pr, func(p *gt.PR) { p.HeadSHA = shaB })
	e.tick(r)
	e.wantReview(ep, pr.Number, shaB)
}

// C27: a verdict at a head the ADR pull request left is refused with rule
// head_changed, posts nothing, and the epic stays in adr_review.
func TestC27VerdictAtAHeadTheADRPullRequestLeftIsRefused(t *testing.T) {
	e, w, r := adrReviewEnv(t)
	ep := e.prdEpicIn(machine.Architecture, map[string]string{machine.FactPRDMerged: "m1"})
	pr := openADRPull(w, ADRBranchFor(ep), shaA, adrPath(13), adrText(ep))
	e.tick(r)
	e.claimDoc(ep)
	w.Update(pr, func(p *gt.PR) { p.HeadSHA = shaB })

	res := e.review(ep, "pass", shaA)
	if res.Refusal == nil || res.Refusal.Code != cli.CodeStatusChanged || res.Refusal.Rule != "head_changed" {
		t.Fatalf("a verdict at the left head: %+v", res)
	}
	if it := e.status(ep); it.Status != "adr_review" || it.Owner != docRun || len(w.Comments(pr.Number)) != 0 {
		t.Fatalf("the refused verdict: %s owner %q, %d comments", it.Status, it.Owner, len(w.Comments(pr.Number)))
	}
}

// C28: a merge that adds the ADR during the review moves the epic to
// spec_needed and drops the reviewer's lease. A close with no merge
// returns it to architecture and drops the lease. An open pull request
// whose ADR reads **Ticket:** none moves no epic, even one its prose
// names.
func TestC28MergeOrCloseEndsTheADRReview(t *testing.T) {
	t.Run("merge", func(t *testing.T) {
		e, w, r := adrReviewEnv(t)
		ep := e.prdEpicIn(machine.Architecture, map[string]string{machine.FactPRDMerged: "m1"})
		pr := openADRPull(w, ADRBranchFor(ep), shaA, adrPath(13), adrText(ep))
		e.tick(r)
		e.claimDoc(ep)
		w.Update(pr, func(p *gt.PR) { p.State, p.Merged = "closed", true })
		e.land(r, map[string]string{adrPath(13): adrText(ep)})
		if it := e.status(ep); it.Status != machine.SpecNeeded || it.Owner != "" || it.Fact(machine.FactADR) != adrPath(13) {
			t.Fatalf("after the merge: %s owner %q adr=%q", it.Status, it.Owner, it.Fact(machine.FactADR))
		}
	})
	t.Run("close", func(t *testing.T) {
		e, w, r := adrReviewEnv(t)
		ep := e.prdEpicIn(machine.Architecture, map[string]string{machine.FactPRDMerged: "m1"})
		pr := openADRPull(w, ADRBranchFor(ep), shaA, adrPath(13), adrText(ep))
		e.tick(r)
		e.claimDoc(ep)
		w.Update(pr, func(p *gt.PR) { p.State = "closed" })
		for range 2 {
			e.tick(r)
			if it := e.status(ep); it.Status != machine.Architecture || it.Owner != "" {
				t.Fatalf("after the close: %s owner %q", it.Status, it.Owner)
			}
		}
	})
	t.Run("none", func(t *testing.T) {
		e, w, r := adrReviewEnv(t)
		ep := e.prdEpicIn(machine.Architecture, map[string]string{machine.FactPRDMerged: "m1"})
		openADRPull(w, "ian/all-epics", shaA, adrPath(13), adrText("none", ep))
		e.tick(r)
		if it := e.status(ep); it.Status != machine.Architecture {
			t.Fatalf("an ADR whose Ticket line reads none moved the epic to %s", it.Status)
		}
	})
}
