//go:build acceptance

package scenarios

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/machine"
)

// fileBug files the operator's bug report as the coordinator, and returns
// the bug.
func fileBug(w *acceptance.World, title, body string) string {
	w.T.Helper()
	r := w.Must(w.Coordinator("work", "create", "--kind", "bug", "--project", acceptance.Project, "--title", title,
		"--body", body, "--request", "the operator reported this bug and asked the loop to fix it"))
	bug := r.Str("item")
	wantStatus(w, bug, machine.Reported)
	return bug
}

// R48 and U5: the coordinator files the operator's bug report. The
// reproducer stage takes it, and its run shapes it into a ticket that the
// implementer stage then takes.
func TestCoordinatorFilesABugAndTheReproducerShapesIt(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	bug := fileBug(w, "The list page shows no widgets", "Open /widgets: the list is empty, and the API returns three widgets.")

	run := w.Dispatch(machine.StageReproducer)
	if run.Item != bug {
		t.Fatalf("the reproducer took %s, want %s", run.Item, bug)
	}
	wantStatus(w, bug, machine.Reproducing)
	r := w.Must(w.In(run.Checkout, "work", "shape", bug, "--title", "Show every widget on the list page",
		"--body", "The list page drops each widget. Render every widget the API returns."))
	ticket := r.Str("ticket")
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	wantStatus(w, bug, machine.Shaped)
	wantStatus(w, ticket, machine.Todo)
	if got := w.Show(ticket).Facts[machine.FactBug]; got != bug {
		t.Fatalf("the shaped ticket names bug %q, want %s", got, bug)
	}
	if impl := w.Dispatch(machine.StageImplementer); impl.Item != ticket {
		t.Fatalf("the implementer took %s, want the shaped ticket %s", impl.Item, ticket)
	}
}

// R46: the coordinator creates an architecture epic. It waits in
// architecture until a merge to the base adds an ADR whose Ticket line
// names it. The reactor then moves it to spec_needed, and the spec author
// takes it.
func TestArchitectureEpicWaitsForItsADR(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	r := w.Must(w.Coordinator("epic", "create", "--architecture", "--project", acceptance.Project,
		"--title", "Move the widget store to Postgres", "--body", "The operator asked for an ADR on the widget store."))
	epic := r.Str("epic")
	wantStatus(w, epic, machine.Architecture)

	w.PushMain("notes", map[string]string{"docs/notes.md": "Epic " + epic + " moves the store.\n"}, "")
	w.React()
	wantStatus(w, epic, machine.Architecture)

	w.PushMain("ADR 1", map[string]string{"docs/adr/0001-widget-store.md": "# ADR 0001: Widget store\n\n**Ticket:** " + epic + "\n\nUse Postgres.\n"}, "")
	w.React()
	wantStatus(w, epic, machine.SpecNeeded)
	if got := w.Show(epic).Facts[machine.FactADR]; got != "docs/adr/0001-widget-store.md" {
		t.Fatalf("the epic records ADR %q", got)
	}
	if run := w.Dispatch(machine.StageSpecAuthor); run.Item != epic {
		t.Fatalf("the spec-author took %s, want %s", run.Item, epic)
	}
	wantStatus(w, epic, machine.SpecDrafting)
}

