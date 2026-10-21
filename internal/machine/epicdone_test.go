package machine

import (
	"context"
	"testing"
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
	w.must(canceled, "cancel", nil)
	if got := w.show(ep).Status; got != Done {
		t.Fatalf("the epic is %s after its last child finished", got)
	}
	if n := w.count(`SELECT count(*) FROM transitions WHERE item = ? AND verb = 'complete'`, ep); n != 1 {
		t.Fatalf("%d complete moves", n)
	}
}
