package machine

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

// ticketIn makes a ticket and puts it in status.
func (w *world) ticketIn(status string) string {
	w.t.Helper()
	id := w.create(KindTicket, "create", nil)
	if status != Todo {
		w.force(id, status, nil)
	}
	return id
}

// edge adds a blocks edge from item to on, as decompose writes it.
func (w *world) edge(item, on string) {
	w.t.Helper()
	if err := w.bc.AddDependency(context.Background(), item, on, beadsx.DepBlocks); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) block(item, by string) cli.Result {
	w.t.Helper()
	return w.call("work block", coord(), []string{item}, map[string]string{"by": by})
}

func (w *world) unblock(item, by string) cli.Result {
	w.t.Helper()
	flags := map[string]string{}
	if by != "" {
		flags["by"] = by
	}
	return w.call("work unblock", coord(), []string{item}, flags)
}

func (w *world) wantState(id, status string, edges ...string) {
	w.t.Helper()
	if got := w.show(id).Status; got != status {
		w.t.Fatalf("%s is in %s, want %s", id, got, status)
	}
	if got := w.edges(id); !slices.Equal(got, edges) {
		w.t.Fatalf("%s waits on %v, want %v", id, got, edges)
	}
}

// C5: work block parks a todo ticket in backlog with the new edge.
func TestC5WorkBlockParksATodoTicket(t *testing.T) {
	w := newWorld(t)
	x := w.ticketIn(InProgress)
	tk := w.ticketIn(Todo)
	mustOK(t, w.block(tk, x))
	w.wantState(tk, Backlog, x)
	if got := w.show(tk).Fact(FactPendingBlock); got != x {
		t.Fatalf("pending_block = %q, want %s", got, x)
	}
}

// C5: work block on a backlog ticket keeps backlog and adds the edge. A
// decision blocks too.
func TestC5WorkBlockAddsAnEdgeToABacklogTicket(t *testing.T) {
	w := newWorld(t)
	y := w.ticketIn(Todo)
	d := w.create(KindDecision, "file", nil)
	tk := w.ticketIn(Backlog)
	w.edge(tk, y)
	mustOK(t, w.block(tk, d))
	w.wantState(tk, Backlog, y, d)
}

// C5: from every other state work block refuses with
// TRANSITION_NOT_ALLOWED and writes no edge.
func TestC5WorkBlockRefusesEveryOtherState(t *testing.T) {
	w := newWorld(t)
	x := w.ticketIn(Todo)
	for _, s := range []string{InProgress, InReview, Repair, ReadyToMerge, Queued, Escalated, Done, Canceled} {
		tk := w.ticketIn(s)
		wantCode(t, w.block(tk, x), cli.CodeTransitionNotAllowed)
		w.wantState(tk, s)
		if got := w.show(tk).Fact(FactPendingBlock); got != "" {
			t.Fatalf("a refused block from %s wrote pending_block %q", s, got)
		}
	}
}

// C2 for work block: each bad blocker is refused before any write. The
// ticket keeps its state and its edges.
func TestC2WorkBlockRefusesABadBlockerBeforeAnyWrite(t *testing.T) {
	w := newWorld(t)
	epic := w.create(KindEpic, "create_architecture", nil)
	bug := w.create(KindBug, "file", nil)
	done := w.ticketIn(Done)
	canceled := w.create(KindDecision, "file", nil)
	w.force(canceled, Canceled, nil)
	tk := w.ticketIn(Todo)
	loop := w.ticketIn(Backlog)
	w.edge(loop, tk)
	for _, c := range []struct {
		name, blocker, code, rule string
	}{
		{"unknown", "tk-404", cli.CodeNotFound, "no_item"},
		{"itself", tk, cli.CodeUsage, "blocker_self"},
		{"an epic", epic, cli.CodeUsage, "blocker_epic"},
		{"a bug", bug, cli.CodeUsage, "blocker_bug"},
		{"a done ticket", done, cli.CodeUsage, "blocker_terminal"},
		{"a canceled decision", canceled, cli.CodeUsage, "blocker_terminal"},
		{"a cycle", loop, cli.CodeUsage, "blocker_cycle"},
	} {
		r := wantCode(t, w.block(tk, c.blocker), c.code)
		if r.Rule != c.rule {
			t.Errorf("%s: rule %q, want %q", c.name, r.Rule, c.rule)
		}
	}
	w.wantState(tk, Todo)
	if n := len(w.history(tk)); n != 1 {
		t.Fatalf("refused blocks wrote %d transitions after the create", n-1)
	}
}

