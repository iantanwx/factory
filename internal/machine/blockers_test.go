package machine

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

// edges lists the ids that id holds a blocks edge to.
func (w *world) edges(id string) []string {
	w.t.Helper()
	deps, err := w.bc.Dependencies(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, d := range deps {
		if d.Kind == beadsx.DepBlocks {
			out = append(out, d.ID)
		}
	}
	return out
}

// shapeRun files a bug and claims it for the reproducer.
func (w *world) shapeRun(run string) (string, rpc.Principal) {
	w.t.Helper()
	bug := w.create(KindBug, "file", nil)
	return bug, w.claimAs(bug, StageReproducer, run)
}

// C3: a bug shaped with blockers closes as shaped, and its ticket waits in
// backlog with one blocks edge to each blocker. A ticket and a decision
// each block.
func TestC3ABugShapedWithBlockersMakesATicketThatWaits(t *testing.T) {
	w := newWorld(t)
	x := w.create(KindTicket, "create", nil)
	d := w.create(KindDecision, "file", nil)
	bug, run := w.shapeRun("repro")
	res := w.call("work shape", run, []string{bug}, map[string]string{"title": "Fix it", "body": "b", "blocked-by": x + ", " + d})
	mustOK(t, res)
	ticket := res.Data.(ShapeResult).Ticket
	if it := w.show(bug); it.Status != Shaped || it.Fact(FactTicket) != ticket {
		t.Fatalf("the bug after shape: %s %v", it.Status, it.Facts)
	}
	if got := w.show(ticket).Status; got != Backlog {
		t.Fatalf("the shaped ticket is in %s, want %s", got, Backlog)
	}
	if got := strings.Join(w.edges(ticket), ","); got != x+","+d {
		t.Fatalf("the shaped ticket waits on %q, want %s,%s", got, x, d)
	}
}

// C2 for work shape: each bad blocker is refused before any write. The
// bug keeps its state, and no ticket is made.
func TestC2WorkShapeRefusesABadBlockerBeforeAnyWrite(t *testing.T) {
	w := newWorld(t)
	epic := w.create(KindEpic, "create_architecture", nil)
	raw := w.create(KindBug, "file", nil)
	shaped, run0 := w.shapeRun("repro-0")
	mustOK(t, w.call("work shape", run0, []string{shaped}, map[string]string{"title": "T", "body": "b"}))
	shapedInto := w.show(shaped).Fact(FactTicket)
	done := w.create(KindTicket, "create", nil)
	w.force(done, Done, nil)
	canceled := w.create(KindDecision, "file", nil)
	w.force(canceled, Canceled, nil)

	bug, run := w.shapeRun("repro")
	before := len(w.items(KindTicket))
	for _, c := range []struct {
		name, blocker, code, rule, hint string
	}{
		{"unknown", "tk-404", cli.CodeNotFound, "no_item", "tk-404"},
		{"an epic", epic, cli.CodeUsage, "blocker_epic", epic},
		{"a raw bug", raw, cli.CodeUsage, "blocker_bug", raw},
		{"a shaped bug", shaped, cli.CodeUsage, "blocker_bug", shapedInto},
		{"a done ticket", done, cli.CodeUsage, "blocker_terminal", done},
		{"a canceled decision", canceled, cli.CodeUsage, "blocker_terminal", canceled},
	} {
		flags := map[string]string{"title": "T", "body": "b", "blocked-by": c.blocker}
		r := wantCode(t, w.call("work shape", run, []string{bug}, flags), c.code)
		if r.Rule != c.rule || !strings.Contains(r.Hint, c.hint) {
			t.Errorf("%s: rule %q, hint %q; want rule %q and a hint that names %s", c.name, r.Rule, r.Hint, c.rule, c.hint)
		}
	}
	if n := len(w.items(KindTicket)); n != before {
		t.Fatalf("refused shapes made %d tickets", n-before)
	}
	if it := w.show(bug); it.Status != Reproducing {
		t.Fatalf("a refused shape moved the bug to %s", it.Status)
	}
}

// C8 for work shape: a crash between the edges and the create move leaves
// the ticket in Initial, which no queue reads. A replay of the same call
// lands the ticket in backlog with one edge for each blocker, as a call
// with no crash does.
func TestC8AWorkShapeCrashLeavesNoClaimableTicket(t *testing.T) {
	w := newWorld(t)
	x := w.create(KindTicket, "create", nil)
	bug, run := w.shapeRun("repro")
	flags := map[string]string{"title": "Fix it", "body": "b", "blocked-by": x}
	w.m.hook = func(p string) bool { return p == pointBlockers }
	wantCode(t, w.call("work shape", run, []string{bug}, flags), cli.CodeInternal)
	w.m.hook = nil

	var made []Item
	for _, it := range w.items(KindTicket) {
		if it.Fact(FactBug) == bug {
			made = append(made, it)
		}
	}
	if len(made) != 1 || made[0].Status != Initial {
		t.Fatalf("after the crash: %+v, want one ticket in %s", made, Initial)
	}
	if got := w.edges(made[0].ID); len(got) != 1 || got[0] != x {
		t.Fatalf("after the crash the ticket waits on %v", got)
	}
	if got := w.show(bug).Status; got != Reproducing {
		t.Fatalf("after the crash the bug is in %s", got)
	}

	res := w.call("work shape", run, []string{bug}, flags)
	mustOK(t, res)
	if got := res.Data.(ShapeResult).Ticket; got != made[0].ID {
		t.Fatalf("the replay shaped %s, want %s", got, made[0].ID)
	}
	if got := w.show(made[0].ID).Status; got != Backlog {
		t.Fatalf("after the replay the ticket is in %s", got)
	}
	if got := w.edges(made[0].ID); len(got) != 1 || got[0] != x {
		t.Fatalf("after the replay the ticket waits on %v", got)
	}
}

// CheckBlockers refuses the ticket itself, and a blocker whose blocks
// edges lead back to the ticket at any depth.
func TestCheckBlockersRefusesTheTicketItselfAndACycle(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	tk := w.create(KindTicket, "create", nil)
	mid := w.create(KindTicket, "create", nil)
	far := w.create(KindTicket, "create", nil)
	free := w.create(KindTicket, "create", nil)
	for _, e := range [][2]string{{far, mid}, {mid, tk}} {
		if err := w.bc.AddDependency(ctx, e[0], e[1], beadsx.DepBlocks); err != nil {
			t.Fatal(err)
		}
	}
	for blockers, rule := range map[string]string{tk: "blocker_self", mid: "blocker_cycle", far: "blocker_cycle"} {
		r := w.m.CheckBlockers(ctx, prj, tk, []string{blockers})
		if r == nil || r.Code != cli.CodeUsage || r.Rule != rule {
			t.Errorf("CheckBlockers(%s, [%s]) = %+v, want %s %s", tk, blockers, r, cli.CodeUsage, rule)
		}
	}
	if r := w.m.CheckBlockers(ctx, prj, tk, []string{free}); r != nil {
		t.Errorf("CheckBlockers(%s, [%s]) = %+v, want nil", tk, free, r)
	}
}
