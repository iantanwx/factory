package machine

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

const adrNeededWords = "R3 and R7 bind every later epic's table"

// prdInReview makes a PRD epic in prd_review, held by a doc-reviewer, with
// facts, and returns the reviewer's principal.
func (w *world) prdInReview(facts map[string]string) (string, rpc.Principal) {
	w.t.Helper()
	ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
	if facts != nil {
		w.force(ep, PRDReview, facts)
	}
	return ep, w.claimAs(ep, StageDocReviewer, "rev")
}

// C21: a PRD pass with --adr-needed writes adr_needed with the text, and
// the verdict comment and the review fact hold it.
func TestC21PRDPassRecordsEachPassingRequirement(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)
	ep, rev := w.prdInReview(nil)

	mustOK(t, w.call("review submit", rev, []string{ep}, map[string]string{
		"verdict": "pass", "head": shaA, "body": "The PRD holds.", "adr-needed": adrNeededWords}))
	it := w.show(ep)
	if it.Status != PRDSignoff || it.Fact(FactADRNeeded) != adrNeededWords {
		t.Fatalf("after the pass: status %s, adr_needed %q", it.Status, it.Fact(FactADRNeeded))
	}
	if len(log.calls) != 1 || !strings.Contains(log.calls[0], "The PRD holds.") || !strings.Contains(log.calls[0], adrNeededWords) {
		t.Fatalf("the verdict comment does not hold the requirements: %v", log.calls)
	}
	if !strings.Contains(it.Fact(FactReview), adrNeededWords) {
		t.Fatalf("the review fact does not hold the requirements: %q", it.Fact(FactReview))
	}
}

// C21: a later PRD pass with no flag clears adr_needed.
func TestC21PRDPassWithNoFlagClearsADRNeeded(t *testing.T) {
	w := newWorld(t)
	ep, rev := w.prdInReview(map[string]string{FactADRNeeded: adrNeededWords})

	mustOK(t, w.call("review submit", rev, []string{ep}, map[string]string{"verdict": "pass", "head": shaA, "body": "The PRD holds."}))
	if it := w.show(ep); it.Status != PRDSignoff || it.Fact(FactADRNeeded) != "" {
		t.Fatalf("after a pass with no flag: status %s, adr_needed %q", it.Status, it.Fact(FactADRNeeded))
	}
}

// C21: the flag refuses with USAGE rule adr_needed_scope on a reject, on
// an epic in spec_review, and on a ticket. Each refusal posts no comment
// and changes nothing.
func TestC21ADRNeededRefusedOutsideAPRDPass(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)

	prd, prdRev := w.prdInReview(nil)
	spec := w.create(KindEpic, "prd_submit", map[string]string{"pr": "8"})
	w.force(spec, SpecReview, map[string]string{FactPRDMerged: "m1", FactDocPR: "9"})
	specRev := w.claimAs(spec, StageDocReviewer, "rev-spec")
	ticket, codeRev := w.inReview()

	cases := []struct {
		name, item, verdict string
		as                  rpc.Principal
	}{
		{"a PRD reject", prd, "reject", prdRev},
		{"a spec pass", spec, "pass", specRev},
		{"a spec reject", spec, "reject", specRev},
		{"a ticket pass", ticket, "pass", codeRev},
		{"a ticket reject", ticket, "reject", codeRev},
	}
	for _, c := range cases {
		before := w.show(c.item)
		res := w.call("review submit", c.as, []string{c.item}, map[string]string{
			"verdict": c.verdict, "head": shaA, "body": "Review.", "adr-needed": adrNeededWords})
		if r := wantCode(t, res, cli.CodeUsage); r.Rule != "adr_needed_scope" {
			t.Errorf("%s: rule %q, want adr_needed_scope", c.name, r.Rule)
		}
		if after := w.show(c.item); after.Status != before.Status || after.Seq != before.Seq || after.Fact(FactADRNeeded) != "" {
			t.Fatalf("%s: the refusal changed the item: %+v", c.name, after)
		}
	}
	if len(log.calls) != 0 {
		t.Fatalf("a refused flag posted comments: %v", log.calls)
	}
}

// C22: the PRD review's finding routes the merge. A pass with the flag,
// then the PRD merge, lands the epic on architecture.
func TestC22PRDReviewFindingRoutesTheMerge(t *testing.T) {
	w := newWorld(t)
	ep, rev := w.prdInReview(nil)

	mustOK(t, w.call("review submit", rev, []string{ep}, map[string]string{
		"verdict": "pass", "head": shaA, "body": "The PRD holds.", "adr-needed": adrNeededWords}))
	w.must(ep, "doc_merged", map[string]string{"sha": "m1"})
	if it := w.show(ep); it.Status != Architecture || it.Fact(FactPRDMerged) != "m1" || it.Fact(FactADRNeeded) != adrNeededWords {
		t.Fatalf("the PRD merge after a flagged pass: status %s, prd_merged %q, adr_needed %q",
			it.Status, it.Fact(FactPRDMerged), it.Fact(FactADRNeeded))
	}
}