// C6: work unblock --by deletes the one edge, and the ticket stays in
// backlog while another open edge remains.
func TestC6WorkUnblockByDeletesOneOfTwoOpenEdges(t *testing.T) {
	w := newWorld(t)
	x, y := w.ticketIn(Todo), w.ticketIn(InReview)
	tk := w.ticketIn(Backlog)
	w.edge(tk, x)
	w.edge(tk, y)
	res := w.unblock(tk, x)
	mustOK(t, res)
	if got := res.Data.(UnblockResult); !got.EdgeRemoved || got.Status != Backlog {
		t.Fatalf("unblock answered %+v", got)
	}
	w.wantState(tk, Backlog, y)
}

// C6: when the last open edge goes, the ticket moves to todo, and its
// done edges stay.
func TestC6WorkUnblockByTheLastOpenEdgeFreesTheTicket(t *testing.T) {
	w := newWorld(t)
	x, d := w.ticketIn(Todo), w.ticketIn(Done)
	tk := w.ticketIn(Backlog)
	w.edge(tk, d)
	w.edge(tk, x)
	mustOK(t, w.unblock(tk, x))
	w.wantState(tk, Todo, d)
	if got := w.show(tk).Fact(FactEntry); got != EntryNew {
		t.Fatalf("entry = %q, want %s", got, EntryNew)
	}
}

// C6: an edge to a canceled item is an open blocker, so the ticket stays.
func TestC6WorkUnblockByKeepsBacklogWithACanceledEdgeLeft(t *testing.T) {
	w := newWorld(t)
	x, c := w.ticketIn(Todo), w.ticketIn(Canceled)
	tk := w.ticketIn(Backlog)
	w.edge(tk, x)
	w.edge(tk, c)
	mustOK(t, w.unblock(tk, x))
	w.wantState(tk, Backlog, c)
}

// C6: an --by that names no edge of a backlog ticket deletes nothing and
// runs the same move step.
func TestC6WorkUnblockByWithNoEdgeOnABacklogTicket(t *testing.T) {
	t.Run("an open blocker remains", func(t *testing.T) {
		w := newWorld(t)
		x, y := w.ticketIn(Todo), w.ticketIn(Todo)
		tk := w.ticketIn(Backlog)
		w.edge(tk, y)
		res := w.unblock(tk, x)
		mustOK(t, res)
		if res.Data.(UnblockResult).EdgeRemoved {
			t.Fatal("unblock reports a removed edge")
		}
		w.wantState(tk, Backlog, y)
	})
	t.Run("no open blocker remains", func(t *testing.T) {
		w := newWorld(t)
		x, d := w.ticketIn(Todo), w.ticketIn(Done)
		tk := w.ticketIn(Backlog)
		w.edge(tk, d)
		mustOK(t, w.unblock(tk, x))
		w.wantState(tk, Todo, d)
	})
}

// C6: outside backlog, an --by that names no edge of a todo ticket
// writes nothing and exits 0. Every other state refuses with
// TRANSITION_NOT_ALLOWED and keeps its edge.
func TestC6WorkUnblockByOutsideBacklog(t *testing.T) {
	w := newWorld(t)
	x := w.ticketIn(Done)
	tk := w.ticketIn(Todo)
	res := w.unblock(tk, x)
	mustOK(t, res)
	if got := res.Data.(UnblockResult); got.EdgeRemoved || got.Status != Todo {
		t.Fatalf("unblock answered %+v", got)
	}
	if n := len(w.history(tk)); n != 1 {
		t.Fatalf("unblock on todo wrote %d transitions", n-1)
	}
	for _, s := range []string{InProgress, InReview, Repair, ReadyToMerge, Queued, Escalated, Done, Canceled} {
		tk := w.ticketIn(s)
		w.edge(tk, x)
		wantCode(t, w.unblock(tk, x), cli.CodeTransitionNotAllowed)
		w.wantState(tk, s, x)
	}
}

