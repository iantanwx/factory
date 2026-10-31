//go:build acceptance

package scenarios

import (
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/machine"
)

// factorio-ku99 C21 and C22, and the answer to PRD open question 4: the
// doc-reviewer's PRD pass names the requirements that pass the ADR test,
// and the operator's merge then lands the epic on architecture. The
// coordinator's --off at sign-off clears the list, and the merge lands the
// epic on spec_needed.
func TestPRDReviewADRNeededEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})

	e1 := adrNeededPass(w, "Widgets")
	w.MergeByHand(docPR(w, e1))
	w.React()
	wantStatus(w, e1, machine.Architecture)

	e2 := adrNeededPass(w, "Gizmos")
	w.Must(w.Coordinator("epic", "adr", e2, "--project", acceptance.Project, "--off", "--request", "Ian: gizmos need no ADR."))
	wantStatus(w, e2, machine.PRDSignoff)
	if got := w.Show(e2).Facts[machine.FactADRNeeded]; got != "" {
		t.Fatalf("adr_needed = %q after --off, want none", got)
	}
	w.MergeByHand(docPR(w, e2))
	w.React()
	wantStatus(w, e2, machine.SpecNeeded)
}

// adrNeededPass submits a PRD and passes its review with --adr-needed.
func adrNeededPass(w *acceptance.World, title string) string {
	w.T.Helper()
	room, co := openRoom(w, "brainstorm", "", "the operator wants "+title)
	epic := submitPRD(w, co, title)
	w.Must(w.Coordinator("room", "close", room))
	run := w.Dispatch(machine.StageDocReviewer)
	if run.Item != epic {
		w.T.Fatalf("the doc-reviewer took %s, want %s", run.Item, epic)
	}
	head := w.Must(w.In(run.Checkout, "pr", "show")).Str("head_sha")
	w.Must(w.In(run.Checkout, "review", "submit", epic, "--verdict", "pass", "--head", head, "--body", "Read the document: pass.",
		"--adr-needed", "R1 binds every later epic"))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	wantStatus(w, epic, machine.PRDSignoff)
	if got := w.Show(epic).Facts[machine.FactADRNeeded]; got != "R1 binds every later epic" {
		w.T.Fatalf("adr_needed = %q after the pass", got)
	}
	return epic
}
