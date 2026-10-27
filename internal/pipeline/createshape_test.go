package pipeline_test

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// ticketBody is a ticket body that holds the Ticket shape.
const ticketBody = "## Source\n\nUS-01 of the widgets PRD.\n\n## Change\n\nList each widget.\n\n" +
	"## Contracts to prove\n\n- C1. The list shows each widget.\n\n## Files\n\n`web/list.go`\n\n" +
	"## Done means\n\nThe test passes.\n\n## Out of scope\n\nThe sort order.\n"

// C12, the work create rows: a ticket under an epic with a PRD is checked
// by the Ticket shape and records shape=ticket. A refusal names every
// fault and creates nothing. The same body passes, unchecked and with no
// shape fact, under an architecture epic, as a PRD-less ticket and as a
// bug.
func TestC12WorkCreateChecksATicketUnderAPRD(t *testing.T) {
	w := newWorld(t)
	epic, _ := w.submitted()
	w.review(epic, "pass")
	w.merge(w.prOf(epic))
	w.tick()
	run := w.run("run-x", "", machine.StageImplementer)
	bad := strings.Replace(strings.Replace(ticketBody, "## Change", "## Plan", 1), "## Files", "## Paths", 1)
	flags := map[string]string{"title": "List widgets", "epic": epic, "story": "US-01", "body": bad}

	r := wantCode(t, w.call(run, "", "work create", flags), cli.CodeShapeRefused)
	if r.Rule != "ticket_shape" || !strings.Contains(r.Hint, "missing section: Change") || !strings.Contains(r.Hint, "missing section: Files") {
		t.Fatalf("refused with rule %q and hint %q", r.Rule, r.Hint)
	}
	flags["body"] = strings.Replace(ticketBody, "List each widget.", strings.Repeat("word ", 151), 1)
	if r := wantCode(t, w.call(run, "", "work create", flags), cli.CodeShapeRefused); !strings.Contains(r.Hint, "Change: 151 words, limit 150") {
		t.Fatalf("the hint %q does not name the Change limit", r.Hint)
	}
	if n := len(w.items(machine.KindTicket)); n != 0 {
		t.Fatalf("a refused create made %d tickets", n)
	}

	flags["body"] = ticketBody
	data := ok(t, w.call(run, "", "work create", flags))
	if got := w.show(data["item"].(string)).Fact(machine.FactShape); got != "ticket" {
		t.Fatalf("the ticket under a PRD holds shape %q, want ticket", got)
	}

	arch := ok(t, w.call(w.coordinator(), "k-arch", "epic create", map[string]string{"title": "Quality ratchets",
		"project": testProject, "architecture": "true", "body": "An ADR records the decision."}))["epic"].(string)
	request := "the operator asked for this"
	for name, flags := range map[string]map[string]string{
		"under an architecture epic": {"epic": arch, "request": request},
		"PRD-less":                   {"request": request},
		"a bug":                      {"kind": "bug", "request": request},
	} {
		flags["title"], flags["body"], flags["project"] = "Unchecked", bad, testProject
		data := ok(t, w.call(w.coordinator(), "", "work create", flags))
		if got := w.show(data["item"].(string)).Fact(machine.FactShape); got != "" {
			t.Errorf("%s: the item holds shape %q, want none", name, got)
		}
	}
}

// factorio-bxv0: work create refuses a ticket whose Done means tells a run
// to call a host verb, with host_verb_in_ticket, and creates nothing. The
// rule holds for a PRD-less ticket, which no shape checks. The same verb
// under Change is accepted, and a bug report may quote the host verb that
// failed.
func TestWorkCreateRefusesAHostVerbUnderDoneMeans(t *testing.T) {
	w := newWorld(t)
	done := strings.Replace(ticketBody, "The test passes.", "`loop doctor` is green.", 1)
	flags := map[string]string{"title": "Doctor", "request": "the operator asked for this", "project": testProject, "body": done}
	r := wantCode(t, w.call(w.coordinator(), "", "work create", flags), cli.CodeShapeRefused)
	if r.Rule != "host_verb_in_ticket" || !strings.Contains(r.Hint, "`loop doctor`") || !strings.Contains(r.Hint, "line 19") {
		t.Fatalf("refused with rule %q and hint %q", r.Rule, r.Hint)
	}
	if n := len(w.items(machine.KindTicket)); n != 0 {
		t.Fatalf("a refused create made %d tickets", n)
	}

	flags["body"] = strings.Replace(ticketBody, "List each widget.", "Teach `loop doctor` to list each widget.", 1)
	ok(t, w.call(w.coordinator(), "", "work create", flags))
	flags["kind"], flags["body"] = "bug", done
	ok(t, w.call(w.coordinator(), "", "work create", flags))
}
