//go:build acceptance

package scenarios

import (
	"slices"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// F1: a ticket from claim to merge. The server admits the ticket and takes
// its lease, and a scripted agent that reads only its brief and the schema
// pushes and opens the PR through the server, with no GitHub or push
// credential of its own. A scripted reviewer passes at the head. The
// reactor posts eligibility for green CI at the reviewed head, the merge
// interface hands the PR to the project's mechanism, and the server marks
// the ticket done when the merge lands: one writer for each move.
func TestF1ScriptedAgentTakesATicketFromClaimToMerge(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	id := createTicket(w, "Add a greeting", "Create `greeting.txt` holding the line `hello, world`.")

	run := w.Dispatch(machine.StageImplementer)
	if run.Item != id || w.Show(id).Owner != run.RunID {
		t.Fatalf("the implementer run %s took %s, and the lease is %s's", run.RunID, run.Item, w.Show(id).Owner)
	}
	launches := w.Host.Launches()
	if last := launches[len(launches)-1]; last.Spec.Checkout != run.Checkout || last.Spec.PromptFile == "" {
		t.Fatalf("the fake host started %+v", last.Spec)
	}
	head := startAgent(w, run.Checkout).implement()
	it := w.Show(id)
	if it.Status != machine.InReview || it.Fact(machine.FactHead) != head || it.Owner != "" {
		t.Fatalf("after the implementer: %s head %s owner %q", it.Status, it.Fact(machine.FactHead), it.Owner)
	}
	if w.Git.RemoteSHA("loop/"+id) != head {
		t.Fatal("the server did not push the run's head")
	}

	w.CI(head, "success")
	w.React()
	wantStatus(w, id, machine.InReview)

	rev := w.Dispatch(machine.StageCodeReviewer)
	if rev.Item != id {
		t.Fatalf("the code-reviewer took %s", rev.Item)
	}
	if got := startAgent(w, rev.Checkout).review("pass"); got != head {
		t.Fatalf("the reviewer read head %s, want %s", got, head)
	}
	if it := w.Show(id); it.Status != machine.ReadyToMerge || it.Fact(machine.FactReviewedHead) != head {
		t.Fatalf("after the pass: %s reviewed %s", it.Status, it.Fact(machine.FactReviewedHead))
	}

	w.React()
	if !slices.Contains(w.API.World.Posted(), head+" "+gt.Eligible+" success") {
		t.Fatalf("no eligibility at the reviewed head: %v", w.API.World.Posted())
	}
	w.HandOff()
	wantStatus(w, id, machine.Queued)
	w.HandOff()
	w.React()
	wantStatus(w, id, machine.Done)
	merges := w.API.World.Merges()
	if len(merges) != 1 || merges[0].SHA != head || merges[0].Status != 200 {
		t.Fatalf("merges = %+v", merges)
	}
	if n := w.DoneWriters(id); n != 1 {
		t.Fatalf("%d writers moved %s to done", n, id)
	}
}
