package pipeline_test

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/shape"
	"github.com/iantanwx/loop/pack"
)

const (
	undecided = "1. Sort widgets by price?\n2. Page size?\n   **Answered (operator, 2026-10-27):** 50.\n"
	decided   = "1. Sort widgets by price?\n   **Deferred (operator, 2026-10-27):** a later epic.\n2. Page size?\n   **Answered (operator, 2026-10-27):** 50.\n"
)

// refusedAll asserts that res is SHAPE_REFUSED with rule, and that the
// batch created no ticket and left the epic in decomposing.
func (w *world) refusedAll(res cli.Result, epic, rule string) *cli.Refusal {
	w.t.Helper()
	ref := wantCode(w.t, res, cli.CodeShapeRefused)
	if ref.Rule != rule {
		w.t.Fatalf("want rule %s, got rule %s: %s", rule, ref.Rule, ref.Hint)
	}
	if n := len(w.items(machine.KindTicket)); n != 0 {
		w.t.Fatalf("a refused batch created %d tickets", n)
	}
	if got := w.show(epic).Status; got != machine.Decomposing {
		w.t.Fatalf("a refused batch moved the epic to %s", got)
	}
	return ref
}

func hintHolds(t *testing.T, ref *cli.Refusal, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(ref.Hint, want) {
			t.Fatalf("the hint %q does not say %q", ref.Hint, want)
		}
	}
}

// C5: decompose reads the spec at spec_path. The base holds a second spec
// file of the epic, so the fallback would refuse no_spec: open_questions
// proves the fact was read.
func TestC5DecomposeRefusesAnUnansweredSpecAtSpecPath(t *testing.T) {
	w := newWorld(t)
	epic := w.decomposingWith(true)
	w.onBase(map[string]string{specPath(epic, "widgets"): specWith(undecided), specPath(epic, "draft"): specWith(decided)})
	dec := w.run("run-dec", epic, machine.StageDecomposer)

	ref := w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(threeTickets)), epic, "open_questions")
	hintHolds(t, ref, specPath(epic, "widgets"), "line 9: Sort widgets by price?")
	if strings.Contains(ref.Hint, "Page size") {
		t.Fatalf("the hint names an answered entry: %q", ref.Hint)
	}

	w.onBase(map[string]string{specPath(epic, "widgets"): specWith(decided), specPath(epic, "draft"): specWith(undecided)})
	ok(t, w.call(dec, "k-dec", "epic decompose", fileFlag(threeTickets)))
	if got := w.show(epic).Status; got != machine.Execution {
		t.Fatalf("epic after the batch: %s", got)
	}
}

// C5, C12: with no spec_path, decompose reads the one base file
// docs/plans/<epic>-*-spec.md. Each ticket of a landed batch holds
// shape=ticket.
func TestC5DecomposeRefusesAnUnansweredSpecByThePathPattern(t *testing.T) {
	w := newWorld(t)
	epic := w.decomposing()
	w.onBase(map[string]string{specPath(epic, "widgets"): specWith(undecided)})
	dec := w.run("run-dec", epic, machine.StageDecomposer)

	ref := w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(threeTickets)), epic, "open_questions")
	hintHolds(t, ref, specPath(epic, "widgets"), "line 9: Sort widgets by price?")

	w.onBase(map[string]string{specPath(epic, "widgets"): specWith(decided)})
	ok(t, w.call(dec, "k-dec", "epic decompose", fileFlag(threeTickets)))
	tickets := w.items(machine.KindTicket)
	if len(tickets) != 3 {
		t.Fatalf("the batch made %d tickets", len(tickets))
	}
	for _, tk := range tickets {
		if got := tk.Fact(machine.FactShape); got != "ticket" {
			t.Fatalf("%s holds shape %q", tk.ID, got)
		}
	}
}

