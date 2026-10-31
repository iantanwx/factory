package server

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
)

// decisionsOf reads `loop escalation decisions <id>` as the triage run, and
// returns each decision's source and text.
func (w *humanWorld) decisionsOf(triage, id string) [][2]string {
	w.t.Helper()
	env, code := w.as(triage, "escalation", "decisions", id)
	if code != 0 {
		w.t.Fatalf("decisions = %d %v", code, env)
	}
	var out [][2]string
	for _, d := range env["data"].(map[string]any)["decisions"].([]any) {
		m := d.(map[string]any)
		out = append(out, [2]string{m["source"].(string), m["text"].(string)})
	}
	return out
}

// `loop escalation decisions` lists the operator's decisions recorded on
// the escalation's item, oldest first: the coordinator's notes, each
// answered question the coordinator prepared about the item, and each
// inbox answer on it. A run's own note, and a question about another item,
// are no decision of the operator (F-26).
func TestEscalationDecisionsListsTheOperatorsDecisionsOnTheItem(t *testing.T) {
	w := newHumanWorld(t, "")
	item := todoTicket(t, w.bx)
	co := w.runToken("run-1", item, machine.StageImplementer)
	if _, code := w.as(co, "escalation", "raise", "--score", "2", "--body", "the run's own reading"); code != 3 {
		t.Fatalf("level 2 raise = %d", code)
	}
	const note = "Operator, 2026-10-24, on F1: keep the 14-day regression window, and do not ask again."
	if env, code := w.coordinator("work", "note", item, "--project", "alpha", "--body", note); code != 0 {
		t.Fatalf("note = %d %v", code, env)
	}
	// The inbox keeps the test's clock, and the item's history the wall
	// clock. Move the inbox past the note.
	w.clock.Advance(time.Minute)
	for about, answer := range map[string]string{"escalation/alpha/" + item: "the left layout", "escalation/alpha/other-1": "another item's answer", "": "no item"} {
		args := []string{"inbox", "prepare", "--body", "Which layout? (" + about + ")"}
		if about != "" {
			args = append(args, "--entry", about)
		}
		env, code := w.coordinator(args...)
		if code != 0 {
			t.Fatalf("prepare = %d %v", code, env)
		}
		entry := env["data"].(map[string]any)["entry"].(string)
		if env, code := w.coordinator("inbox", "answer", entry, "--body", answer); code != 0 {
			t.Fatalf("answer %s = %d %v", entry, code, env)
		}
	}
	env, code := w.as(co, "escalation", "raise", "--score", "4", "--body", "which of two layouts the owner prefers")
	if code != 0 {
		t.Fatalf("raise = %d %v", code, env)
	}
	id := fmt.Sprint(env["data"].(map[string]any)["escalation"].(map[string]any)["escalation"])

	if env, _ := w.as(co, "escalation", "decisions", id); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("a non-triage read = %v", env)
	}
	triage := w.runToken("fix-1", "", "triage")
	if env, _ := w.as(triage, "escalation", "decisions", "999"); env["code"] != cli.CodeNotFound {
		t.Fatalf("an unknown escalation = %v", env)
	}
	got := w.decisionsOf(triage, id)
	if len(got) != 2 || got[0] != [2]string{escalation.SourceNote, note} || got[1][0] != escalation.SourceQuestion ||
		!strings.Contains(got[1][1], "Which layout? (escalation/alpha/"+item+")") || !strings.Contains(got[1][1], "the left layout") {
		t.Fatalf("decisions = %q", got)
	}

	panelFile(t, triage, "preference", nil)
	if _, code := w.as(triage, "escalation", "route", id, "--file", "panel.json"); code != 0 {
		t.Fatal("route failed")
	}
	if env, code := w.coordinator("inbox", "answer", "escalation/alpha/"+item, "--body", "use the left layout"); code != 0 {
		t.Fatalf("answer = %d %v", code, env)
	}
	got = w.decisionsOf(triage, id)
	if len(got) != 3 || !slices.Contains(got, [2]string{escalation.SourceAnswer, "use the left layout"}) {
		t.Fatalf("decisions after the answer = %q", got)
	}
}
