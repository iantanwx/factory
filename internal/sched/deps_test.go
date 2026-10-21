package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

func (w *world) dependsOn(item, on string, kind beadsx.DepKind) {
	w.t.Helper()
	if err := w.bx.AddDependency(context.Background(), item, on, kind); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) merge(item string) {
	w.t.Helper()
	res := w.m.MergeObserved(context.Background(), machine.MergeEvent{Project: prj, Item: item, Base: "main", Into: "main", SHA: "abc123"})
	if res.Refusal != nil {
		w.t.Fatalf("merge %s: %+v", item, res.Refusal)
	}
}

// The end-to-end path: a dependent ticket waits in backlog while its
// blocker is open. The blocker merges. The next dependency-advance tick
// moves the dependent to todo, and the next implementer tick admits it.
func TestBlockerMergeAdmitsTheDependentOnTheNextTick(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, map[string]string{"implementer": "3/5 * * * *", DependencyAdvanceJob: "*/5 * * * *"})
	blocker := w.ticket(machine.InReview)
	dependent := w.ticket(machine.Backlog)
	w.dependsOn(dependent, blocker, beadsx.DepBlocks)

	w.clock.Set(at(t, "2026-10-21T10:00:30Z"))
	w.tick() // the first sight sets each deadline after now
	w.clock.Set(at(t, "2026-10-21T10:03:30Z"))
	w.tick() // implementer: nothing in todo
	w.clock.Set(at(t, "2026-10-21T10:05:30Z"))
	w.tick() // dependency-advance: the blocker is open
	if got := w.status(dependent); got != machine.Backlog || len(w.host.Launches()) != 0 {
		t.Fatalf("with the blocker open: dependent %s, %d launches", got, len(w.host.Launches()))
	}
	if _, ref := w.a.Admit(context.Background(), admissionRequest(dependent)); ref == nil || ref.Code != cli.CodeDependencyOpen {
		t.Fatalf("admission of a backlog ticket = %+v; want %s", ref, cli.CodeDependencyOpen)
	}

	w.merge(blocker)
	w.clock.Set(at(t, "2026-10-21T10:08:30Z"))
	w.tick() // implementer: the merge alone moves nothing
	if got := w.status(dependent); got != machine.Backlog || len(w.host.Launches()) != 0 {
		t.Fatalf("before the job ticks: dependent %s, %d launches", got, len(w.host.Launches()))
	}
	w.clock.Set(at(t, "2026-10-21T10:10:30Z"))
	w.tick() // dependency-advance: every blocker is done
	if got := w.status(dependent); got != machine.Todo {
		t.Fatalf("after dependency-advance: dependent %s, want todo", got)
	}
	w.clock.Set(at(t, "2026-10-21T10:13:30Z"))
	w.tick() // implementer admits the dependent
	l := w.host.Launches()
	if len(l) != 1 || w.status(dependent) != machine.InProgress || w.lease(dependent).Owner == "" {
		t.Fatalf("after the implementer tick: %d launches, dependent %s, lease %+v", len(l), w.status(dependent), w.lease(dependent))
	}
}

func TestDependencyAdvance(t *testing.T) {
	ctx := context.Background()
	t.Run("every blocker done", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.ticket(machine.Done), w.ticket(machine.Done)
		d := w.ticket(machine.Backlog)
		w.dependsOn(d, a, beadsx.DepBlocks)
		w.dependsOn(d, b, beadsx.DepBlocks)
		if err := w.s.advanceDependencies(ctx, prj); err != nil {
			t.Fatal(err)
		}
		it, _ := w.m.Show(ctx, prj, d)
		if it.Status != machine.Todo || it.Fact(machine.FactEntry) != machine.EntryNew {
			t.Fatalf("dependent = %s entry %q; want todo, new", it.Status, it.Fact(machine.FactEntry))
		}
	})
	held := map[string]func(w *world, d string){
		"one blocker open": func(w *world, d string) {
			w.dependsOn(d, w.ticket(machine.Done), beadsx.DepBlocks)
			w.dependsOn(d, w.ticket(machine.InProgress), beadsx.DepBlocks)
		},
		// A canceled blocker never landed. A person decides with
		// `work unblock`.
		"canceled blocker": func(w *world, d string) {
			w.dependsOn(d, w.ticket(machine.Canceled), beadsx.DepBlocks)
		},
		// decompose adds every edge before the create that puts a ticket
		// in backlog, so no edge is a doubt, and a doubt holds.
		"no blocking edge": func(w *world, d string) {
			w.dependsOn(d, w.ticket(machine.Done), beadsx.DepRelated)
		},
	}
	for name, setup := range held {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			d := w.ticket(machine.Backlog)
			setup(w, d)
			if err := w.s.advanceDependencies(ctx, prj); err != nil {
				t.Fatal(err)
			}
			if got := w.status(d); got != machine.Backlog {
				t.Fatalf("dependent = %s, want backlog", got)
			}
		})
	}
	t.Run("an edge of another kind is no blocker", func(t *testing.T) {
		w := newWorld(t)
		d := w.ticket(machine.Backlog)
		w.dependsOn(d, w.ticket(machine.Done), beadsx.DepBlocks)
		w.dependsOn(d, w.ticket(machine.Todo), beadsx.DepRelated)
		if err := w.s.advanceDependencies(ctx, prj); err != nil {
			t.Fatal(err)
		}
		if got := w.status(d); got != machine.Todo {
			t.Fatalf("dependent = %s, want todo", got)
		}
	})
	t.Run("beads down moves nothing", func(t *testing.T) {
		w := newWorld(t)
		d := w.ticket(machine.Backlog)
		w.dependsOn(d, w.ticket(machine.Done), beadsx.DepBlocks)
		w.bx.SetUnavailable(true)
		err := w.s.advanceDependencies(ctx, prj)
		w.bx.SetUnavailable(false)
		if err == nil {
			t.Fatal("a tick with beads down reported no error")
		}
		if got := w.status(d); got != machine.Backlog {
			t.Fatalf("dependent = %s, want backlog", got)
		}
	})
}

// The scheduler registers dependency-advance itself when it can read
// beads, so the server registers nothing for it.
func TestDependencyAdvanceRegistersWithBeads(t *testing.T) {
	w := newWorld(t)
	if w.s.job(DependencyAdvanceJob) == nil {
		t.Fatal("dependency-advance is not registered")
	}
	if err := w.s.RegisterJob(DependencyAdvanceJob, func(context.Context, string) error { return nil }); err == nil {
		t.Fatal("a second registration was accepted")
	}
	w.cfg.Beads = nil
	w.rebuild()
	if w.s.job(DependencyAdvanceJob) != nil {
		t.Fatal("dependency-advance is registered with no beads reader")
	}
}