// The case PR #48 left open: a todo ticket that still holds an edge to a
// done item. unblock --by deletes that edge and keeps todo, because the
// ticket is already free. It writes no transition.
func TestWorkUnblockByDeletesADoneEdgeOfATodoTicket(t *testing.T) {
	w := newWorld(t)
	d := w.ticketIn(Done)
	tk := w.ticketIn(Todo)
	w.edge(tk, d)
	res := w.unblock(tk, d)
	mustOK(t, res)
	if got := res.Data.(UnblockResult); !got.EdgeRemoved || got.Status != Todo {
		t.Fatalf("unblock answered %+v", got)
	}
	w.wantState(tk, Todo)
	if n := len(w.history(tk)); n != 1 {
		t.Fatalf("unblock on todo wrote %d transitions", n-1)
	}
}

// C6: a ticket that work block parked leaves backlog at work unblock --by
// the same blocker, with no pending_block left.
func TestC6WorkUnblockByEndsABlock(t *testing.T) {
	w := newWorld(t)
	x := w.ticketIn(Todo)
	tk := w.ticketIn(Todo)
	mustOK(t, w.block(tk, x))
	mustOK(t, w.unblock(tk, x))
	w.wantState(tk, Todo)
	if got := w.show(tk).Fact(FactPendingBlock); got != "" {
		t.Fatalf("pending_block = %q after unblock", got)
	}
}

// C6: work block crashed before its edge, and its blocker was then
// canceled. work unblock --by that blocker frees the ticket.
func TestC6WorkUnblockByEndsACrashedBlockOnACanceledBlocker(t *testing.T) {
	w := newWorld(t)
	x := w.ticketIn(Todo)
	tk := w.ticketIn(Todo)
	w.m.hook = func(p string) bool { return p == pointBlockMoved }
	wantCode(t, w.block(tk, x), cli.CodeInternal)
	w.m.hook = nil
	w.force(x, Canceled, nil)
	mustOK(t, w.unblock(tk, x))
	w.wantState(tk, Todo)
	if got := w.show(tk).Fact(FactPendingBlock); got != "" {
		t.Fatalf("pending_block = %q after unblock", got)
	}
}

// C7: with no --by, work unblock frees a backlog ticket with no edge, or
// whose every edge leads to a done item, and deletes no edge. It refuses
// a ticket with an open blocker, an edge to a canceled item among them,
// and the hint names each one.
func TestC7WorkUnblockFreesOnlyATicketWithNoOpenBlocker(t *testing.T) {
	w := newWorld(t)
	x, c, d, e := w.ticketIn(Todo), w.ticketIn(Canceled), w.ticketIn(Done), w.ticketIn(Done)
	for name, edges := range map[string][]string{"no edge": nil, "only done edges": {d, e}} {
		tk := w.ticketIn(Backlog)
		for _, on := range edges {
			w.edge(tk, on)
		}
		if res := w.unblock(tk, ""); res.Refusal != nil {
			t.Fatalf("%s: %+v", name, res.Refusal)
		}
		w.wantState(tk, Todo, edges...)
	}
	for name, edges := range map[string][]string{"an open edge": {x, d}, "a canceled edge": {c, d}, "both": {x, c}} {
		tk := w.ticketIn(Backlog)
		for _, on := range edges {
			w.edge(tk, on)
		}
		r := wantCode(t, w.unblock(tk, ""), cli.CodeUsage)
		for _, on := range edges {
			if on != d && !strings.Contains(r.Hint, on) {
				t.Errorf("%s: hint %q names no %s", name, r.Hint, on)
			}
		}
		if strings.Contains(r.Hint, d) {
			t.Errorf("%s: hint %q names the done blocker %s", name, r.Hint, d)
		}
		w.wantState(tk, Backlog, edges...)
	}
}

// C8 for work block: a crash between the move and the edge leaves the
// ticket in backlog with pending_block, so dependency-advance holds it
// even with its old done edge. A replay reaches the state of a call with
// no crash.
func TestC8AWorkBlockCrashLeavesNoClaimableTicket(t *testing.T) {
	w := newWorld(t)
	d, x := w.ticketIn(Done), w.ticketIn(Todo)
	tk := w.ticketIn(Todo)
	w.edge(tk, d)
	w.m.hook = func(p string) bool { return p == pointBlockMoved }
	wantCode(t, w.block(tk, x), cli.CodeInternal)
	w.m.hook = nil
	w.wantState(tk, Backlog, d)
	if got := w.show(tk).Fact(FactPendingBlock); got != x {
		t.Fatalf("after the crash pending_block = %q, want %s", got, x)
	}
	mustOK(t, w.block(tk, x))
	w.wantState(tk, Backlog, d, x)
}

