package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// A fixer run that the scheduler admitted asks peek for its own work. Its
// own live run fills the fixer cap of 1, and that must not hide the
// escalation it was started for (factorio-4zx, loop fault F-4).
func TestFixerPeekInsideAnAdmittedFixerRunListsTheWaitingEscalation(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	item := todoTicket(t, w.bx)
	co := w.runToken("run-1", item, machine.StageImplementer)
	env, code := w.as(co, "escalation", "raise", "--score", "4", "--body", "which of two layouts the owner prefers")
	if code != 0 {
		t.Fatalf("raise = %d %v", code, env)
	}
	id := fmt.Sprint(env["data"].(map[string]any)["escalation"].(map[string]any)["escalation"])

	// Before any fixer run is live, the scheduler's peek sees the work.
	if c, ref := w.srv.Admission.Peek(ctx, "alpha", config.StageFixer); c == nil || ref != nil {
		t.Fatalf("peek before the fixer starts = %+v %+v", c, ref)
	}
	// The scheduler admits the fixer run, as dispatch does: a live run row.
	ad, ref := w.srv.Admission.Admit(ctx, admission.Request{Project: "alpha", Stage: config.StageFixer,
		RunID: "fix-1", Harness: string(agenthost.Claude), Model: "opus"})
	if ref != nil {
		t.Fatalf("admit the fixer = %+v", ref)
	}
	fixer := w.runToken(ad.RunID, "", config.StageFixer)

	env, code = w.as(fixer, "work", "peek", "--stage", "fixer")
	data, _ := env["data"].(map[string]any)
	pending, _ := data["escalations"].([]any)
	if code != 0 || len(pending) != 1 || fmt.Sprint(pending[0].(map[string]any)["escalation"]) != id {
		t.Fatalf("peek inside the admitted fixer run = %d %v, want escalation %s", code, env, id)
	}
}
