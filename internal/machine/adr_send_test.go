package machine

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

const sendWords = "Ian: this one needs an ADR before the spec."

// C9: the operator's send before the PRD merge keeps the status and
// writes adr_sent, in each PRD state. The PRD merge then lands the epic
// on architecture with prd_merged set, and not on spec_needed.
func TestC9SendBeforeThePRDMergeRoutesTheMerge(t *testing.T) {
	w := newWorld(t)
	for _, from := range []string{"prd_needed", "prd_drafting", "prd_review", "prd_amending", "prd_signoff"} {
		ep := w.create(KindEpic, "create_prd", nil)
		w.force(ep, from, nil)
		w.must(ep, "adr_send", map[string]string{"request": sendWords})
		if it := w.show(ep); it.Status != from || it.Fact("adr_sent") != sendWords {
			t.Fatalf("send from %s: status %s, adr_sent %q", from, it.Status, it.Fact("adr_sent"))
		}
	}

	ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
	w.must(ep, "review_pass", map[string]string{"head": "h1"})
	w.must(ep, "adr_send", map[string]string{"request": sendWords})
	w.must(ep, "doc_merged", map[string]string{"sha": "m1"})
	it := w.show(ep)
	if it.Status != "architecture" || it.Fact("prd_merged") != "m1" || it.Fact("adr_sent") != sendWords {
		t.Fatalf("the PRD merge of a sent epic: status %s, prd_merged %q, adr_sent %q", it.Status, it.Fact("prd_merged"), it.Fact("adr_sent"))
	}
}

// C9: the PRD review's adr_needed routes the merge the same way as the
// operator's send.
func TestC9ADRNeededRoutesThePRDMerge(t *testing.T) {
	w := newWorld(t)
	ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
	w.force(ep, "prd_signoff", map[string]string{"reviewed_head": "h1", "adr_needed": "R3 binds every epic"})
	w.must(ep, "doc_merged", map[string]string{"sha": "m1"})
	if it := w.show(ep); it.Status != "architecture" || it.Fact("prd_merged") != "m1" {
		t.Fatalf("the PRD merge with adr_needed: status %s, prd_merged %q", it.Status, it.Fact("prd_merged"))
	}
}

// C10: an epic nobody sent lands on spec_needed at its PRD merge, while
// the operator sends another epic in the same state.
func TestC10UnsentEpicGoesOnAsToday(t *testing.T) {
	w := newWorld(t)
	unsent := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
	sent := w.create(KindEpic, "prd_submit", map[string]string{"pr": "8"})
	for _, ep := range []string{unsent, sent} {
		w.must(ep, "review_pass", map[string]string{"head": "h1"})
	}
	w.must(sent, "adr_send", map[string]string{"request": sendWords})
	for _, ep := range []string{unsent, sent} {
		w.must(ep, "doc_merged", map[string]string{"sha": "m1"})
	}
	it := w.show(unsent)
	if it.Status != "spec_needed" || it.Fact("prd_merged") != "m1" || it.Fact("adr_sent") != "" {
		t.Fatalf("the PRD merge of an unsent epic: status %s, prd_merged %q, adr_sent %q", it.Status, it.Fact("prd_merged"), it.Fact("adr_sent"))
	}
	if got := w.show(sent).Status; got != "architecture" {
		t.Fatalf("the PRD merge of the sent epic: status %s", got)
	}
}

// C16: the send in the spec stage moves an unheld epic to architecture,
// writes adr_sent, and keeps the spec pull request in doc_pr.
func TestC16SendInTheSpecStageReturnsToArchitecture(t *testing.T) {
	w := newWorld(t)
	for _, from := range []string{"spec_needed", "spec_review", "spec_signoff", "spec_amending"} {
		ep := w.create(KindEpic, "create_prd", nil)
		w.force(ep, from, map[string]string{"prd_merged": "m1", "doc_pr": "9"})
		w.must(ep, "adr_send", map[string]string{"request": sendWords})
		it := w.show(ep)
		if it.Status != "architecture" || it.Fact("adr_sent") != sendWords || it.Fact("doc_pr") != "9" {
			t.Fatalf("send from %s: status %s, adr_sent %q, doc_pr %q", from, it.Status, it.Fact("adr_sent"), it.Fact("doc_pr"))
		}
	}
}

// C15: the send refuses outside its rows, and on a spec-stage epic that a
// run holds. Each refusal changes nothing.
func TestC15SendRefusedOutsideItsRows(t *testing.T) {
	w := newWorld(t)
	for _, from := range []string{"spec_drafting", "spec_approved", "decomposing", "execution", "done", "escalated", "architecture", "adr_review"} {
		ep := w.create(KindEpic, "create_prd", nil)
		w.force(ep, from, nil)
		before := w.show(ep)
		r := wantCode(t, w.apply(ep, "adr_send", map[string]string{"request": sendWords}), cli.CodeTransitionNotAllowed)
		if r.Rule != "no_edge" {
			t.Errorf("send from %s: rule %q, want no_edge", from, r.Rule)
		}
		if after := w.show(ep); after.Status != from || after.Seq != before.Seq || after.Fact("adr_sent") != "" {
			t.Fatalf("a refused send from %s changed the epic: %+v", from, after)
		}
	}

	ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
	w.force(ep, "spec_review", map[string]string{"prd_merged": "m1", "doc_pr": "9"})
	if _, res := w.m.Claim(context.Background(), ClaimRequest{Project: prj, Item: ep, Stage: StageDocReviewer, RunID: "r-rev"}); res.Refusal != nil {
		t.Fatalf("claim: %+v", res.Refusal)
	}
	before := w.show(ep)
	r := wantCode(t, w.apply(ep, "adr_send", map[string]string{"request": sendWords}), cli.CodeLeaseHeld)
	if r.Rule != "lease_held" {
		t.Errorf("send on a held epic: rule %q, want lease_held", r.Rule)
	}
	if after := w.show(ep); after.Status != "spec_review" || after.Seq != before.Seq || after.Fact("adr_sent") != "" {
		t.Fatalf("a refused send changed the held epic: %+v", after)
	}
}
