package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
)

// An escalation whose item reaches done before its panel never heads the
// fixer's queue: the server closes it, peek for the fixer offers the next
// escalation, and a route on the closed one answers already_routed (F-25).
func TestEscalationOnADoneItemLeavesTheFixerQueue(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	raise := func(run, item, body string) string {
		co := w.runToken(run, item, machine.StageImplementer)
		env, code := w.as(co, "escalation", "raise", "--score", "4", "--body", body)
		if code != 0 {
			t.Fatalf("raise = %d %v", code, env)
		}
		return fmt.Sprint(env["data"].(map[string]any)["escalation"].(map[string]any)["escalation"])
	}
	done, next := todoTicket(t, w.bx), todoTicket(t, w.bx)
	first := raise("run-1", done, "which of two layouts the owner prefers")
	second := raise("run-2", next, "which of two colours the owner prefers")

	res := w.srv.Machine.Apply(ctx, machine.Request{Project: "alpha", Item: done, Verb: "merged",
		Params: map[string]string{"sha": "6f04f00"}, Actor: machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("merged = %+v", res.Refusal)
	}
	if it := w.show(done); it.Status != machine.Done {
		t.Fatalf("item = %s", it.Status)
	}

	fixer := w.runToken("fix-1", "", "fixer")
	env, code := w.as(fixer, "work", "peek", "--stage", "fixer")
	pending, _ := env["data"].(map[string]any)["escalations"].([]any)
	if code != 0 || len(pending) == 0 || fmt.Sprint(pending[0].(map[string]any)["escalation"]) != second {
		t.Fatalf("fixer peek offers %v first, want escalation %s; escalation %s is on a done item: %d %v",
			pending, second, first, code, env)
	}
	panelFile(t, fixer, "preference", nil)
	env, code = w.as(fixer, "escalation", "route", first, "--file", "panel.json")
	if code != 3 || env["rule"] != "already_routed" {
		t.Fatalf("route on the done item's escalation = %d %v, want rule already_routed", code, env)
	}
}

// A ticket superseded while its escalation waits for the panel closes that
// escalation too: no holds row stays, the inbox drops its panel entry, and
// the fixer's queue holds only the next escalation (F-25).
func TestEscalationOnASupersededItemIsClosed(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	raise := func(run, item, body string) string {
		co := w.runToken(run, item, machine.StageImplementer)
		env, code := w.as(co, "escalation", "raise", "--score", "4", "--body", body)
		if code != 0 {
			t.Fatalf("raise = %d %v", code, env)
		}
		return fmt.Sprint(env["data"].(map[string]any)["escalation"].(map[string]any)["escalation"])
	}
	gone, next := todoTicket(t, w.bx), todoTicket(t, w.bx)
	first := raise("run-1", gone, "which of two layouts the owner prefers")
	second := raise("run-2", next, "which of two colours the owner prefers")

	res := w.srv.Machine.Apply(ctx, machine.Request{Project: "alpha", Item: gone, Verb: machine.VerbSupersede,
		Params: map[string]string{"evidence": machine.EvidenceCommit, "ref": "6f04f00", "reason": "main holds the change"},
		Actor:  machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("supersede = %+v", res.Refusal)
	}
	if it := w.show(gone); it.Status != machine.Canceled {
		t.Fatalf("item = %s", it.Status)
	}

	if n := w.count(`SELECT count(*) FROM escalations WHERE id = ? AND state = ?`, first, escalation.StateClosed); n != 1 {
		t.Fatalf("escalation %s is not closed", first)
	}
	if n := w.count(`SELECT count(*) FROM holds WHERE project = 'alpha' AND item = ?`, gone); n != 0 {
		t.Fatalf("%d holds rows stay on the canceled item %s", n, gone)
	}
	if e := entryByID(w.entries(cli.PrincipalCoordinator), "panel/"+first); e != nil {
		t.Fatalf("the inbox still shows panel/%s: %v", first, e)
	}
	fixer := w.runToken("fix-1", "", "fixer")
	env, code := w.as(fixer, "work", "peek", "--stage", "fixer")
	pending, _ := env["data"].(map[string]any)["escalations"].([]any)
	if code != 0 || len(pending) != 1 || fmt.Sprint(pending[0].(map[string]any)["escalation"]) != second {
		t.Fatalf("fixer peek offers %v, want escalation %s only: %d %v", pending, second, code, env)
	}
}

// A run that still holds its token after its item went terminal cannot
// raise an escalation on it: no panel could route one, so the server writes
// no row and no hold, and the fixer's queue stays empty (F-25).
func TestRaiseOnATerminalItemIsRefused(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	gone := todoTicket(t, w.bx)
	co := w.runToken("run-1", gone, machine.StageImplementer)

	res := w.srv.Machine.Apply(ctx, machine.Request{Project: "alpha", Item: gone, Verb: machine.VerbSupersede,
		Params: map[string]string{"evidence": machine.EvidenceCommit, "ref": "6f04f00", "reason": "main holds the change"},
		Actor:  machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("supersede = %+v", res.Refusal)
	}

	env, code := w.as(co, "escalation", "raise", "--score", "4", "--body", "which of two layouts the owner prefers")
	if code != 3 || env["code"] != cli.CodeTransitionNotAllowed || env["rule"] != "item_terminal" {
		t.Fatalf("raise on the canceled item = %d %v, want TRANSITION_NOT_ALLOWED rule item_terminal", code, env)
	}
	if n := w.count(`SELECT count(*) FROM escalations WHERE project = 'alpha' AND item = ?`, gone); n != 0 {
		t.Fatalf("%d escalations stand on the canceled item %s", n, gone)
	}
	if n := w.count(`SELECT count(*) FROM holds WHERE project = 'alpha' AND item = ?`, gone); n != 0 {
		t.Fatalf("%d holds rows stand on the canceled item %s", n, gone)
	}
	fixer := w.runToken("fix-1", "", "fixer")
	if env, code := w.as(fixer, "work", "peek", "--stage", "fixer"); code != 1 {
		t.Fatalf("fixer peek = %d %v, want nothing", code, env)
	}
}
