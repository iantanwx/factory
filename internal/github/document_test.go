package github

import (
	"context"
	"strconv"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// epicInReview submits a PRD epic whose document PR is pr.
func (e *env) epicInReview(pr int) string {
	e.t.Helper()
	id, res := e.m.Create(context.Background(), machine.CreateRequest{Project: testProject, Kind: machine.KindEpic, Title: "e",
		Verb: "prd_submit", Params: map[string]string{"pr": strconv.Itoa(pr)}, Actor: machine.Server("test")})
	mustOK(e.t, res)
	return id
}

// U13: a PRD PR merged while the doc-reviewer holds it counts as the
// operator's sign-off. The merge lands on sign-off first, as the outran
// row says, and the sign-off row carries it on in the same read. The
// review lease is revoked.
func TestDocumentMergedDuringReviewIsTheSignoff(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	pr := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	id := e.epicInReview(pr.Number)
	w.Update(pr, func(p *gt.PR) { p.Head = BranchFor(id) })
	l, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageDocReviewer, RunID: "run-rev"})
	mustOK(t, res)
	r := e.reactor()
	w.Update(pr, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaM })
	e.tick(r)
	it := e.status(id)
	if it.Status != machine.SpecNeeded || it.Owner != "" || it.Epoch <= l.Epoch || it.Fact(machine.FactPRDMerged) != shaM {
		t.Fatalf("after the merge in review: %s owner %q epoch %d %v", it.Status, it.Owner, it.Epoch, it.Facts)
	}
}

// Only the epic's current document PR moves it. The merged PRD PR, read
// again while the spec waits for its author or its sign-off, moves
// nothing, and it is no fault that blocks the reconcile.
func TestOnlyTheCurrentDocumentPRMovesTheEpic(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	prd := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	id := e.epicInReview(prd.Number)
	w.Update(prd, func(p *gt.PR) { p.Head = BranchFor(id) })
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "review_pass", Actor: machine.Server("test")}))
	r := e.reactor()
	w.Update(prd, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaM })
	e.tick(r)
	if got := e.status(id).Status; got != machine.SpecNeeded {
		t.Fatalf("the operator's merge left the epic in %s", got)
	}
	// The spec PR opens, passes review and waits for the operator.
	_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageSpecAuthor, RunID: "run-spec"})
	mustOK(t, res)
	spec := w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaB})
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "pr_open",
		Params: map[string]string{"pr": strconv.Itoa(spec.Number)}, Actor: machine.Server("test")}))
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "review_pass", Actor: machine.Server("test")}))
	// The old PRD PR is read again: the spec is not signed off by it.
	w.Update(prd, func(p *gt.PR) {})
	e.tick(r)
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.status(id).Status; got != machine.SpecSignoff {
		t.Fatalf("the merged PRD PR moved the epic to %s", got)
	}
	w.Update(spec, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaM })
	e.tick(r)
	if got := e.status(id).Status; got != machine.SpecApproved {
		t.Fatalf("the spec merge left the epic in %s", got)
	}
}

// A closed document PR escalates the epic, and never cancels it.
func TestClosedDocumentPREscalatesTheEpic(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	pr := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	id := e.epicInReview(pr.Number)
	w.Update(pr, func(p *gt.PR) { p.Head = BranchFor(id) })
	r := e.reactor()
	w.Update(pr, func(p *gt.PR) { p.State = "closed" })
	e.tick(r)
	if it := e.status(id); it.Status != machine.Escalated || it.Fact(machine.FactResume) != machine.PRDReview {
		t.Fatalf("after the close: %s %v", it.Status, it.Facts)
	}
}
