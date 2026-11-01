package github

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// designHead is the head the fake GitHub gives a pull request it opens.
var designHead = strings.Repeat("c", 40)

// designInReview opens the design pull request of an epic in design from
// its design room, and returns the epic and the pull request.
func (e *env) designInReview(w *gt.World) (string, *gt.PR) {
	e.t.Helper()
	ep := e.designEpic(machine.Design)
	mustOK(e.t, e.roomCall("pr open", ep, "design-room", map[string]string{"title": "Widgets: design record"}))
	n, err := strconv.Atoi(e.status(ep).Fact(machine.FactDocPR))
	if err != nil {
		e.t.Fatalf("the design pull request open left %s with no doc_pr: %v", ep, e.status(ep).Facts)
	}
	return ep, w.PR(n)
}

// C22: the doc-reviewer claims a design pull request in design_review and
// reads it with `pr show`. A verdict at a head the pull request left is
// refused head_changed, and the epic stays. A pass at the head moves the
// epic to design_signoff, pins reviewed_head, and posts the comment.
func TestC22DesignPullRequestGetsOneReviewAtItsHead(t *testing.T) {
	e, w, _ := adrReviewEnv(t)
	ep, pr := e.designInReview(w)
	e.claimDoc(ep)
	res := e.runCall("pr show", ep, machine.StageDocReviewer, nil, nil)
	if v, ok := res.Data.(PRView); res.Refusal != nil || !ok || v.Number != pr.Number || v.HeadSHA != designHead {
		t.Fatalf("pr show = %+v %+v, want #%d at %s", res.Data, res.Refusal, pr.Number, designHead)
	}

	wantRule(t, e.review(ep, "pass", shaA), cli.CodeStatusChanged, "head_changed")
	if it := e.status(ep); it.Status != machine.DesignReview || it.Fact(machine.FactReviewedHead) != "" {
		t.Fatalf("after a verdict at a stale head: %s %v, want design_review with no reviewed_head", it.Status, it.Facts)
	}

	mustOK(t, e.review(ep, "pass", designHead))
	if it := e.status(ep); it.Status != machine.DesignSignoff || it.Fact(machine.FactReviewedHead) != designHead || it.Owner != "" {
		t.Fatalf("after the pass: %s owner %q %v, want design_signoff with reviewed_head %s", it.Status, it.Owner, it.Facts, designHead)
	}
	if n := len(w.Comments(pr.Number)); n != 1 {
		t.Fatalf("%d comments on #%d, want the one verdict", n, pr.Number)
	}
}

// C22: a reject returns the epic to design, where the design room revises
// the record, and the pull request stays open. The room's next `pr open`
// hands the same pull request to review again. A closed design pull
// request escalates the epic, from review or from sign-off, and the
// answer resumes it at design.
func TestC22RejectReturnsTheDesignToItsRoom(t *testing.T) {
	e, w, _ := adrReviewEnv(t)
	ep, pr := e.designInReview(w)
	e.claimDoc(ep)
	mustOK(t, e.review(ep, "reject", designHead))
	if it := e.status(ep); it.Status != machine.Design || it.Owner != "" {
		t.Fatalf("after the reject: %s owner %q, want design", it.Status, it.Owner)
	}
	if got := w.PR(pr.Number).State; got != "open" {
		t.Fatalf("the rejected design pull request is %s, want open", got)
	}
	mustOK(t, e.roomCall("pr open", ep, "design-room", map[string]string{}))
	if it := e.status(ep); it.Status != machine.DesignReview || it.Fact(machine.FactDocPR) != strconv.Itoa(pr.Number) || len(w.PRs()) != 1 {
		t.Fatalf("after the room's second open: %s %v with %d pull requests, want design_review of #%d", it.Status, it.Facts,
			len(w.PRs()), pr.Number)
	}

	for _, from := range []string{machine.DesignReview, machine.DesignSignoff} {
		e := newEnv(t)
		w := e.withWorld()
		r := e.reactor()
		ep, pr := e.designInReview(w)
		if from == machine.DesignSignoff {
			e.apply(ep, "review_pass", map[string]string{"head": designHead})
		}
		w.Update(pr, func(p *gt.PR) { p.State = "closed" })
		e.tick(r)
		if it := e.status(ep); it.Status != machine.Escalated || it.Fact(machine.FactResume) != machine.Design {
			t.Fatalf("a design pull request closed in %s: %s with resume=%q, want escalated with resume=design", from, it.Status,
				it.Fact(machine.FactResume))
		}
		e.apply(ep, machine.VerbAnswer, map[string]string{"answer": "Open the design pull request again."})
		if got := e.status(ep).Status; got != machine.Design {
			t.Fatalf("the answer to a design pull request closed in %s resumed at %s, want design", from, got)
		}
	}
}

