package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
)

// C-bug-1 (F-30): when the stored incarnation is gone from a complete
// inventory, and exactly one live agent session of the coordinator's harness
// works in the coordinator's checkout, the next tick binds the doorbell to
// that session. A plain shell in the same checkout is not the coordinator.
// The server starts no second coordinator.
func TestCoordinatorRebindsToTheOneAgentInItsCheckoutWhenTheIncarnationIsGone(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	if err := w.s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	w.tick()
	first := w.s.Coordinator()
	if first.Session == nil {
		t.Fatalf("first coordinator = %+v", first)
	}

	w.host.Restart(false)
	moved, ok := w.host.CurrentID(first.Session.ID)
	if !ok || moved == first.Session.ID {
		t.Fatalf("fixture: the handle did not move: %+v", moved)
	}
	// The report: term_6d18c03a under incarnation c4ee55be and pty
	// 64d0518d, beside term_701b49ef, a shell with no agent. Neither
	// carries the stored incarnation f0a973d9 or pty 0ff24666.
	agent := agenthost.RunID{Host: moved.Host, Terminal: "term_6d18c03a", Incarnation: "c4ee55be"}
	shell := agenthost.RunID{Host: moved.Host, Terminal: "term_701b49ef", Incarnation: "8482f9e8"}
	w.cfg.Agents = editedInventory{w.host, func(inv agenthost.Inventory) agenthost.Inventory {
		for i, r := range inv.Rows {
			if r.ID == moved {
				inv.Rows[i].ID, inv.Rows[i].Pty = agent, "pty_64d0518d"
				inv.Rows = append(inv.Rows, agenthost.Row{ID: shell, Checkout: r.Checkout, Connected: true})
				break
			}
		}
		return inv
	}}
	w.rebuild()
	w.tick()
	w.tick()

	got := w.s.Coordinator()
	if got.Session == nil || got.RunID != first.RunID {
		t.Fatalf("the coordinator after the incarnation changed = %+v; first %+v", got, first)
	}
	if got.Session.ID != agent {
		t.Fatalf("stored handle = %+v, want the one agent session in the checkout %+v: the doorbell rings the dead handle", got.Session.ID, agent)
	}
	if !w.liveToken(first.RunID) {
		t.Fatalf("the rebind revoked the token of run %s", first.RunID)
	}
	if n := len(w.host.Launches()); n != 1 {
		t.Fatalf("launches = %d, want 1: the server started a second coordinator", n)
	}
}
