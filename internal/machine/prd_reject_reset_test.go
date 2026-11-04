package machine

import "testing"

// factorio-5iq9: the operator's answer to the second PRD reject starts the
// count again. The next reject goes to the reviser once, and the reject
// after it escalates again.
func TestAnswerResetsThePRDRejectCount(t *testing.T) {
	w := newWorld(t)
	ep := w.create(KindEpic, "prd_submit", nil)
	w.must(ep, "review_reject", nil)
	w.must(ep, "prd_amended", map[string]string{"head": "h2"})
	w.must(ep, "review_reject", nil)
	if got := w.show(ep).Status; got != Escalated {
		t.Fatalf("the second reject landed on %s, want %s", got, Escalated)
	}
	w.must(ep, "answer", nil)
	if it := w.show(ep); it.Status != PRDReview || it.Fact(FactPRDRejects) != "" {
		t.Fatalf("after the answer: %s prd_rejects=%q, want %s and no count", it.Status, it.Fact(FactPRDRejects), PRDReview)
	}
	w.must(ep, "review_reject", nil)
	if it := w.show(ep); it.Status != PRDAmending || it.Fact(FactPRDRejects) != "1" {
		t.Fatalf("the first reject after the answer: %s prd_rejects=%q, want %s and 1", it.Status, it.Fact(FactPRDRejects), PRDAmending)
	}
	w.must(ep, "prd_amended", map[string]string{"head": "h3"})
	w.must(ep, "review_reject", nil)
	if it := w.show(ep); it.Status != Escalated || it.Fact(FactPRDRejects) != "2" {
		t.Fatalf("the second reject after the answer: %s prd_rejects=%q, want %s and 2", it.Status, it.Fact(FactPRDRejects), Escalated)
	}
}
