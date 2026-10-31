package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
)

// escalateToPerson raises a level 4 escalation on a new ticket and routes it
// to a person, and returns the ticket and its inbox entry.
func (w *humanWorld) escalateToPerson() (item, entry string) {
	w.t.Helper()
	item = todoTicket(w.t, w.bx)
	co := w.runToken("run-1", item, machine.StageImplementer)
	env, code := w.as(co, "escalation", "raise", "--score", "4", "--body", "which of two layouts the owner prefers")
	if code != 0 {
		w.t.Fatalf("raise = %d %v", code, env)
	}
	id := fmt.Sprint(env["data"].(map[string]any)["escalation"].(map[string]any)["escalation"])
	triage := w.runToken("fix-1", "", "triage")
	panelFile(w.t, triage, "preference", nil)
	if env, code := w.as(triage, "escalation", "route", id, "--file", "panel.json"); code != 0 ||
		env["data"].(map[string]any)["route"] != string(escalation.RoutePerson) {
		w.t.Fatalf("route = %d %v", code, env)
	}
	return item, "escalation/alpha/" + item
}

// prepareAbout prepares a question about entry, and returns its entry id.
func (w *humanWorld) prepareAbout(entry, body string) string {
	w.t.Helper()
	env, code := w.coordinator("inbox", "prepare", "--body", body, "--entry", entry)
	if code != 0 {
		w.t.Fatalf("prepare = %d %v", code, env)
	}
	return env["data"].(map[string]any)["entry"].(string)
}

// C17. While a question about an escalation entry has no answer, the
// escalation's answer refuses with TRANSITION_NOT_ALLOWED, rule
// question_open, and the hint names the question. The item stays
// escalated, and the entry's next line says to answer the question first.
// Once the question has its answer, the escalation's answer resumes the
// item.
func TestC17EscalationAnswerWaitsForItsOpenQuestion(t *testing.T) {
	w := newHumanWorld(t, "")
	item, e := w.escalateToPerson()
	q := w.prepareAbout(e, "Which layout? options: left, costs a day; right, costs a week")

	if got := entryByID(w.entries("person"), e); got == nil || !strings.Contains(got["next"].(string), "loop inbox answer "+q+" ") {
		t.Fatalf("escalation entry = %v", got)
	}
	env, code := w.coordinator("inbox", "answer", e, "--body", "use the left layout")
	if code != 3 || env["code"] != cli.CodeTransitionNotAllowed || env["rule"] != "question_open" ||
		!strings.Contains(env["hint"].(string), q) {
		t.Fatalf("early answer = %d %v", code, env)
	}
	if it := w.show(item); it.Status != machine.Escalated {
		t.Fatalf("item after the refusal = %s", it.Status)
	}

	if env, code := w.coordinator("inbox", "answer", q, "--body", "left"); code != 0 {
		t.Fatalf("answer the question = %d %v", code, env)
	}
	if env, code := w.coordinator("inbox", "answer", e, "--body", "use the left layout"); code != 0 {
		t.Fatalf("answer after the question = %d %v", code, env)
	}
	if it := w.show(item); it.Status != machine.Todo || it.Fact(machine.FactAnswer) != "use the left layout" {
		t.Fatalf("answered item = %s %v", it.Status, it.Facts)
	}
}

// C17. An open question about another entry does not hold the escalation:
// the hint names only the escalation's own question, and once that one has
// its answer the escalation's answer resumes the item.
func TestC17QuestionAboutAnotherEntryDoesNotHoldTheEscalation(t *testing.T) {
	w := newHumanWorld(t, "")
	item, e := w.escalateToPerson()
	other := w.prepareAbout("escalation/alpha/other-1", "Which store? options: sqlite, costs nothing; postgres, costs a server")
	q := w.prepareAbout(e, "Which layout? options: left, costs a day; right, costs a week")

	env, code := w.coordinator("inbox", "answer", e, "--body", "use the left layout")
	if code != 3 || env["rule"] != "question_open" || !strings.Contains(env["hint"].(string), q) ||
		strings.Contains(env["hint"].(string), other) {
		t.Fatalf("early answer = %d %v", code, env)
	}
	if env, code := w.coordinator("inbox", "answer", q, "--body", "left"); code != 0 {
		t.Fatalf("answer the question = %d %v", code, env)
	}
	if got := entryByID(w.entries("person"), e); got == nil || strings.Contains(got["next"].(string), other) {
		t.Fatalf("escalation entry = %v", got)
	}
	if env, code := w.coordinator("inbox", "answer", e, "--body", "use the left layout"); code != 0 {
		t.Fatalf("answer with another entry's question open = %d %v", code, env)
	}
	if it := w.show(item); it.Status != machine.Todo {
		t.Fatalf("answered item = %s", it.Status)
	}
}