// C5: with no spec_path, zero or two base files of the epic refuse no_spec.
func TestC5DecomposeRefusesWithoutOneSpecFile(t *testing.T) {
	w := newWorld(t)
	epic := w.decomposing()
	dec := w.run("run-dec", epic, machine.StageDecomposer)
	pattern := "docs/plans/" + epic + "-*-spec.md"

	w.onBase(map[string]string{specPath(epic, "second"): specWith("None.\n")})
	ref := w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(threeTickets)), epic, "no_spec")
	hintHolds(t, ref, "2 files", pattern)

	w.onBase(map[string]string{specPath(epic, "second"): "", specPath(epic, "widgets"): ""})
	ref = w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(threeTickets)), epic, "no_spec")
	hintHolds(t, ref, "0 files", pattern)
}

// C12: decompose checks each body by the Ticket shape, and refuses the
// batch with every fault of every ticket, by key.
func TestC12DecomposeRefusesAMisshapedBatchByKey(t *testing.T) {
	w := newWorld(t)
	epic := w.decomposing()
	dec := w.run("run-dec", epic, machine.StageDecomposer)
	noChange := strings.Replace(shapedBody, "## Change\n\nA trader sees widgets.\n\n", "", 1)
	noChangeNoFiles := strings.Replace(noChange, "## Files\n\n- May change: `widgets.go`.\n\n", "", 1)
	longChange := strings.Replace(shapedBody, "A trader sees widgets.\n\n## Contracts", strings.Repeat("word ", 151)+"\n\n## Contracts", 1)
	b := batch(
		ticket{Key: "list", Title: "List widgets", Body: noChangeNoFiles, Stories: []string{"US-01"}},
		ticket{Key: "sort", Title: "Sort widgets", Body: longChange, Stories: []string{"US-02"}, BlockedBy: []string{"list"}},
		ticket{Key: "both", Title: "Polish widgets", Stories: []string{"US-01"}},
	)

	ref := w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(b)), epic, "ticket_shape")
	hintHolds(t, ref, "ticket list: missing section: Change; missing section: Files",
		"ticket sort: Change: 151 words, limit 150")
	if strings.Contains(ref.Hint, "ticket both") {
		t.Fatalf("the hint names a ticket that holds its shape: %q", ref.Hint)
	}
}

// C15: the decomposer runbook names each section of the Ticket shape as a
// "## " heading, and each limit as a number. Stop conditions is optional.
func TestC15DecomposerRunbookNamesTheTicketShape(t *testing.T) {
	raw, err := fs.ReadFile(pack.Files, "runbooks/decomposer.md")
	if err != nil {
		t.Fatal(err)
	}
	item := regexp.MustCompile("^[0-9]+\\. `## ([^`]+)`(.*)$")
	var required, optional []string
	text := map[string]string{} // each section's list item, continuation lines included
	last := ""
	for _, l := range strings.Split(string(raw), "\n") {
		switch m := item.FindStringSubmatch(l); {
		case m != nil:
			last = m[1]
			if strings.HasPrefix(m[2], ", optional") {
				optional = append(optional, last)
			} else {
				required = append(required, last)
			}
			text[last] = m[2]
		case last != "" && strings.HasPrefix(l, "   "):
			text[last] += " " + strings.TrimSpace(l)
		default:
			last = ""
		}
	}
	var want []string
	for _, n := range shape.Ticket.Required {
		want = append(want, n.Name)
	}
	if !slices.Equal(required, want) {
		t.Fatalf("the runbook's required sections %q, the Ticket shape's %q", required, want)
	}
	if !slices.Equal(optional, []string{"Stop conditions"}) {
		t.Fatalf("the runbook's optional sections %q", optional)
	}
	for _, l := range shape.Ticket.Limits {
		where := text[l.Section]
		if l.Section == "" {
			where = string(raw)
		}
		if !strings.Contains(where, fmt.Sprintf("%d words", l.Words)) {
			t.Fatalf("the runbook does not give %q a limit of %d words", l.Section, l.Words)
		}
		if l.Items > 0 && !strings.Contains(where, fmt.Sprintf("%d contracts", l.Items)) {
			t.Fatalf("the runbook does not give %q a limit of %d contracts", l.Section, l.Items)
		}
	}
}
