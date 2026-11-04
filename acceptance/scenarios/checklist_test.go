//go:build acceptance

package scenarios

import (
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/machine"
)

// stopBug is a bug body of the Bug shape with one stop condition.
const stopBug = shapedBug + "\n## Stop conditions\n\n- The fix calls a paid API.\n"

// A code-reviewer finds a stop condition of a shaped ticket fired, and the
// ticket waits in the operator's inbox, not in todo. The operator's answer
// waives it, and the next review passes the ticket with a waived line that
// names that answer, as `review checklist` lists it.
func TestAFiredStopConditionWaitsForTheOperatorsWaiver(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	bug := fileBug(w, "The list page shows no widgets", "Open /widgets: the list is empty, and the API returns three widgets.")
	repro := w.Dispatch(machine.StageReproducer)
	ticket := w.Must(w.In(repro.Checkout, "work", "shape", bug, "--title", "Show every widget on the list page",
		"--body", stopBug, "--priority", "2")).Str("ticket")
	w.Must(w.In(repro.Checkout, "run", "finish", "--outcome", "done"))

	_, head := implement(w, ticket, map[string]string{"web/list.go": "package web\n"})
	w.CI(head, "success")
	rest := "- C-bug-1 pass: the test is red at the base.\n- D1 pass: the test passes.\n"
	review(w, ticket, "reject", "--body", rest+"- stop1 fail: the fix calls the paid widgets API.")
	wantStatus(w, ticket, machine.Escalated)
	entry := "escalation/" + acceptance.Project + "/" + ticket
	if w.Entry(entry) == nil {
		t.Fatal("a fired stop condition put no escalation in the inbox")
	}
	w.Must(w.Coordinator("inbox", "answer", entry, "--body", "Waive stop1: the paid API is in budget."))
	wantStatus(w, ticket, machine.Todo)

	head = rework(w, ticket, map[string]string{"web/list.go": "package web\n\n// List lists every widget.\n"})
	w.CI(head, "success")
	rev := w.Dispatch(machine.StageCodeReviewer)
	var answer string
	for _, o := range w.Must(w.In(rev.Checkout, "review", "checklist", ticket)).List() {
		if line := o.(map[string]any); line["id"] == "stop1" {
			if as, _ := line["answers"].([]any); len(as) == 1 {
				answer, _ = as[0].(string)
			}
		}
	}
	if answer == "" {
		t.Fatal("review checklist names no standing answer for stop1")
	}
	head = w.Must(w.In(rev.Checkout, "pr", "show")).Str("head_sha")
	w.Must(w.In(rev.Checkout, "review", "submit", ticket, "--verdict", "pass", "--head", head,
		"--body", rest+"- stop1 waived: "+answer+" waives it."))
	w.Must(w.In(rev.Checkout, "run", "finish", "--outcome", "done"))
	wantStatus(w, ticket, machine.ReadyToMerge)
}
