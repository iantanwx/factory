package machine

import (
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
	for n := 1; n <= 2; n++ {
		run := "run-" + strconv.Itoa(n)
		mustOK(t, w.tryClaim(bug, StageReproducer, run, ""))
		mustOK(t, w.apply(bug, "ask", map[string]string{"body": "- Which page shows the empty list?"}))
		it := w.show(bug)
		if it.Status != "asked" || it.Owner != "" || it.Fact("asks") != strconv.Itoa(n) ||
			it.Fact("question") != "- Which page shows the empty list?" || it.Fact("asked_by") != "server:test" {
			t.Fatalf("ask %d: %+v", n, it)
		}
		mustOK(t, w.apply(bug, VerbAnswer, map[string]string{"answer": "the widgets page"}))
		it = w.show(bug)
		if it.Status != Reported || it.Fact(FactAnswer) != "the widgets page" || it.Fact("question") != "" || it.Fact("asks") != strconv.Itoa(n) {
			t.Fatalf("answer %d: %+v", n, it)
		}
	}
	mustOK(t, w.tryClaim(bug, StageReproducer, "run-3", ""))
	ref := wantCode(t, w.apply(bug, "ask", map[string]string{"body": "- And which account?"}), "ASK_LIMIT")
	if ref.CurrentStatus != Reproducing || w.show(bug).Status != Reproducing {
		t.Fatalf("the third ask moved the report: %+v", ref)
	}
	// The run that holds the report escalates the missing fact instead.
	mustOK(t, w.apply(bug, VerbEscalate, nil))
	if got := w.show(bug).Status; got != Escalated {
		t.Fatalf("escalate after the third ask landed on %s", got)
	}
}
