package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/escalation"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// capFinding is the reject text of the review that reaches the PRD cap.
const capFinding = "1. US-01 names no screen. Replace \"See widgets\" with \"See the widget list\"."

// prdAtCap is a PRD epic that the doc-reviewer rejected twice: the second
// reject reaches the cap with capFinding.
func prdAtCap(t *testing.T) (*humanWorld, string) {
	t.Helper()
	w, _, _ := newDocWorld(t)
	r := w.openRoom("brainstorm", "", "the operator wants widgets")
	co := r["checkout"].(string)
	gt.Commit(t, co, "prd", map[string]string{"docs/prd/widgets.md": "# Widgets\n\n## US-01 See widgets\n"})
	env, code := w.as(co, "prd", "submit", "--path", "docs/prd/widgets.md", "--title", "Widgets")
	if code != 0 {
		t.Fatalf("prd submit = %d %v", code, env)
	}
	epic := env["data"].(map[string]any)["epic"].(string)
	w.docReview(epic, "reject")
	w.revise(epic)
	w.docReviewSays(epic, "reject", capFinding)
	return w, epic
}

// revise runs the reviser on the epic, which must be in prd_amending. Its
// push returns the PRD to review, as the GitHub reactor's prd_amended does,
// and then the run finishes.
func (w *humanWorld) revise(epic string) {
	w.t.Helper()
	ctx := context.Background()
	st, ref := w.srv.Sched.Dispatch(ctx, "alpha", machine.StageReviser)
	if ref != nil || st == nil || st.Item != epic {
		w.t.Fatalf("reviser dispatch = %+v %+v", st, ref)
	}
	if res := w.srv.Machine.Apply(ctx, machine.Request{Project: "alpha", Item: epic, Verb: "prd_amended",
		Actor: machine.Server("test")}); res.Refusal != nil {
		w.t.Fatalf("prd_amended: %+v", *res.Refusal)
	}
	if _, ref := w.srv.Sched.Finish(ctx, st.RunID, "done"); ref != nil {
		w.t.Fatalf("finish: %+v", ref)
	}
}

// capEscalation is the one escalation of the epic, which must wait for its
// panel.
func (w *humanWorld) capEscalation(epic string) escalation.Record {
	w.t.Helper()
	pending, err := w.srv.Escalations.Pending(context.Background(), "alpha")
	if err != nil {
		w.t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Item != epic {
		w.t.Fatalf("the escalations that wait for a panel = %+v", pending)
	}
	return pending[0]
}

// The second PRD reject opens one escalation for the triage panel, with
// the reject text as its finding, and the inbox entry waits on triage.
func TestSecondPRDRejectWaitsForTheTriagePanel(t *testing.T) {
	w, epic := prdAtCap(t)
	if got := w.show(epic).Status; got != machine.Escalated {
		t.Fatalf("after the second reject: %s", got)
	}
	rec := w.capEscalation(epic)
	if rec.Stage != machine.StageDocReviewer || rec.State != escalation.StateAwaitingPanel || !strings.Contains(rec.Finding, capFinding) {
		t.Fatalf("the escalation = %+v", rec)
	}
	triage := w.runToken("fix-1", "", "triage")
	env, code := w.as(triage, "work", "peek", "--stage", "triage")
	pending, _ := env["data"].(map[string]any)["escalations"].([]any)
	if code != 0 || len(pending) != 1 || fmt.Sprint(pending[0].(map[string]any)["escalation"]) != fmt.Sprint(rec.ID) {
		t.Fatalf("triage peek = %d %v", code, env)
	}
	if e := entryByID(w.entries("person"), "escalation/alpha/"+epic); e != nil {
		t.Fatalf("a person reads the cap before triage: %v", e)
	}
	if e := entryByID(w.entries("coordinator"), fmt.Sprintf("panel/%d", rec.ID)); e == nil || !strings.Contains(e["text"].(string), capFinding) {
		t.Fatalf("the panel entry = %v", e)
	}
}

// A loop route returns the PRD to the reviser once. The next reject is the
// first of a new count, and the reject after it reaches a person with no
// second escalation.
func TestLoopRouteReturnsTheCappedPRDToTheReviserOnce(t *testing.T) {
	w, epic := prdAtCap(t)
	id := fmt.Sprint(w.capEscalation(epic).ID)
	triage := w.runToken("fix-1", "", "triage")
	panelFile(t, triage, "derived", &escalation.Reading{Probabilities: map[string]float64{"0": 0, "1": 0, "2": 0.1, "3": 0.9}, Score: 2.9, Confidence: 0.95})
	env, code := w.as(triage, "escalation", "route", id, "--file", "panel.json")
	if code != 0 || env["data"].(map[string]any)["route"] != string(escalation.RouteLoop) {
		t.Fatalf("route = %d %v", code, env)
	}
	it := w.show(epic)
	if it.Status != machine.PRDAmending || it.Fact(machine.FactCapReturned) != id ||
		!strings.Contains(it.Fact(machine.FactNote), "panel routed this escalation back to the loop") {
		t.Fatalf("after the loop route: %s %v", it.Status, it.Facts)
	}
	if e := entryByID(w.entries(""), "escalation/alpha/"+epic); e != nil {
		t.Fatalf("the returned epic stays in the inbox: %v", e)
	}
	w.revise(epic)

	w.docReview(epic, "reject")
	if got := w.show(epic).Status; got != machine.PRDAmending {
		t.Fatalf("the first reject after the return: %s", got)
	}
	w.revise(epic)
	w.docReview(epic, "reject")
	if got := w.show(epic).Status; got != machine.Escalated {
		t.Fatalf("the second reject after the return: %s", got)
	}
	if n := w.count(`SELECT count(*) FROM escalations`); n != 1 {
		t.Fatalf("%d escalations, want only the first cap's", n)
	}
	e := entryByID(w.entries("person"), "escalation/alpha/"+epic)
	if e == nil || !strings.Contains(e["text"].(string), "brainstorm room") ||
		!strings.Contains(e["text"].(string), "already returned it to the reviser once") {
		t.Fatalf("the inbox entry = %v", e)
	}
}

// A person route leaves the epic escalated, and the inbox asks the
// operator for a brainstorm room. The answer returns the PRD to review.
func TestPersonRouteAsksForABrainstormRoom(t *testing.T) {
	w, epic := prdAtCap(t)
	id := fmt.Sprint(w.capEscalation(epic).ID)
	triage := w.runToken("fix-1", "", "triage")
	panelFile(t, triage, "preference", nil)
	env, code := w.as(triage, "escalation", "route", id, "--file", "panel.json")
	if code != 0 || env["data"].(map[string]any)["route"] != string(escalation.RoutePerson) {
		t.Fatalf("route = %d %v", code, env)
	}
	if it := w.show(epic); it.Status != machine.Escalated || it.Fact(machine.FactCapReturned) != "" {
		t.Fatalf("after the person route: %s %v", it.Status, it.Facts)
	}
	e := entryByID(w.entries("person"), "escalation/alpha/"+epic)
	if e == nil || !strings.Contains(e["text"].(string), "brainstorm room") || !strings.Contains(e["text"].(string), capFinding) {
		t.Fatalf("the inbox entry = %v", e)
	}
	if env, code := w.coordinator("inbox", "answer", "escalation/alpha/"+epic, "--body", "the room revised it"); code != 0 {
		t.Fatalf("answer = %d %v", code, env)
	}
	if got := w.show(epic).Status; got != machine.PRDReview {
		t.Fatalf("after the answer: %s", got)
	}
}
