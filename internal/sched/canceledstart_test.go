package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/machine"
)

// cancelOnCheckout cancels the epic while the run's start makes its
// checkout, as the coordinator's cancel did between admission and the
// session start of r-fc6aee663e41 (F-59).
type cancelOnCheckout struct {
	*gitCheckouts
	w    *world
	epic string
}

func (c *cancelOnCheckout) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	res := c.w.m.Apply(ctx, machine.Request{Project: prj, Item: c.epic, Verb: "cancel", Command: "work cancel",
		Actor: machine.Actor{Name: "coordinator"}, Params: map[string]string{"reason": "the work moved"}})
	if res.Refusal != nil {
		c.w.t.Fatalf("cancel: %+v", res.Refusal)
	}
	return c.gitCheckouts.CreateCheckout(ctx, spec)
}

// C-bug-1: an epic that the coordinator cancels after admission claimed
// it, and before its spec-author session starts, gets no session. The run
// ends, and the spec-author mutex is free again.
func TestASpecAuthorRunOnAnEpicCanceledDuringItsStartStartsNoSession(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.project.set(snapshot(t, "stages:\n  spec-author: {}\nmerge:\n  mechanism: direct\n", ""))
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "Let a reader save a search", Description: "From the operator.", Type: "epic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: machine.SpecNeeded,
		SetMetadata: map[string]string{"loop." + machine.FactPRDMerged: "abc123"}}); err != nil {
		t.Fatal(err)
	}
	w.cfg.Checkouts = func(string) CheckoutHost { return &cancelOnCheckout{gitCheckouts: w.co, w: w, epic: is.ID} }
	w.rebuild()

	_, _ = w.s.Dispatch(ctx, prj, machine.StageSpecAuthor)

	if got := w.status(is.ID); got != machine.Canceled {
		t.Fatalf("the epic is in %s, want canceled", got)
	}
	if n := len(w.host.Launches()); n != 0 {
		t.Fatalf("%d sessions started on the canceled epic %s, want 0", n, is.ID)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE item = ? AND ended_at IS NULL`, is.ID); n != 0 {
		t.Fatalf("%d runs on the canceled epic %s still live, want 0", n, is.ID)
	}
	if n := w.count(`SELECT count(*) FROM stage_mutexes WHERE project = ? AND stage = ?`, prj, machine.StageSpecAuthor); n != 0 {
		t.Fatalf("the spec-author mutex is still held after the cancel")
	}
}
