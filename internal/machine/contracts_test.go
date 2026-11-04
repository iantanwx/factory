package machine

import (
	"context"
	"regexp"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

const ticketBody = "## Source\n\nSpec entry 7.\n\n## Change\n\nRefuse it.\n\n" +
	"## Contracts to prove\n\n- C1. Refusal: one.\n- C10. Behaviour: ten.\n\n## Files\n\n- verbs.go\n"

const bugBody = "## Contract C-bug-1\n\nThe verb refuses.\n\n## Source\n\nA report.\n"

// shapedInReview makes a ticket with body and facts in in_review, and claims
// it for a code reviewer.
func (w *world) shapedInReview(body string, facts map[string]string) (string, rpc.Principal) {
	w.t.Helper()
	id, res := w.m.Create(context.Background(), CreateRequest{Project: prj, Kind: KindTicket, Title: "item",
		Description: body, Verb: "create", Actor: Server("test"), Facts: facts})
	mustOK(w.t, res)
	w.claimAs(id, StageImplementer, "impl")
	w.opened(id, shaA)
	return id, w.claimAs(id, StageCodeReviewer, "rev")
}

// wantUnnamed holds a contract_unnamed refusal that names each id of missing,
// and a ticket that the refused verdict left in in_review with no comment.
func (w *world) wantUnnamed(res cli.Result, id string, log *commentLog, missing ...string) {
	w.t.Helper()
	r := wantCode(w.t, res, cli.CodeShapeRefused)
	if r.Rule != "contract_unnamed" || r.Item != id || r.CurrentStatus != InReview {
		w.t.Fatalf("refusal: %+v", *r)
	}
	for _, m := range missing {
		if !regexp.MustCompile(`\b` + m + `\b`).MatchString(r.Hint) {
			w.t.Fatalf("the hint %q does not name %s", r.Hint, m)
		}
	}
	if it := w.show(id); it.Status != InReview || it.Fact(FactReview) != "" || it.Fact(FactReviewedHead) != "" || len(log.calls) != 0 {
		w.t.Fatalf("a refused verdict wrote: %s %v %v", it.Status, it.Facts, log.calls)
	}
}

// C13: on a ticket with shape=ticket, a pass and a reject must name each id
// under Contracts to prove. C1 does not name C10. A refused verdict posts no
// comment and records nothing. The same body with no shape fact is not
// checked.
func TestC13TicketReviewNamesEachContractID(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)

	id, rev := w.shapedInReview(ticketBody, map[string]string{FactShape: "ticket"})
	w.wantUnnamed(w.call("review submit", rev, []string{id}, map[string]string{"verdict": "pass", "head": shaA, "body": "C1 pass: the test fails at the base."}), id, log, "C10")
	w.wantUnnamed(w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "body": "C10 fail: no test."}), id, log, "C1")
	w.wantUnnamed(w.call("review submit", rev, []string{id}, map[string]string{"verdict": "pass", "head": shaA}), id, log, "C1", "C10")

	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "pass", "head": shaA, "body": "- C1 pass: red at the base.\n- C10 pass: red at the base."}))
	if it := w.show(id); it.Status != ReadyToMerge || len(log.calls) != 1 {
		t.Fatalf("a pass that names each id: %s %v", it.Status, log.calls)
	}

	plain, rev := w.shapedInReview(ticketBody, nil)
	mustOK(t, w.call("review submit", rev, []string{plain}, map[string]string{"verdict": "reject", "body": "Fix the test."}))
	if it := w.show(plain); it.Status != Todo || len(log.calls) != 2 {
		t.Fatalf("a ticket with no shape fact: %s %v", it.Status, log.calls)
	}

	ep, res := w.m.Create(context.Background(), CreateRequest{Project: prj, Kind: KindEpic, Title: "epic",
		Description: ticketBody, Verb: "prd_submit", Actor: Server("test"), Facts: map[string]string{FactShape: "ticket"}})
	mustOK(t, res)
	w.claim(ep, StageDocReviewer, "doc-rev")
	mustOK(t, w.call("review submit", w.runOn(ep, StageDocReviewer, "doc-rev"), []string{ep}, map[string]string{"verdict": "pass", "head": shaA, "body": "Reads well."}))
	if got := w.show(ep).Status; got != PRDSignoff {
		t.Fatalf("an epic's review: %s", got)
	}
}

// C13: on a ticket with shape=bug, the review must name each id of a
// Contract C-bug-<n> heading. C-bug-10 does not name C-bug-1.
func TestC13BugReviewNamesEachContractID(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)

	id, rev := w.shapedInReview(bugBody, map[string]string{FactShape: "bug"})
	w.wantUnnamed(w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "body": "C-bug-10 fail."}), id, log, "C-bug-1")

	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "body": "C-bug-1 fail: the test passes at the base."}))
	if it := w.show(id); it.Status != Todo || len(log.calls) != 1 {
		t.Fatalf("a reject that names C-bug-1: %s %v", it.Status, log.calls)
	}
}
