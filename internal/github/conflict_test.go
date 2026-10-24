package github

import (
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// 5.10 (F-11): a PR in review that conflicts with its base goes to repair
// before any review. GitHub starts no pull_request workflow on a
// conflicting PR, so its head has no check, CI never turns green, and no
// review can pass to reach the hand-off that reads the conflict. Ported
// from health in scripts/sdlc/pr-health.ts (alpha): CONFLICTING is red.
func TestConflictingPRInReviewGoesToRepair(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InReview, shaB)
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaB, MergeableState: "dirty"})
	e.tick(e.reactor())
	if got := e.status(id).Status; got != machine.Repair {
		t.Fatalf("a dirty PR with no checks left %s in %s, want repair", id, got)
	}
}

// 5.10: a conflict that comes after a green build is red too. The base
// moved on under a reviewed-clean head, and only a merge of the base
// repairs it.
func TestConflictAfterGreenCIGoesToRepair(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InReview, shaB)
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaB, MergeableState: "dirty"})
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	w.SetRuns(shaB, gt.CheckRun("ci", shaB, "completed", "success", t0))
	e.tick(e.reactor())
	if got := e.status(id).Status; got != machine.Repair {
		t.Fatalf("a dirty PR with green CI left %s in %s, want repair", id, got)
	}
}

// 5.10: unknown is no conflict. GitHub computes the state after a read
// asks for it, so the ticket waits in review for the next tick. A PR only
// behind its base has no conflict either: the merge mechanism tests it on
// the current base.
func TestUnknownOrBehindPRStaysInReview(t *testing.T) {
	for _, state := range []string{"unknown", "behind", "clean"} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t)
			w := e.withWorld()
			id := e.ticket(machine.InReview, shaB)
			w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaB, MergeableState: state})
			e.tick(e.reactor())
			if got := e.status(id).Status; got != machine.InReview {
				t.Fatalf("a PR whose mergeable state is %s moved %s to %s, want it held in review", state, id, got)
			}
		})
	}
}

// 5.10: a conflict wins over a check that GitHub never started. The
// conflict is what holds the PR, and a merge of the base repairs it, so
// the ticket goes to repair and raises no ci_not_started alert.
func TestConflictBesideAnUnstartedCheckGoesToRepair(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InReview, shaB)
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaB, MergeableState: "dirty"})
	const run = 9301
	w.SetRuns(shaB, notStartedRun(run, shaB, time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)))
	serveNotStarted(e, run, shaB)
	e.tick(e.reactor())
	if got := e.status(id).Status; got != machine.Repair {
		t.Fatalf("a dirty PR beside a check that never started left %s in %s, want repair", id, got)
	}
	if n := e.alerts(AlertCINotStarted); n != 0 {
		t.Fatalf("%d ci_not_started alerts for a PR that conflicts", n)
	}
}
