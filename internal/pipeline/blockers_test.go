package pipeline_test

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// edges lists the ids that id holds a blocks edge to.
func (w *world) edges(id string) []string {
	w.t.Helper()
	deps, err := w.bx.Dependencies(w.ctx, id)
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

// newItem creates one item of kind with its first move, as the server.
func (w *world) newItem(kind machine.Kind, verb string) string {
	w.t.Helper()
	id, res := w.m.Create(w.ctx, machine.CreateRequest{Project: testProject, Kind: kind, Title: string(kind), Verb: verb,
		Actor: machine.Server("test")})
	if res.Refusal != nil {
		w.t.Fatalf("create a %s: %+v", kind, *res.Refusal)
	}
	return id
}

func prdLess(flags map[string]string) map[string]string {
	flags["title"], flags["project"] = "Fix the typo", testProject
	flags["request"] = "Ian: please fix the typo once the start page lands"
	return flags
}

// C1: a ticket created with blockers waits in backlog with one blocks edge
// to each. With no --blocked-by it lands in todo, with no edge.
func TestC1ATicketCreatedWithBlockersWaitsInBacklog(t *testing.T) {
	w := newWorld(t)
	x := w.newItem(machine.KindTicket, machine.VerbCreate)
	y := w.newItem(machine.KindDecision, machine.VerbFile)

	data := ok(t, w.call(w.coordinator(), "k-blocked", "work create", prdLess(map[string]string{"blocked-by": x + "," + y})))
	id := data["item"].(string)
	if got := w.show(id).Status; got != machine.Backlog || data["status"] != machine.Backlog {
		t.Fatalf("the ticket is in %s, answered %v; want %s", got, data["status"], machine.Backlog)
	}
	if got := strings.Join(w.edges(id), ","); got != x+","+y {
		t.Fatalf("the ticket waits on %q, want %s,%s", got, x, y)
	}

	data = ok(t, w.call(w.coordinator(), "k-free", "work create", prdLess(map[string]string{})))
	id = data["item"].(string)
	if got := w.show(id).Status; got != machine.Todo || len(w.edges(id)) != 0 {
		t.Fatalf("with no --blocked-by the ticket is in %s with edges %v", got, w.edges(id))
	}
}

// C2 for work create: each bad blocker is refused before any write. No
// bead is made, and no edge is written.
func TestC2WorkCreateRefusesABadBlockerBeforeAnyWrite(t *testing.T) {
	w := newWorld(t)
	epic := w.newItem(machine.KindEpic, machine.VerbCreateArchitecture)
	bug := w.newItem(machine.KindBug, machine.VerbFile)
	done := w.newItem(machine.KindTicket, machine.VerbCreate)
	if _, err := w.bx.Update(w.ctx, done, beadsxUpdate(machine.Todo, machine.Done)); err != nil {
		t.Fatal(err)
	}
	open := w.newItem(machine.KindTicket, machine.VerbCreate)
	tickets := len(w.items(machine.KindTicket))
	for _, c := range []struct {
		name, blocker, code, rule string
	}{
		{"unknown", "tk-404", cli.CodeNotFound, "no_item"},
		{"an epic", epic, cli.CodeUsage, "blocker_epic"},
		{"a bug", bug, cli.CodeUsage, "blocker_bug"},
		{"a done ticket", done, cli.CodeUsage, "blocker_terminal"},
	} {
		// The good blocker comes first, so a write before the check would
		// show as its edge.
		res := w.call(w.coordinator(), "", "work create", prdLess(map[string]string{"blocked-by": open + "," + c.blocker}))
		if r := wantCode(t, res, c.code); r.Rule != c.rule || !strings.Contains(r.Hint, c.blocker) {
			t.Errorf("%s: rule %q, hint %q; want rule %q and a hint that names %s", c.name, r.Rule, r.Hint, c.rule, c.blocker)
		}
	}
	if n := len(w.items(machine.KindTicket)); n != tickets {
		t.Fatalf("refused creates made %d tickets", n-tickets)
	}
	for _, it := range append(w.items(machine.KindTicket), w.items(machine.KindEpic)...) {
		if e := w.edges(it.ID); len(e) != 0 {
			t.Fatalf("a refused create left %s waiting on %v", it.ID, e)
		}
	}
}

// C4: a bug report takes no blocker. The coordinator's filing with
// --blocked-by refuses with USAGE, and files no report.
func TestC4ABugReportTakesNoBlocker(t *testing.T) {
	w := newWorld(t)
	x := w.newItem(machine.KindTicket, machine.VerbCreate)
	bug := prdLess(map[string]string{"kind": "bug", "blocked-by": x})
	if r := wantCode(t, w.call(w.coordinator(), "k-bug", "work create", bug), cli.CodeUsage); r.Rule != "bug_takes_no_blocker" {
		t.Fatalf("rule %q, want bug_takes_no_blocker", r.Rule)
	}
	if n := len(w.items(machine.KindBug)); n != 0 {
		t.Fatalf("a refused filing made %d bugs", n)
	}
}

// C8 for work create: a crash between the edges and the create move
// leaves the ticket in Initial. Dependency-advance reads backlog only, and
// the implementer's work query does not see it. A replay of the same call
// lands it in backlog with the same edges as a call with no crash.
func TestC8AWorkCreateCrashLeavesNoClaimableTicket(t *testing.T) {
	w := newWorld(t)
	x := w.newItem(machine.KindTicket, machine.VerbCreate)
	// The blocker is in progress, so the work query has no other ticket.
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: x, Stage: machine.StageImplementer, RunID: "r-x"}); res.Refusal != nil {
		t.Fatalf("claim: %+v", *res.Refusal)
	}
	flags := prdLess(map[string]string{"blocked-by": x})
	w.p.SetCrash(func(point string) bool { return point == "blockers_added" })
	wantCode(t, w.call(w.coordinator(), "k-crash", "work create", flags), cli.CodeInternal)
	w.p.SetCrash(nil)

	var made []machine.Item
	for _, it := range w.items(machine.KindTicket) {
		if it.ID != x {
			made = append(made, it)
		}
	}
	if len(made) != 1 || made[0].Status != machine.Initial {
		t.Fatalf("after the crash: %+v, want one ticket in %s", made, machine.Initial)
	}
	id := made[0].ID
	if got := w.edges(id); len(got) != 1 || got[0] != x {
		t.Fatalf("after the crash the ticket waits on %v", got)
	}
	held, ref := w.m.List(w.ctx, testProject, machine.ListFilter{Kind: machine.KindTicket, Status: machine.Backlog})
	if ref != nil || len(held) != 0 {
		t.Fatalf("dependency-advance would read %+v %+v", held, ref)
	}
	if c, ref := w.adm.Peek(w.ctx, testProject, machine.StageImplementer); c != nil || ref != nil {
		t.Fatalf("the implementer's work query = %+v %+v, want nothing", c, ref)
	}

	data := ok(t, w.call(w.coordinator(), "k-crash", "work create", flags))
	if data["item"] != id || data["status"] != machine.Backlog {
		t.Fatalf("the replay answered %v, want %s in %s", data, id, machine.Backlog)
	}
	if got := w.show(id).Status; got != machine.Backlog {
		t.Fatalf("after the replay the ticket is in %s", got)
	}
	if got := w.edges(id); len(got) != 1 || got[0] != x {
		t.Fatalf("after the replay the ticket waits on %v", got)
	}
}
