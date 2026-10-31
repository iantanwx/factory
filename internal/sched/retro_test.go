package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/retro"
	"github.com/iantanwx/loop/internal/store"
)

// retroWorld is a world whose project runs the retro stage.
func retroWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.project.set(snapshot(t, "stages:\n  retro: {}\nmerge:\n  mechanism: direct\n", ""))
	return w
}

// doneEpic makes an epic in done and queues its retro, as the machine's
// Terminal hook does when the epic reaches done.
func (w *world) doneEpic() string {
	w.t.Helper()
	ctx := context.Background()
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "Let a reader save a search", Description: "From the operator.", Type: "epic"})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: machine.Done}); err != nil {
		w.t.Fatal(err)
	}
	if err := w.st.Write(ctx, func(tx *store.Tx) error {
		return retro.Enqueue(tx, machine.TerminalEvent{Project: prj, Item: is.ID, Kind: machine.KindEpic, Status: machine.Done})
	}); err != nil {
		w.t.Fatal(err)
	}
	return is.ID
}

// bindRetro runs the first `loop retro record` of run: it binds the
// project's oldest pending retro.
func (w *world) bindRetro(run string) retro.Retro {
	w.t.Helper()
	r, ok, err := retro.Bind(context.Background(), w.st, prj, run)
	if err != nil || !ok {
		w.t.Fatalf("bind a retro to %s: ok %v, err %v", run, ok, err)
	}
	return r
}

func (w *world) retroState(item string) (state string, attempts int) {
	return w.str(`SELECT state FROM retros WHERE project = ? AND item = ?`, prj, item),
		w.count(`SELECT attempts FROM retros WHERE project = ? AND item = ?`, prj, item)
}

// Admission starts one retro run for a project with a pending retro, and
// none for a project without one. The cap is 1, so a second pending retro
// waits for the first run.
func TestRetroStartsOnlyWhileARetroIsPending(t *testing.T) {
	ctx := context.Background()
	w := retroWorld(t)
	if st, ref := w.s.Dispatch(ctx, prj, retro.Stage); ref != nil || st != nil {
		t.Fatalf("with no pending retro, dispatch started %+v, refused %+v", st, ref)
	}
	w.doneEpic()
	w.doneEpic()
	st := w.dispatch(retro.Stage)
	if st.Item != "" {
		t.Fatalf("the retro run claimed item %s; a retro run claims none", st.Item)
	}
	if st, ref := w.s.Dispatch(ctx, prj, retro.Stage); ref != nil || st != nil {
		t.Fatalf("with one retro run live, dispatch started %+v, refused %+v; want none at cap 1", st, ref)
	}
}

// C30: a retro run that finishes with its retro still running returns it
// to pending with one attempt. The second such run marks it failed and
// raises one retro_failed alert, and admission starts no third run.
func TestC30ARunThatEndsWithNoRouteReleasesItsRetro(t *testing.T) {
	ctx := context.Background()
	w := retroWorld(t)
	epic := w.doneEpic()
	want := []struct {
		state    string
		attempts int
	}{{retro.StatePending, 1}, {"failed", 2}}
	for i, outcome := range []string{"done", "failed"} {
		st := w.dispatch(retro.Stage)
		w.bindRetro(st.RunID)
		if _, ref := w.s.Finish(ctx, st.RunID, outcome); ref != nil {
			t.Fatal(ref)
		}
		if state, n := w.retroState(epic); state != want[i].state || n != want[i].attempts {
			t.Fatalf("after run %d ended %s with no route: retro %s with %d attempts, want %s with %d",
				i+1, outcome, state, n, want[i].state, want[i].attempts)
		}
	}
	if n := w.alerts("retro_failed"); n != 1 {
		t.Fatalf("%d retro_failed alerts, want 1", n)
	}
	if st, ref := w.s.Dispatch(ctx, prj, retro.Stage); ref != nil || st != nil {
		t.Fatalf("with the one retro failed, dispatch started %+v, refused %+v", st, ref)
	}
}

// C30: a run that routed its retro changes nothing at finish.
func TestC30ARunThatRoutedChangesNothingAtFinish(t *testing.T) {
	ctx := context.Background()
	w := retroWorld(t)
	epic := w.doneEpic()
	st := w.dispatch(retro.Stage)
	w.bindRetro(st.RunID)
	// The runbook's empty panel file: the run found no learning.
	if _, ref := retro.RouteAll(ctx, retro.World{Store: w.st, Machine: w.m}, st.RunID, nil); ref != nil {
		t.Fatalf("route the empty panel file: %+v", ref)
	}
	if _, ref := w.s.Finish(ctx, st.RunID, "done"); ref != nil {
		t.Fatal(ref)
	}
	if state, n := w.retroState(epic); state != retro.StateDone || n != 0 {
		t.Fatalf("after a routed run finished: retro %s with %d attempts, want done with 0", state, n)
	}
	if n := w.alerts("retro_failed"); n != 0 {
		t.Fatalf("%d retro_failed alerts after a routed run", n)
	}
}