// C24: DocMerge names the record that a merged design pull request holds,
// for an epic in design_review or design_signoff. A merge commit with no
// record names none.
func TestC24DocMergeNamesTheDesignRecord(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	for _, status := range []string{machine.DesignReview, machine.DesignSignoff} {
		ep := e.designEpic(status)
		record := "docs/design/" + ep + "/record.md"
		w.SetFile(shaM, record, "# Widgets: design record\n")
		ev, err := e.svc.DocMerge(context.Background(), e.project, e.status(ep), shaM)
		if err != nil || ev.DesignPath != record {
			t.Fatalf("an epic in %s: DocMerge = %+v %v, want design_path %s", status, ev, err, record)
		}
		ev, err = e.svc.DocMerge(context.Background(), e.project, e.status(ep), shaB)
		if err != nil || ev.DesignPath != "" {
			t.Fatalf("an epic in %s, a merge with no record: DocMerge = %+v %v, want no design_path", status, ev, err)
		}
	}
}

// C24: a design merge at the reviewed head passes the base audit with the
// rules of spec_merged, and raises no unreviewed_merge. A design that
// merged during its review fails outran.
func TestC24DesignMergePassesTheBaseAuditAtItsReviewedHead(t *testing.T) {
	for _, c := range []struct {
		name, rule string
		land       func(e *env) string
	}{
		{"merge commit at the reviewed head", "", func(e *env) string {
			side, merge := e.mergeBranch("design", map[string]string{"docs/design/x/record.md": "record\n"})
			ep := e.designEpic(machine.DesignReview)
			e.apply(ep, "review_pass", map[string]string{"head": side})
			e.docMerged(ep, merge)
			return merge
		}},
		{"squash merged during its review", "outran", func(e *env) string {
			ep := e.designEpic(machine.DesignReview)
			sq := commit(e.t, e.checkout, "design", map[string]string{"docs/design/x/record.md": "record\n"})
			e.docMerged(ep, sq)
			return sq
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, r := startAudit(t)
			sha := c.land(e)
			e.pushBase()
			e.tick(r)
			if c.rule == "" {
				e.wantAlerts("")
			} else {
				e.wantAlerts(c.rule, sha)
			}
		})
	}
}

// C20: through the design merge, the spec's merge, execution and done, the
// prototype's branch keeps the head the design room pushed, and the loop
// sends GitHub no delete of a ref or a branch.
func TestC20PrototypeBranchOutlivesTheEpic(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	r := e.reactor()
	ctx := context.Background()
	ep := e.designEpic(machine.Design)
	proto := commit(t, e.checkout, "prototype", map[string]string{"web/index.html": "<p>widgets</p>\n"})
	mustOK(t, e.prototypePush(ep, "design-room"))
	mustOK(t, e.roomCall("pr open", ep, "design-room", map[string]string{"title": "Widgets: design record"}))
	design := w.PR(1)
	e.apply(ep, "review_pass", map[string]string{"head": designHead})
	kept := func(stage, want string) {
		t.Helper()
		e.tick(r)
		if got := e.status(ep).Status; got != want {
			t.Fatalf("after %s: the epic is %s, want %s", stage, got, want)
		}
		if got := e.remoteSHA(PrototypeBranchFor(ep)); got != proto {
			t.Fatalf("after %s: remote %s = %q, want %s", stage, PrototypeBranchFor(ep), got, proto)
		}
	}
	w.Update(design, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaM })
	kept("the design merge", machine.SpecNeeded)

	_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: ep, Stage: machine.StagePlanner, RunID: "run-spec"})
	mustOK(t, res)
	spec := w.Add(&gt.PR{Head: SpecBranchFor(ep), HeadSHA: shaB})
	e.apply(ep, "pr_open", map[string]string{"pr": strconv.Itoa(spec.Number)})
	e.apply(ep, "review_pass", map[string]string{"head": shaB})
	w.Update(spec, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaA })
	kept("the spec merge", machine.SpecApproved)

	_, res = e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: ep, Stage: machine.StageDecomposer, RunID: "run-cut"})
	mustOK(t, res)
	e.apply(ep, machine.VerbDecomposed, nil)
	kept("the decomposition", machine.Execution)
	e.apply(ep, "complete", nil)
	kept("done", machine.Done)

	for _, c := range e.api.Calls() {
		if strings.HasPrefix(c, "DELETE ") && (strings.Contains(c, "/git/refs") || strings.Contains(c, "/branches/")) {
			t.Errorf("the loop sent GitHub %s", c)
		}
	}
}
