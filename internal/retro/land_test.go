package retro_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/retro"
)

// landedRef is the landed_ref of the learning id.
func (w *world) landedRef(id int) string {
	w.t.Helper()
	var ref string
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT landed_ref FROM learnings WHERE id = ?`, id).Scan(&ref)
	}); err != nil {
		w.t.Fatal(err)
	}
	return ref
}

// cutShort undoes the end of a route that a crash cut short after the
// learnings were stored: no learning holds its created id, and the retro
// holds no notice.
func (w *world) cutShort() {
	w.t.Helper()
	w.exec(`UPDATE learnings SET landed_ref = ''`)
	w.exec(`DELETE FROM notices`)
}

func (w *world) items(kind machine.Kind) []machine.Item {
	w.t.Helper()
	items, ref := w.m.List(context.Background(), prj, machine.ListFilter{Kind: kind})
	if ref != nil {
		w.t.Fatalf("list %s: %+v", kind, ref)
	}
	return items
}

func (w *world) noticeText(id string) string {
	w.t.Helper()
	es, err := inbox.New(inbox.Config{Store: w.st}).Entries(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	for _, e := range es {
		if e.ID == id {
			return e.Title + "\n" + e.Text
		}
	}
	w.t.Fatalf("no inbox entry %s", id)
	return ""
}

// C17: a project policy learning that both judges land, with a reading
// that clears both thresholds, leaves one ticket in todo in the retro's
// project. The ticket holds the fact retro_learning and the create key
// retro/L-1, the learning holds its id in landed_ref, the notice names it,
// and no inbox entry but the notice awaits a person. A second route of the
// same file leaves the same ticket, also when a crash cut the first route
// short.
func TestC17APassedProjectPolicyLearningOpensWork(t *testing.T) {
	w := newWorld(t)
	w.retroOf(prj, "r-1", "r-a", "r-b")
	file := panelFile(t, landing(learning("name the merge base in each ticket", "run:r-a", "run:r-b"), retro.ProjectPolicy, retro.Edit))
	got := w.mustRoute(prj, "r-1", file)

	tickets := w.items(machine.KindTicket)
	if len(tickets) != 1 {
		t.Fatalf("%d tickets after the route, want 1: %+v", len(tickets), tickets)
	}
	tk := tickets[0]
	if tk.Status != machine.Todo || tk.Fact("retro_learning") != "L-1" || tk.Fact(machine.FactCreateKey) != "retro/L-1" {
		t.Fatalf("ticket = %+v, want todo with retro_learning L-1 and create key retro/L-1", tk)
	}
	if len(got.Learnings) != 1 || got.Learnings[0].Ref != tk.ID {
		t.Fatalf("route = %+v, want L-1 landed as %s", got, tk.ID)
	}
	if ref := w.landedRef(1); ref != tk.ID {
		t.Fatalf("L-1 landed_ref = %q, want %s", ref, tk.ID)
	}
	if text := w.noticeText(got.Notice); !strings.Contains(text, tk.ID) {
		t.Fatalf("the notice does not name %s:\n%s", tk.ID, text)
	}
	es, err := inbox.New(inbox.Config{Store: w.st}).Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.Awaits == inbox.AwaitsPerson && e.Kind != inbox.KindNotice {
			t.Fatalf("entry %s (%s) awaits a person", e.ID, e.Kind)
		}
	}

	if res := w.route(prj, "r-1", file); res.Refusal == nil || res.Refusal.Rule != "retro_done" {
		t.Fatalf("second route = %+v, want retro_done", res)
	}
	w.cutShort()
	again := w.mustRoute(prj, "r-1", file)
	if tickets := w.items(machine.KindTicket); len(tickets) != 1 || tickets[0].ID != tk.ID {
		t.Fatalf("tickets after the second route = %+v, want only %s", tickets, tk.ID)
	}
	if len(again.Learnings) != 1 || again.Learnings[0].Ref != tk.ID || w.landedRef(1) != tk.ID {
		t.Fatalf("second route = %+v, want L-1 landed as %s", again, tk.ID)
	}
	if n := w.count(`SELECT count(*) FROM notices`); n != 1 {
		t.Fatalf("%d notices after the second route, want 1", n)
	}
}

// A userspace skill learning lands as a ticket whose body names both skill
// roots of ADR 0013, so the implementer edits the one that git tracks.
func TestAUserspaceSkillTicketNamesBothSkillRoots(t *testing.T) {
	w := newWorld(t)
	w.retroOf(prj, "r-1", "r-a", "r-b")
	w.mustRoute(prj, "r-1", panelFile(t, landing(learning("the neon skill names the pooled URL", "run:r-a", "run:r-b"),
		retro.UserspaceSkill, retro.Extend)))
	tickets := w.items(machine.KindTicket)
	if len(tickets) != 1 {
		t.Fatalf("%d tickets, want 1", len(tickets))
	}
	it, ref := w.m.Show(context.Background(), prj, tickets[0].ID)
	if ref != nil {
		t.Fatal(ref)
	}
	for _, s := range []string{".claude/skills/<name>", ".agents/skills/<name>", "the neon skill names the pooled URL", "run:r-a", "run:r-b",
		"userspace_skill", "extend"} {
		if !strings.Contains(it.Body, s) {
			t.Fatalf("the ticket body does not hold %q:\n%s", s, it.Body)
		}
	}
}

// C21: a project ticket learning lands as one bug in reported in the
// retro's project, with the fact retro_learning and no request, and the
// reproducer stage claims it.
func TestC21AProjectTicketLearningLandsAsABugReport(t *testing.T) {
	w := newWorld(t)
	w.retroOf(prj, "r-1", "r-a", "r-b")
	got := w.mustRoute(prj, "r-1", panelFile(t, landing(learning("the export drops the last row", "run:r-a", "run:r-b"),
		retro.ProjectTicket, retro.NA)))
	bugs := w.items(machine.KindBug)
	if len(bugs) != 1 {
		t.Fatalf("%d bugs after the route, want 1", len(bugs))
	}
	b := bugs[0]
	if b.Status != machine.Reported || b.Fact("retro_learning") != "L-1" || b.Fact(machine.FactRequest) != "" {
		t.Fatalf("bug = %+v, want reported with retro_learning L-1 and no request", b)
	}
	if n := len(w.items(machine.KindTicket)); n != 0 {
		t.Fatalf("%d tickets, want none", n)
	}
	if len(got.Learnings) != 1 || got.Learnings[0].Ref != b.ID {
		t.Fatalf("route = %+v, want L-1 landed as %s", got, b.ID)
	}
	if _, res := w.m.Claim(context.Background(), machine.ClaimRequest{Project: prj, Item: b.ID, Stage: machine.StageReproducer,
		RunID: "r-repro"}); res.Refusal != nil {
		t.Fatalf("the reproducer claim of %s = %+v", b.ID, res.Refusal)
	}
}

// C22: a loop fault learning lands as one loop fault from the host, whose
// text names the learning id, the project and every case. A second route
// after a crash cut the first short files no second fault.
func TestC22ALoopFaultLearningNamesEachCase(t *testing.T) {
	w := newWorld(t)
	w.retroOf(prj, "r-1", "r-a", "r-b")
	file := panelFile(t, landing(learning("loop pr open drops the body", "run:r-a", "run:r-b"), retro.LoopFault, retro.NA))
	got := w.mustRoute(prj, "r-1", file)
	var id, text, reporter string
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT 'F-' || n, text, reporter_kind FROM faults`).Scan(&id, &text, &reporter)
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"L-1", prj, "run:r-a", "run:r-b", "loop pr open drops the body"} {
		if !strings.Contains(text, s) {
			t.Fatalf("fault %s does not name %q:\n%s", id, s, text)
		}
	}
	if reporter != cli.PrincipalHost {
		t.Fatalf("fault %s reporter = %s, want host", id, reporter)
	}
	if len(got.Learnings) != 1 || got.Learnings[0].Ref != id || w.landedRef(1) != id {
		t.Fatalf("route = %+v, want L-1 landed as %s", got, id)
	}
	if n := len(w.items(machine.KindTicket)) + len(w.items(machine.KindBug)); n != 0 {
		t.Fatalf("%d tickets and bugs, want none", n)
	}

	w.exec(`DELETE FROM notices`)
	w.mustRoute(prj, "r-1", file)
	if n := w.count(`SELECT count(*) FROM faults`); n != 1 {
		t.Fatalf("%d faults after the second route, want 1", n)
	}
	if w.landedRef(1) != id {
		t.Fatalf("L-1 landed_ref = %q after the second route, want %s", w.landedRef(1), id)
	}
}
