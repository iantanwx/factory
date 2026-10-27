package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// pendingBlock writes the fact pending_block on item, as a work block
// that crashed before its edge leaves it.
func (w *world) pendingBlock(item, on string) {
	w.t.Helper()
	is, err := w.bx.Get(context.Background(), item)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.bx.Update(context.Background(), item, beadsx.UpdateSpec{IfStatus: is.Status,
		SetMetadata: map[string]string{"loop." + machine.FactPendingBlock: on}}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) advance() {
	w.t.Helper()
	if err := w.s.advanceDependencies(context.Background(), prj); err != nil {
		w.t.Fatal(err)
	}
}

// C8: work block crashed after its move and before its edge, on a ticket
// that already held an edge to a done item. A dependency-advance tick
// holds the ticket, because pending_block names an open item.
func TestC8DependencyAdvanceHoldsACrashedBlock(t *testing.T) {
	w := newWorld(t)
	d := w.ticket(machine.Backlog)
	w.dependsOn(d, w.ticket(machine.Done), beadsx.DepBlocks)
	w.pendingBlock(d, w.ticket(machine.Todo))
	w.advance()
	if got := w.status(d); got != machine.Backlog {
		t.Fatalf("a ticket with an open pending blocker = %s, want backlog", got)
	}
}

// C9: a backlog ticket with no edge, whose pending_block names a done
// item, has a blocker and no open blocker, so the next tick frees it.
func TestC9DependencyAdvanceFreesAPendingBlockOnADoneItem(t *testing.T) {
	w := newWorld(t)
	d := w.ticket(machine.Backlog)
	w.pendingBlock(d, w.ticket(machine.Done))
	w.advance()
	if got := w.status(d); got != machine.Todo {
		t.Fatalf("a ticket whose pending blocker is done = %s, want todo", got)
	}
}

// C9: a ticket that work block parked waits while its blocker is open,
// and leaves backlog at the first tick after the blocker is done.
func TestC9DependencyAdvanceFreesATicketThatWorkBlockParked(t *testing.T) {
	w := newWorld(t)
	x := w.ticket(machine.InProgress)
	d := w.ticket(machine.Todo)
	v, _ := cli.Surface().Verb("work block")
	inv := cli.Invocation{Verb: v, Args: []string{d}, Flags: map[string]string{"by": x}, Extra: map[string]string{}}
	p := rpc.Principal{Kind: cli.PrincipalCoordinator, RunID: "coord", Project: prj}
	if res := w.m.Handlers()["work block"](context.Background(), &rpc.Call{Inv: inv, Principal: p}); res.Refusal != nil {
		t.Fatalf("work block: %+v", res.Refusal)
	}
	w.advance()
	if got := w.status(d); got != machine.Backlog {
		t.Fatalf("with the blocker open: %s, want backlog", got)
	}
	is, _ := w.bx.Get(context.Background(), x)
	if _, err := w.bx.Update(context.Background(), x, beadsx.UpdateSpec{IfStatus: is.Status, Status: machine.Done}); err != nil {
		t.Fatal(err)
	}
	w.advance()
	if got := w.status(d); got != machine.Todo {
		t.Fatalf("with the blocker done: %s, want todo", got)
	}
}
