//go:build acceptance

package scenarios

import (
	"encoding/json"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// inReview takes a new ticket through implementation to review with green
// CI, and returns its id.
func inReview(w *acceptance.World, title, file string) string {
	w.T.Helper()
	id := createTicket(w, title, "Create `"+file+"` holding the line `"+title+"`.")
	_, head := implement(w, id, map[string]string{file: title + "\n"})
	w.CI(head, "success")
	return id
}

// oneDone checks that item is done and that one write moved it there.
func oneDone(w *acceptance.World, item string) {
	w.T.Helper()
	wantStatus(w, item, machine.Done)
	if n := w.DoneWriters(item); n != 1 {
		w.T.Fatalf("%d writers moved %s to done", n, item)
	}
}

// KTD16: a merge wins from any live state, and done has exactly one
// writer. A run that held the lease, an escalation that waited on the
// operator, and a review room open on the item each settle with no second
// done.
func TestOneDoneWriter(t *testing.T) {
	w := newWorld(t, acceptance.Options{})

	t.Run("merge while a run holds the lease", func(t *testing.T) {
		x := inReview(w, "Merged under review", "x.txt")
		run := w.Dispatch(machine.StageCodeReviewer)
		head := w.Must(w.In(run.Checkout, "pr", "show")).Str("head_sha")
		w.MergeByHand(w.PRFor(x))
		w.React()
		oneDone(w, x)
		r := w.In(run.Checkout, "review", "submit", x, "--verdict", "pass", "--head", head)
		if r.Code != 3 || (r.RefusalCode() != cli.CodeLeaseNotHeld && r.RefusalCode() != cli.CodeTransitionNotAllowed) {
			t.Fatalf("the stale reviewer's verdict = %s", r)
		}
		w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
		w.React()
		w.HandOff()
		oneDone(w, x)
	})

	t.Run("merge while escalated", func(t *testing.T) {
		y := inReview(w, "Merged while escalated", "y.txt")
		run := w.Dispatch(machine.StageCodeReviewer)
		esc := w.Must(w.In(run.Checkout, "escalation", "raise", "--score", "5", "--body",
			"The ticket names a vendor account that no repository document records."))
		id := escalationID(t, esc)
		w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "blocked"))
		routeAs(w, id, panelFor(finding{Level: "owner-only", Gate: "inside", Text: "a vendor account no document records"}))
		wantStatus(w, y, machine.Escalated)
		w.MergeByHand(w.PRFor(y))
		w.React()
		oneDone(w, y)
		if r := w.Coordinator("inbox", "answer", "escalation/"+acceptance.Project+"/"+y, "--body", "Too late."); r.Code == 0 {
			t.Fatalf("an answer on a done ticket landed: %s", r)
		}
		oneDone(w, y)
	})

	t.Run("merge while a review room is open", func(t *testing.T) {
		z := inReview(w, "Merged with a room open", "z.txt")
		room, co := openRoom(w, "review", z, "the operator wants to read this change")
		w.MergeByHand(w.PRFor(z))
		w.React()
		oneDone(w, z)
		dec, _ := json.Marshal(map[string]any{"decision": "Ship it as merged.", "statement": "The operator said it is fine."})
		applied := w.Must(w.In(co, "room", "apply", room, "--file", w.WriteFile("decision.json", string(dec))))
		if applied.Data()["answered"] != false {
			t.Fatalf("the room answered a done ticket: %s", applied)
		}
		w.React()
		w.HandOff()
		oneDone(w, z)
	})
}
