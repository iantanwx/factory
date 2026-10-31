//go:build acceptance

package scenarios

import (
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// failTwice settles two runs of stage on item with no work done: the
// first finishes failed (the release row), and the second's session ends
// with no finish (the reclaim row). Each leaves the item in want. The
// coordinator then edits the task, so the retry limit admits the stage
// again.
func failTwice(w *acceptance.World, stage, item, want string) {
	w.T.Helper()
	run := w.Dispatch(stage)
	if run.Item != item {
		w.T.Fatalf("the %s took %s, want %s", stage, run.Item, item)
	}
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "failed"))
	wantStatus(w, item, want)
	run = w.Dispatch(stage)
	if run.Item != item {
		w.T.Fatalf("the %s took %s, want %s", stage, run.Item, item)
	}
	crash(w, run)
	wantStatus(w, item, want)
	w.Must(w.Coordinator("work", "edit", item, "--project", acceptance.Project, "--body", "Edited after two "+stage+" failures."))
}

// The epic rows the F4 path does not reach: release and reclaim from each
// state a run holds, a document merged while its review ran (outran), a
// closed document PR, and an escalation a run raises.
func TestEpicRowsEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})

	room, co := openRoom(w, "intake", "", "the operator wants gadgets")
	e1 := submitPRD(w, co, "Gadgets")
	w.Must(w.Coordinator("room", "close", room))
	failTwice(w, machine.StageDocReviewer, e1, machine.PRDReview)

	// outran: the operator merges the PRD while a review runs. The merge is
	// the sign-off too, so the epic moves on to spec_needed.
	run := w.Dispatch(machine.StageDocReviewer)
	w.MergeByHand(docPR(w, e1))
	w.React()
	wantStatus(w, e1, machine.SpecNeeded)
	if r := w.In(run.Checkout, "review", "submit", e1, "--verdict", "pass"); r.Code != 3 {
		t.Fatalf("the outran reviewer's verdict = %s", r)
	}
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))

	failTwice(w, machine.StageSpecAuthor, e1, machine.SpecNeeded)
	draftSpec(w, e1, "draft one")
	failTwice(w, machine.StageDocReviewer, e1, machine.SpecReview)
	run = w.Dispatch(machine.StageDocReviewer)
	w.MergeByHand(docPR(w, e1))
	w.React()
	wantStatus(w, e1, machine.SpecApproved)
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	failTwice(w, machine.StageDecomposer, e1, machine.SpecApproved)

	// A closed PRD PR escalates the epic, and the answer resumes it.
	room, co = openRoom(w, "intake", "", "the operator wants gizmos")
	e2 := submitPRD(w, co, "Gizmos")
	w.Must(w.Coordinator("room", "close", room))
	docReview(w, e2, "reject")
	failTwice(w, machine.StageReviser, e2, machine.PRDAmending)
	w.ClosePR(docPR(w, e2))
	w.React()
	wantStatus(w, e2, machine.Escalated)
	entry := "escalation/" + acceptance.Project + "/" + e2
	if w.Entry(entry) == nil {
		t.Fatal("a closed PRD PR put no inbox entry")
	}
	w.Must(w.Coordinator("inbox", "answer", entry, "--body", "Revise it again.", "--amend"))
	resumed := w.Status(e2)
	if resumed == machine.Escalated {
		t.Fatal("the answer left the epic escalated")
	}

	// escalate: a run on the epic raises a finding only the owner can
	// answer, and the triage panel sends it to the operator.
	stage := map[string]string{machine.PRDReview: machine.StageDocReviewer, machine.PRDAmending: machine.StageReviser}[resumed]
	if stage == "" {
		t.Fatalf("the epic resumed to %s, which no stage here claims", resumed)
	}
	run = w.Dispatch(stage)
	if run.Item != e2 {
		t.Fatalf("the %s took %s", stage, run.Item)
	}
	r := w.Must(w.In(run.Checkout, "escalation", "raise", "--score", "owner-only", "--body", "The PRD needs a fact only the owner holds."))
	if got := routeAs(w, escalationID(t, r), panelFor(finding{Level: "owner-only", Gate: "inside", Text: "a fact only the owner holds"})); got != "person" {
		t.Fatalf("the panel routed to %s", got)
	}
	wantStatus(w, e2, machine.Escalated)
	if r := w.In(run.Checkout, "run", "finish", "--outcome", "blocked"); r.Code != 0 && r.RefusalCode() != cli.CodeLeaseNotHeld {
		t.Fatalf("finish after the escalation = %s", r)
	}
}

// F-55, C-bug-1 and C-bug-2: the coordinator cancels an epic whose work
// moved elsewhere. The epic lands in canceled with the reason, and its
// open document PR closes with no merge.
func TestEpicCancelEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})

	room, co := openRoom(w, "intake", "", "the operator wants sprockets")
	ep := submitPRD(w, co, "Sprockets")
	w.Must(w.Coordinator("room", "close", room))
	pr := docPR(w, ep)
	if pr.State != "open" {
		t.Fatalf("the PRD PR is %s before the cancel", pr.State)
	}

	reason := "ADR 0013 gave the work to another epic."
	w.Must(w.Coordinator("work", "cancel", ep, "--reason", reason, "--project", acceptance.Project))
	wantStatus(w, ep, machine.Canceled)
	if got := w.Show(ep).Facts[machine.FactCancelReason]; got != reason {
		t.Fatalf("cancel_reason = %q, want %q", got, reason)
	}
	pr = w.API.World.PR(pr.Number)
	if pr.State != "closed" || pr.Merged {
		t.Fatalf("the PRD PR is %s, merged %v, want closed with no merge", pr.State, pr.Merged)
	}

	// The reactor reads the closed PR of a terminal epic and changes nothing.
	w.React()
	wantStatus(w, ep, machine.Canceled)
}