// F-10: an epic that an ADR named only in prose, and that the reactor
// before the fix moved to spec_needed, returns to architecture with no
// adr fact on the next reactor pass. The epic on the ADR's Ticket line
// stays in spec_needed.
func TestMisjoinedEpicReturnsToArchitecture(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	create := func(title string) string {
		r := w.Must(w.Coordinator("epic", "create", "--architecture", "--project", acceptance.Project,
			"--title", title, "--body", "The operator asked for an ADR."))
		return r.Str("epic")
	}
	own, cited := create("Share the skill pack"), create("Move the widget store to Postgres")
	const adr = "docs/adr/0007-shared-skill-pack.md"
	// Replay the write the reactor before the fix made for the cited epic.
	res := w.Srv.Machine.Apply(context.Background(), machine.Request{Project: acceptance.Project, Item: cited,
		Verb: machine.VerbADRLanded, ExpectedStatus: machine.Architecture, Params: map[string]string{"path": adr},
		Actor: machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("adr_landed on %s: %+v", cited, res.Refusal)
	}
	w.PushMain("ADR 7", map[string]string{adr: "# ADR 0007: Share the skill pack\n\n**Ticket:** " + own +
		"\n\nEpic " + cited + " reads the same pack later.\n"}, "")
	w.React()
	wantStatus(w, own, machine.SpecNeeded)
	wantStatus(w, cited, machine.Architecture)
	if got := w.Show(cited).Facts[machine.FactADR]; got != "" {
		t.Fatalf("the cited epic still records ADR %q", got)
	}
}

// factorio-dr8 (alpha bugs-report-ask.ts): the reproducer asks the
// reporter for a fact only the reporter holds, and no escalation pages
// the operator. The ask ends the run's claim, and the inbox relays the
// question to the reporter. The coordinator answers it with the
// reporter's words, and the report returns to the reproducer, whose next
// brief holds every question and every answer so far, as the issue
// thread does in alpha. A report asks at most twice: the third ask
// refuses with ASK_LIMIT, and the run escalates with all four texts.
func TestReproducerAsksTheReporterThroughTheInbox(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	bug := fileBug(w, "The list page shows no widgets", "The list is empty.")
	entry := "ask/" + acceptance.Project + "/" + bug
	questions := []string{"- Which page shows the empty list?\n- Which account did you use?", "- Which browser shows the empty list?"}
	answers := []string{"The widgets page, at /widgets. Every account I tried.", "Firefox 131 on Linux."}
	brief := func(run acceptance.Run) string {
		b, err := os.ReadFile(filepath.Join(run.Checkout, ".loop", "brief.json"))
		if err != nil {
			t.Fatalf("read the brief: %v", err)
		}
		return string(b)
	}
	// briefHolds fails unless the brief holds each earlier round, with
	// each question beside its answer.
	briefHolds := func(b string, rounds int) {
		t.Helper()
		var facts struct {
			Item struct {
				Facts map[string]string `json:"facts"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(b), &facts); err != nil {
			t.Fatalf("parse the brief: %v", err)
		}
		for i := range rounds {
			n := strconv.Itoa(i + 1)
			if q, a := facts.Item.Facts["question_"+n], facts.Item.Facts["answer_"+n]; q != questions[i] || a != answers[i] {
				t.Fatalf("the brief after %d answers holds question_%s %q and answer_%s %q, want %q and %q",
					rounds, n, q, n, a, questions[i], answers[i])
			}
		}
	}
	for i, answer := range answers {
		run := w.Dispatch(machine.StageReproducer)
		if run.Item != bug {
			t.Fatalf("ask %d: the reproducer took %s, want %s", i+1, run.Item, bug)
		}
		briefHolds(brief(run), i)
		w.Must(w.In(run.Checkout, "work", "ask", bug, "--body", questions[i]))
		w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
		wantStatus(w, bug, "asked")
		e := w.Entry(entry)
		if e == nil || e["awaits"] != "person" || !contains(e["text"].(string), questions[i]) ||
			!contains(e["next"].(string), "loop inbox answer "+entry) {
			t.Fatalf("ask %d: the inbox entry = %v", i+1, e)
		}
		if r := w.Coordinator("inbox", "answer", entry, "--body", answer, "--amend"); r.Code != 2 || r.RefusalCode() != "USAGE" {
			t.Fatalf("ask %d: an answer with --amend: %s", i+1, r)
		}
		w.Must(w.Coordinator("inbox", "answer", entry, "--body", answer))
		wantStatus(w, bug, machine.Reported)
		if got := w.Show(bug).Facts[machine.FactAnswer]; got != answer {
			t.Fatalf("ask %d: the report holds answer %q", i+1, got)
		}
		if w.Entry(entry) != nil {
			t.Fatalf("ask %d: the answered entry is still in the inbox", i+1)
		}
	}
	run := w.Dispatch(machine.StageReproducer)
	briefHolds(brief(run), 2)
	r := w.In(run.Checkout, "work", "ask", bug, "--body", "- Which version did you run?")
	if r.Code != 3 || r.RefusalCode() != "ASK_LIMIT" {
		t.Fatalf("the third ask: %s", r)
	}
	wantStatus(w, bug, machine.Reproducing)
}
