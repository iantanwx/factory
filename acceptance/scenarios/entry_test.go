//go:build acceptance

package scenarios

import (
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
// architecture until a merge to the base adds an ADR that names it. The
// reactor then moves it to spec_needed, and the spec author takes it.
func TestArchitectureEpicWaitsForItsADR(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	r := w.Must(w.Coordinator("epic", "create", "--architecture", "--project", acceptance.Project,
		"--title", "Move the widget store to Postgres", "--body", "The operator asked for an ADR on the widget store."))
	epic := r.Str("epic")
	wantStatus(w, epic, machine.Architecture)

	w.PushMain("notes", map[string]string{"docs/notes.md": "Epic " + epic + " moves the store.\n"}, "")
	w.React()
	wantStatus(w, epic, machine.Architecture)

	w.PushMain("ADR 1", map[string]string{"docs/adr/0001-widget-store.md": "# 1. Widget store\n\nEpic: " + epic + "\n\nUse Postgres.\n"}, "")
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
