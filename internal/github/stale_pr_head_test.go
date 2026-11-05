package github

import (
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// C-bug-1 (F-78): the loop pushes a new head to a ticket's branch, and
// GitHub drops the push's synchronize event, so the PR keeps the old
// head. Inside the grace period the reactor waits. Past it, the reactor
// sends GitHub a new push to the branch that holds the loop's head, so
// GitHub synchronizes the PR and CI starts.
func TestStalePRHeadAfterTheLoopsPushIsRepaired(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	old := commit(t, e.checkout, "work", map[string]string{"a.txt": "a\n"})
	id := e.ticket(machine.InReview, old)
	if _, err := e.push(id, old); err != nil {
		t.Fatalf("push %s: %v", old, err)
	}
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: old})
	r := e.reactor()

	// The loop's push of next lands on the branch. The fake PR keeps old,
	// as GitHub does when it drops the synchronize event.
	next := commit(t, e.checkout, "Merge main into "+BranchFor(id), map[string]string{"b.txt": "b\n"})
	if _, err := e.push(id, next); err != nil {
		t.Fatalf("push %s: %v", next, err)
	}
	updates := e.recordRefUpdates()

	e.tick(r)
	if got := updates(); len(got) != 0 {
		t.Fatalf("inside the grace period the reactor pushed %v; want it to wait", got)
	}

	start := time.Now()
	e.svc.cfg.Now = func() time.Time { return start.Add(time.Hour) }
	e.tick(r)
	got := updates()
	if len(got) == 0 || got[len(got)-1] != "refs/heads/"+BranchFor(id) {
		t.Fatalf("an hour after the loop pushed %s, the PR still shows %s and the reactor sent no push to %s (ref updates %v)",
			short(next), short(old), BranchFor(id), got)
	}
	head := e.remoteSHA(BranchFor(id))
	gitT(t, e.git.RemoteDir(), "merge-base", "--is-ancestor", next, head)
}

// C-bug-1: a PR whose head already matches the loop's push gets no push,
// however long ago the loop pushed.
func TestPRHeadThatMatchesTheLoopsPushGetsNoPush(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	head := commit(t, e.checkout, "work", map[string]string{"a.txt": "a\n"})
	id := e.ticket(machine.InReview, head)
	if _, err := e.push(id, head); err != nil {
		t.Fatalf("push %s: %v", head, err)
	}
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: head})
	r := e.reactor()
	updates := e.recordRefUpdates()

	start := time.Now()
	e.svc.cfg.Now = func() time.Time { return start.Add(time.Hour) }
	e.tick(r)
	if got := updates(); len(got) != 0 {
		t.Fatalf("the PR shows the loop's head %s, and the reactor pushed %v; want no push", short(head), got)
	}
	if got := e.remoteSHA(BranchFor(id)); got != head {
		t.Fatalf("branch head %s; want %s", short(got), short(head))
	}
}
