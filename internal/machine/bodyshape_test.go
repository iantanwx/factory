package machine

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// shapedBug is a bug body that holds the Bug shape.
const shapedBug = "## Contract C-bug-1\n\nThe list page shows every widget the API returns.\n\n" +
	"## Source\n\nUS-01 of the widgets PRD.\n\n" +
	"## Reproduction\n\n`web/list_test.go`\n\n```go\nfunc TestListShowsEveryWidget(t *testing.T) {}\n```\n\n" +
	"## What was seen\n\nThe list is empty.\n\n## Priority\n\n2\n\n## Files\n\n`web/list.go`\n\n" +
	"## Done means\n\nThe test passes.\n\n## Out of scope\n\nThe sort order.\n"

// shapedTicket is a ticket body that holds the Ticket shape.
const shapedTicket = "## Source\n\nSpec entry 1.\n\n## Change\n\nList each widget.\n\n" +
	"## Contracts to prove\n\n- C1. The list shows each widget.\n\n## Files\n\n`web/list.go`\n\n" +
	"## Done means\n\nThe test passes.\n\n## Out of scope\n\nThe sort order.\n"

// taskBody is the body of id's task, as admission reads it.
func (w *world) taskBody(id string) string {
	w.t.Helper()
	is, err := w.bc.Get(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	_, body := TaskText(is)
	return body
}

// C12, the work shape row: the bug body is checked by the Bug shape
// before the ticket is made, and the ticket records shape=bug. A refusal
// names every fault, leaves the bug in its state and makes no ticket.
func TestC12WorkShapeChecksTheBugShape(t *testing.T) {
	w := newWorld(t)
	bug, run := w.shapeRun("repro")
	before := w.show(bug)
	noReproduction := strings.Replace(shapedBug, "## Reproduction", "## Steps", 1)
	for name, body := range map[string]string{
		"a ticket body":      shapedTicket,
		"no Reproduction":    noReproduction,
		"no contract":        strings.Replace(shapedBug, "## Contract C-bug-1", "## Contract", 1),
		"Done means over 60": strings.Replace(shapedBug, "The test passes.", strings.Repeat("word ", 61), 1),
	} {
		r := wantCode(t, w.call("work shape", run, []string{bug}, map[string]string{"title": "T", "body": body, "priority": "2"}),
			cli.CodeShapeRefused)
		if r.Rule != "bug_shape" || r.Item != bug {
			t.Errorf("%s: refused with rule %q on %q, want bug_shape on %s", name, r.Rule, r.Item, bug)
		}
	}
	r := wantCode(t, w.call("work shape", run, []string{bug}, map[string]string{"title": "T", "body": "x", "priority": "2"}), cli.CodeShapeRefused)
	for _, want := range []string{"missing section: Contract C-bug-<n>", "missing section: Reproduction", "missing section: Out of scope"} {
		if !strings.Contains(r.Hint, want) {
			t.Errorf("the hint %q does not name %q", r.Hint, want)
		}
	}
	if after := w.show(bug); after.Status != before.Status || after.Seq != before.Seq {
		t.Fatalf("a refused shape changed the bug: %+v", after)
	}
	if n := len(w.items(KindTicket)); n != 0 {
		t.Fatalf("a refused shape made %d tickets", n)
	}

	res := w.call("work shape", run, []string{bug}, map[string]string{"title": "T", "body": shapedBug, "priority": "2"})
	mustOK(t, res)
	if got := w.show(res.Data.(ShapeResult).Ticket).Fact(FactShape); got != "bug" {
		t.Fatalf("the shaped ticket holds shape %q, want bug", got)
	}
}

// C12, the work edit rows: an edit of the body checks the shape that the
// item's shape fact names. An item with no shape fact, and an edit with
// no body, are not checked. A refusal writes nothing.
func TestC12WorkEditChecksTheShapeFact(t *testing.T) {
	w := newWorld(t)
	for _, c := range []struct{ shape, good, bad, rule string }{
		{"ticket", shapedTicket, strings.Replace(shapedTicket, "## Change", "## Plan", 1), "ticket_shape"},
		{"bug", shapedBug, strings.Replace(shapedBug, "## Priority", "## Urgency", 1), "bug_shape"},
	} {
		id := w.create(KindTicket, "create", nil)
		w.force(id, Todo, map[string]string{FactShape: c.shape})
		mustOK(t, w.call("work edit", coord(), []string{id}, map[string]string{"body": c.good}))
		before := w.show(id)
		r := wantCode(t, w.call("work edit", coord(), []string{id}, map[string]string{"title": "New", "body": c.bad}), cli.CodeShapeRefused)
		if r.Rule != c.rule || r.Item != id || r.CurrentStatus != Todo {
			t.Errorf("%s: refused %+v, want rule %s on %s in todo", c.shape, *r, c.rule, id)
		}
		after := w.show(id)
		if w.taskBody(id) != c.good || after.Title != before.Title || after.Seq != before.Seq || after.Fact(FactShape) != c.shape {
			t.Errorf("%s: a refused edit wrote: %+v", c.shape, after)
		}
		// An edit with no body is not checked, and keeps the shape.
		mustOK(t, w.call("work edit", coord(), []string{id}, map[string]string{"title": "Only the title"}))
		if it := w.show(id); it.Title != "Only the title" || it.Fact(FactShape) != c.shape {
			t.Errorf("%s: after a title edit: %q, shape %q", c.shape, it.Title, it.Fact(FactShape))
		}
	}

	// A ticket with no shape fact takes any body.
	id := w.create(KindTicket, "create", nil)
	mustOK(t, w.call("work edit", coord(), []string{id}, map[string]string{"body": "One line."}))
	if got, it := w.taskBody(id), w.show(id); got != "One line." || it.Fact(FactShape) != "" {
		t.Fatalf("the unshaped edit: body %q, shape %q", got, it.Fact(FactShape))
	}
}

// factorio-bxv0: a run cannot call a host verb, so an edit of a ticket body
// that tells a run to call one under Done means or Contracts to prove is
// refused with host_verb_in_ticket, and the hint names the verb and its
// line. The rule holds for a ticket with no shape fact too. A host verb
// under Change, Source or Out of scope is accepted, since a ticket may
// change a host verb, and so is a verb that is no host verb.
func TestWorkEditRefusesAHostVerbUnderDoneMeans(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	w.force(id, Todo, nil)
	for _, c := range []struct{ name, old, new, line string }{
		{"Done means", "The test passes.", "The test passes, and `loop doctor` is green.", "line 19"},
		{"Contracts to prove", "- C1. The list shows each widget.", "- C1. `loop upgrade --dry-run` lists each widget.", "line 11"},
		{"a fenced block", "The test passes.", "```sh\nloop doctor\n```", "line 20"},
	} {
		body := strings.Replace(shapedTicket, c.old, c.new, 1)
		r := wantCode(t, w.call("work edit", coord(), []string{id}, map[string]string{"body": body}), cli.CodeShapeRefused)
		verb := "`loop doctor`"
		if c.name == "Contracts to prove" {
			verb = "`loop upgrade`"
		}
		if r.Rule != "host_verb_in_ticket" || !strings.Contains(r.Hint, verb) || !strings.Contains(r.Hint, c.line) {
			t.Errorf("%s: refused with rule %q and hint %q, want host_verb_in_ticket naming %s on %s", c.name, r.Rule, r.Hint, verb, c.line)
		}
	}
	for name, body := range map[string]string{
		"Change":         strings.Replace(shapedTicket, "List each widget.", "List each widget, as `loop doctor` does.", 1),
		"Source":         strings.Replace(shapedTicket, "Spec entry 1.", "`loop doctor` failed in alert/113.", 1),
		"Out of scope":   strings.Replace(shapedTicket, "The sort order.", "A change to `loop upgrade`.", 1),
		"no host verb":   strings.Replace(shapedTicket, "The test passes.", "`loop work show` names the ticket.", 1),
		"another prefix": strings.Replace(shapedTicket, "The test passes.", "`loop doctorate` is no verb.", 1),
	} {
		if res := w.call("work edit", coord(), []string{id}, map[string]string{"body": body}); res.Refusal != nil {
			t.Errorf("%s: refused %+v", name, res.Refusal)
		}
	}
}

// factorio-bxv0: the gate reads its host verbs from the capability matrix,
// so it refuses each verb that HostVerbs lists, and a copy that drifts
// from the list fails here.
func TestWorkEditRefusesEveryHostVerb(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	w.force(id, Todo, nil)
	m, ok := cli.Surface().CapabilityMatrix()
	if !ok || len(m.HostVerbs) == 0 {
		t.Fatal("the surface lists no host verb")
	}
	for _, v := range m.HostVerbs {
		body := strings.Replace(shapedTicket, "The test passes.", "Run `loop "+v+"`.", 1)
		res := w.call("work edit", coord(), []string{id}, map[string]string{"body": body})
		if res.Refusal == nil || res.Refusal.Rule != "host_verb_in_ticket" || !strings.Contains(res.Refusal.Hint, "`loop "+v+"`") {
			t.Errorf("loop %s: answered %+v, want host_verb_in_ticket naming it", v, res.Refusal)
		}
	}
}
