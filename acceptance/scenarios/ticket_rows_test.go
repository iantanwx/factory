//go:build acceptance

package scenarios

import (
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// rework starts an implementer run on item, brings its branch in through
// the mirror, commits files, pushes and opens the PR, and finishes. It
// returns the new head.
func rework(w *acceptance.World, item string, files map[string]string) string {
	w.T.Helper()
	run := w.Dispatch(machine.StageImplementer)
	if run.Item != item {
		w.T.Fatalf("the implementer took %s, want %s", run.Item, item)
	}
	syncItem(w, run.Checkout)
	head := w.Commit(run.Checkout, "rework "+item, files)
	w.Must(w.In(run.Checkout, "branch", "push"))
	w.Must(w.In(run.Checkout, "pr", "open", "--title", "Implement "+item))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	return head
}

// queue takes a ready_to_merge ticket to queued: the reactor posts
// eligibility, and the hand-off enqueues it.
func queue(w *acceptance.World, item string) {
	w.T.Helper()
	w.React()
	w.HandOff()
	wantStatus(w, item, machine.Queued)
}

// crash ends a run's session with no finish, as a crashed agent does, and
// lets the scheduler's next tick settle it.
func crash(w *acceptance.World, run acceptance.Run) {
	w.T.Helper()
	w.Host.Exit(run.Session.ID, 1)
	w.Tick()
	if got := w.RunState(run.RunID); got == "running" {
		w.T.Fatalf("run %s still reads running after its session ended", run.RunID)
	}
}

// H5: the implementer finds its ticket's change already on main, and
// closes the ticket with that commit as evidence. The server reads the
// commit back from the base: a commit main does not hold refuses, and
// the ticket keeps the commit and the reason.
func TestSupersedeClosesATicketMainAlreadyAnswers(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	id := createTicket(w, "Ticket D", "Create `d.txt` holding the line `d`.")
	sha := w.PushMain("add d.txt", map[string]string{"d.txt": "d\n"}, "")
	run := w.Dispatch(machine.StageImplementer)
	if run.Item != id {
		t.Fatalf("the implementer took %s, want %s", run.Item, id)
	}
	r := w.In(run.Checkout, "work", "supersede", id, "--commit", "0123abc", "--reason", "Main adds d.txt.")
	if r.RefusalCode() != cli.CodeNotFound {
		t.Fatalf("a commit main lacks: %s", r)
	}
	wantStatus(w, id, machine.InProgress)
	w.Must(w.In(run.Checkout, "work", "supersede", id, "--commit", sha, "--reason", "Main adds d.txt."))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	it := w.Show(id)
	if it.Status != machine.Canceled || it.Fact(machine.FactSupersededBy) != "commit "+sha || it.Owner != "" {
		t.Fatalf("after supersede: %s %v owner %q", it.Status, it.Facts, it.Owner)
	}
}

// The ticket rows the happy path does not reach, each through the path
// that performs it in production: the CLI a run or the coordinator runs,
// the reactor, the merge hand-off, or the scheduler's tick.
func TestTicketRowsEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})

	// A: review_reject, then ci_red, the repair claim and fix_pushed.
	a := createTicket(w, "Ticket A", "Create `a.txt` holding the line `a`.")
	_, head := implement(w, a, map[string]string{"a.txt": "a\n"})
	w.CI(head, "success")
	review(w, a, "reject")
	wantStatus(w, a, machine.Todo)
	head = rework(w, a, map[string]string{"a.txt": "a2\n"})
	wantStatus(w, a, machine.InReview)
	w.CI(head, "failure")
	w.React()
	wantStatus(w, a, machine.Repair)
	head = rework(w, a, map[string]string{"a.txt": "a3\n"})
	wantStatus(w, a, machine.InReview)
	w.Must(w.Coordinator("work", "note", a, "--project", acceptance.Project, "--body", "The operator watches this one."))

	// dequeue_conflict: the base moved under the queued PR.
	w.CI(head, "success")
	review(w, a, "pass")
	queue(w, a)
	w.API.World.Update(w.PRFor(a), func(p *gt.PR) { p.MergeableState = "dirty" })
	w.HandOff()
	wantStatus(w, a, machine.Todo)

	// dequeue_red: CI went red on the queued head.
	w.API.World.Update(w.PRFor(a), func(p *gt.PR) { p.MergeableState = "clean" })
	head = rework(w, a, map[string]string{"a.txt": "a4\n"})
	w.CI(head, "success")
	review(w, a, "pass")
	queue(w, a)
	w.CI(head, "failure")
	w.API.World.Update(w.PRFor(a), func(p *gt.PR) { p.MergeableState = "blocked" })
	w.HandOff()
	wantStatus(w, a, machine.Repair)

	// release and reclaim from repair keep the ticket in repair.
	run := w.Dispatch(machine.StageImplementer)
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "failed"))
	wantStatus(w, a, machine.Repair)
	crash(w, w.Dispatch(machine.StageImplementer))
	wantStatus(w, a, machine.Repair)

	// pr_closed escalates, and the coordinator cancels.
	w.ClosePR(w.PRFor(a))
	w.React()
	wantStatus(w, a, machine.Escalated)
	if w.Entry("escalation/"+acceptance.Project+"/"+a) == nil {
		t.Fatal("a closed PR put no escalation in the inbox")
	}
	w.Must(w.Coordinator("work", "cancel", a, "--project", acceptance.Project))
	wantStatus(w, a, machine.Canceled)

	// B waits in review while C fails twice at implementation.
	b := createTicket(w, "Ticket B", "Create `b.txt` holding the line `b`.")
	implement(w, b, map[string]string{"b.txt": "b\n"})
	wantStatus(w, b, machine.InReview)

	// C: release and reclaim from in_progress return it to todo.
	c := createTicket(w, "Ticket C", "Create `c.txt` holding the line `c`.")
	run = w.Dispatch(machine.StageImplementer)
	if run.Item != c {
		t.Fatalf("the implementer took %s, want %s", run.Item, c)
	}
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "failed"))
	wantStatus(w, c, machine.Todo)
	crash(w, w.Dispatch(machine.StageImplementer))
	wantStatus(w, c, machine.Todo)

	// B: release and reclaim from in_review keep it in review.
	run = w.Dispatch(machine.StageCodeReviewer)
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "failed"))
	wantStatus(w, b, machine.InReview)
	crash(w, w.Dispatch(machine.StageCodeReviewer))
	wantStatus(w, b, machine.InReview)

	// pr_closed, then the operator's answer resumes B to todo.
	w.ClosePR(w.PRFor(b))
	w.React()
	wantStatus(w, b, machine.Escalated)
	w.Must(w.Coordinator("inbox", "answer", "escalation/"+acceptance.Project+"/"+b, "--body", "Reopen it as new work."))
	wantStatus(w, b, machine.Todo)
}
