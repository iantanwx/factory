package machine

import (
	"slices"
	"strconv"
	"testing"
)

// bugs-report-ask.test.ts (alpha): the reproducer asks a report's
// reporter for a missing fact, at most twice. Each ask ends the run's
// lease and leaves the report in asked, so the inbox relays the question.
// The reporter's answer returns the report to reported with the answer,
// and the third ask refuses with ASK_LIMIT, so the run escalates instead.
func TestReproducerAsksTheReporterAtMostTwice(t *testing.T) {
	w := newWorld(t)
	bug := w.create(KindBug, "file", nil)
	questions := []string{"- Which page shows the empty list?", "- Which account did you use?"}
	answers := []string{"the widgets page", "every account I tried"}
	for i := range 2 {
		n := strconv.Itoa(i + 1)
		mustOK(t, w.tryClaim(bug, StageReproducer, "run-"+n, ""))
		mustOK(t, w.apply(bug, "ask", map[string]string{"body": questions[i]}))
		it := w.show(bug)
		if it.Status != "asked" || it.Owner != "" || it.Fact("asks") != n ||
			it.Fact("question_"+n) != questions[i] || it.Fact("asked_by") != "server:test" {
			t.Fatalf("ask %s: %+v", n, it)
		}
		mustOK(t, w.apply(bug, VerbAnswer, map[string]string{"answer": answers[i]}))
		it = w.show(bug)
		if it.Status != Reported || it.Fact(FactAnswer) != answers[i] || it.Fact("answer_"+n) != answers[i] ||
			it.Fact("question_"+n) != questions[i] || it.Fact("asks") != n {
			t.Fatalf("answer %s: %+v", n, it)
		}
	}
	// Each round stays on the report, so the run that meets the limit can
	// quote both questions and both answers in its escalation.
	it := w.show(bug)
	for i, want := range map[string]string{"question_1": questions[0], "answer_1": answers[0], "question_2": questions[1], "answer_2": answers[1]} {
		if it.Fact(i) != want {
			t.Fatalf("after two rounds the report holds %s = %q, want %q: %+v", i, it.Fact(i), want, it.Facts)
		}
	}
	mustOK(t, w.tryClaim(bug, StageReproducer, "run-3", ""))
	ref := wantCode(t, w.apply(bug, "ask", map[string]string{"body": "- And which browser?"}), "ASK_LIMIT")
	if ref.CurrentStatus != Reproducing || w.show(bug).Status != Reproducing {
		t.Fatalf("the third ask moved the report: %+v", ref)
	}
	if slices.Contains(ref.AllowedNext, "ask") || !slices.Contains(ref.AllowedNext, VerbEscalate) {
		t.Fatalf("the ASK_LIMIT refusal names allowed_next %v, want escalate and no ask", ref.AllowedNext)
	}
	// The run that holds the report escalates the missing fact instead.
	mustOK(t, w.apply(bug, VerbEscalate, nil))
	if got := w.show(bug).Status; got != Escalated {
		t.Fatalf("escalate after the third ask landed on %s", got)
	}
}
