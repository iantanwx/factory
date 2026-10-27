package machine

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// The plan's epic table moves an epic in execution to done when every
// child is terminal. The last child to finish closes its epic, whether it
// merged or was canceled, and an epic with an open child stays.
func TestTheLastTerminalChildClosesItsEpic(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.create(KindEpic, "create_architecture", nil)
	w.force(ep, Execution, nil)
	child := func() string {
		id, res := w.m.Create(ctx, CreateRequest{Project: prj, Kind: KindTicket, Title: "c", Verb: "create",
			Actor: Server("test"), Facts: map[string]string{FactEpic: ep}})
		mustOK(t, res)
		return id
	}
	merged, canceled := child(), child()

	mustOK(t, w.m.MergeObserved(ctx, MergeEvent{Project: prj, Item: merged, Base: "main", Into: "main", SHA: "abc"}))
	if got := w.show(ep).Status; got != Execution {
		t.Fatalf("the epic is %s with one open child", got)
	}

	w.force(canceled, Escalated, nil)
	w.must(canceled, "cancel", map[string]string{"reason": "dropped"})
	if got := w.show(ep).Status; got != Done {
		t.Fatalf("the epic is %s after its last child finished", got)
	}
	if n := w.count(`SELECT count(*) FROM transitions WHERE item = ? AND verb = 'complete'`, ep); n != 1 {
		t.Fatalf("%d complete moves", n)
	}
}

// runEpicCompleteKill stops the machine between a child's terminal write
// and its epic's complete write, as a crash there would. The epic stays
// in execution until the restart, and the start-up reconcile completes
// it exactly once. An epic in execution with an open child stays.
func runEpicCompleteKill(t *testing.T, mk func(t *testing.T) *world) {
	w := mk(t)
	ctx := context.Background()
	child := func(ep string) string {
		id, res := w.m.Create(ctx, CreateRequest{Project: prj, Kind: KindTicket, Title: "c", Verb: "create",
			Actor: Server("test"), Facts: map[string]string{FactEpic: ep}})
		mustOK(t, res)
		return id
	}
	ep, held := w.create(KindEpic, "create_architecture", nil), w.create(KindEpic, "create_architecture", nil)
	w.force(ep, Execution, nil)
	w.force(held, Execution, nil)
	last := child(ep)
	child(held)

	w.stopAt(pointChildTerminal)
	mustOK(t, w.m.MergeObserved(ctx, MergeEvent{Project: prj, Item: last, Base: "main", Into: "main", SHA: "abc"}))
	if got := w.show(last).Status; got != Done {
		t.Fatalf("the child is %s after its merge", got)
	}
	if got := w.show(ep).Status; got != Execution {
		t.Fatalf("the epic is %s; the kill point did not stop the second write", got)
	}
	for range 2 {
		w.restart()
		if got := w.show(ep).Status; got != Done {
			t.Fatalf("after reconcile the epic is %s, want done", got)
		}
		if n := w.count(`SELECT count(*) FROM transitions WHERE item = ? AND verb = 'complete'`, ep); n != 1 {
			t.Fatalf("%d complete moves", n)
		}
		if got := w.show(held).Status; got != Execution {
			t.Fatalf("the epic with an open child is %s", got)
		}
	}
}

func TestEpicCompleteSurvivesACrashBetweenTheTwoWrites(t *testing.T) {
	runEpicCompleteKill(t, newWorld)
}

// CompleteEpics also runs while the server serves: an epic whose complete
// write failed after its last child finished closes on the next sweep.
// An epic in execution with no child is no finished epic, and stays.
func TestCompleteEpicsClosesAFinishedEpic(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep, empty := w.create(KindEpic, "create_architecture", nil), w.create(KindEpic, "create_architecture", nil)
	w.force(ep, Execution, nil)
	w.force(empty, Execution, nil)
	id, res := w.m.Create(ctx, CreateRequest{Project: prj, Kind: KindTicket, Title: "c", Verb: "create",
		Actor: Server("test"), Facts: map[string]string{FactEpic: ep}})
	mustOK(t, res)
	w.stopAt(pointChildTerminal)
	mustOK(t, w.m.MergeObserved(ctx, MergeEvent{Project: prj, Item: id, Base: "main", Into: "main", SHA: "abc"}))
	w.m.hook = nil
	if err := w.m.CompleteEpics(ctx, prj); err != nil {
		t.Fatal(err)
	}
	if got := w.show(ep).Status; got != Done {
		t.Fatalf("the finished epic is %s", got)
	}
	if got := w.show(empty).Status; got != Execution {
		t.Fatalf("the epic with no child is %s", got)
	}
}

// C16: the architecture entry and complete read one open-child rule. For
// each set of children, OpenChildren names exactly the children that make
// complete refuse with children_open.
func TestOpenChildrenNamesEachChildThatRefusesComplete(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.create(KindEpic, "create_architecture", nil)
	w.force(ep, Execution, nil)
	child := func() string {
		id, res := w.m.Create(ctx, CreateRequest{Project: prj, Kind: KindTicket, Title: "c", Verb: "create",
			Actor: Server("test"), Facts: map[string]string{FactEpic: ep}})
		mustOK(t, res)
		return id
	}
	merged, held, open := child(), child(), child()
	w.create(KindTicket, "create", nil) // no child of ep
	mustOK(t, w.m.MergeObserved(ctx, MergeEvent{Project: prj, Item: merged, Base: "main", Into: "main", SHA: "abc"}))
	w.force(held, Escalated, nil)

	got, ref := w.m.OpenChildren(ctx, prj, ep)
	if ref != nil {
		t.Fatalf("OpenChildren refused: %+v", ref)
	}
	if want := []string{held, open}; !slices.Equal(got, want) {
		t.Fatalf("OpenChildren = %v, want %v", got, want)
	}
	r := wantCode(t, w.apply(ep, "complete", nil), cli.CodeDependencyOpen)
	if r.Rule != "children_open" || !strings.Contains(r.Hint, held+" ("+Escalated+")") || !strings.Contains(r.Hint, open+" ("+Todo+")") ||
		strings.Contains(r.Hint, merged) {
		t.Fatalf("complete refused %+v; want each open child and no other", r)
	}

	w.must(held, "cancel", map[string]string{"reason": "dropped"})
	if got, _ := w.m.OpenChildren(ctx, prj, ep); !slices.Equal(got, []string{open}) {
		t.Fatalf("OpenChildren after the cancel = %v, want [%s]", got, open)
	}
	w.must(open, "escalate", nil)
	w.must(open, "cancel", map[string]string{"reason": "dropped"})
	if got, _ := w.m.OpenChildren(ctx, prj, ep); len(got) != 0 {
		t.Fatalf("OpenChildren with every child terminal = %v", got)
	}
	if got := w.show(ep).Status; got != Done {
		t.Fatalf("complete left the epic in %s with no open child", got)
	}
}