// C8 for work unblock --by: a crash after the delete leaves the ticket in
// backlog. A replay after the delete, and one after the move, each end
// with the ticket in todo and exit 0.
func TestC8AWorkUnblockByCrashLeavesNoClaimableTicket(t *testing.T) {
	w := newWorld(t)
	x := w.ticketIn(Todo)
	tk := w.ticketIn(Todo)
	mustOK(t, w.block(tk, x))
	w.m.hook = func(p string) bool { return p == pointEdgeRemoved }
	wantCode(t, w.unblock(tk, x), cli.CodeInternal)
	w.m.hook = nil
	w.wantState(tk, Backlog)
	mustOK(t, w.unblock(tk, x))
	w.wantState(tk, Todo)
	res := w.unblock(tk, x)
	mustOK(t, res)
	if res.Data.(UnblockResult).EdgeRemoved {
		t.Fatal("the replay after the move reports a removed edge")
	}
	w.wantState(tk, Todo)
}

// crashedBlock leaves tk in backlog with pending_block on, and no edge
// to it, as a work block that crashed before its edge does.
func (w *world) crashedBlock(tk, on string) {
	w.t.Helper()
	w.m.hook = func(p string) bool { return p == pointBlockMoved }
	wantCode(w.t, w.block(tk, on), cli.CodeInternal)
	w.m.hook = nil
}

// C8: while pending_block names an open item with no edge, work block and
// work unblock --by with another blocker refuse with block_pending, write
// nothing, and the hint names the replay of work block.
func TestC8BlockPendingOnAnOpenBlockerNamesTheReplay(t *testing.T) {
	w := newWorld(t)
	x, y := w.ticketIn(Todo), w.ticketIn(Todo)
	tk := w.ticketIn(Todo)
	w.crashedBlock(tk, x)
	before := len(w.history(tk))
	for name, res := range map[string]cli.Result{"work block": w.block(tk, y), "work unblock": w.unblock(tk, y)} {
		r := wantCode(t, res, cli.CodeUsage)
		if r.Rule != "block_pending" || !strings.Contains(r.Hint, "loop work block "+tk+" --by "+x) {
			t.Errorf("%s: rule %q hint %q; want block_pending and the replay of work block", name, r.Rule, r.Hint)
		}
	}
	w.wantState(tk, Backlog)
	if n := len(w.history(tk)); n != before {
		t.Fatalf("refused calls wrote %d transitions", n-before)
	}
}

// C8: when the pending blocker is terminal, the hint names work unblock
// --by it, and that command frees the ticket.
func TestC8BlockPendingOnATerminalBlockerNamesTheUnblock(t *testing.T) {
	w := newWorld(t)
	x, y := w.ticketIn(Todo), w.ticketIn(Todo)
	tk := w.ticketIn(Todo)
	w.crashedBlock(tk, x)
	w.force(x, Canceled, nil)
	for name, res := range map[string]cli.Result{"work block": w.block(tk, y), "work unblock": w.unblock(tk, y)} {
		r := wantCode(t, res, cli.CodeUsage)
		if r.Rule != "block_pending" || !strings.Contains(r.Hint, "loop work unblock "+tk+" --by "+x) {
			t.Errorf("%s: rule %q hint %q; want block_pending and work unblock --by %s", name, r.Rule, r.Hint, x)
		}
	}
	mustOK(t, w.unblock(tk, x))
	w.wantState(tk, Todo)
}

// work unblock of an epic keeps the epic table's own rule (spec
// factorio-4n0): it lands on epic_entry on the person's word, with an open
// blocker left, and deletes no edge. The ticket rules of work unblock
// cover tickets only.
func TestWorkUnblockOfAnEpicLandsOnItsEntryOnThePersonsWord(t *testing.T) {
	w := newWorld(t)
	x, y := w.ticketIn(Todo), w.ticketIn(Todo)
	for by, epic := range map[string]string{"": w.create(KindEpic, "create_prd", map[string]string{"blocked": "true"}),
		x: w.create(KindEpic, "create_prd", map[string]string{"blocked": "true"})} {
		w.edge(epic, x)
		w.edge(epic, y)
		mustOK(t, w.unblock(epic, by))
		w.wantState(epic, PRDNeeded, x, y)
	}
}
