// Package merge lands eligible PRs through the project's merge mechanism
// (R51, KTD29). A project's config names one of three mechanisms:
// mergify, github-queue or direct. Each one hands off a PR only at the head
// a review passed, with loop/eligible posted on that head (KTD15). Each one
// maps its own events onto three outcomes, merged, dequeued with a reason
// or closed, so every mechanism reaches the same ticket transitions.
package merge

import (
	"context"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
)

// Outcome is where a handed-off PR stands in its mechanism.
type Outcome string

// The outcomes. Pending means the mechanism still holds the PR.
const (
	Pending  Outcome = "pending"
	Merged   Outcome = "merged"
	Dequeued Outcome = "dequeued"
	Closed   Outcome = "closed"
)

// Reason says why a mechanism dequeued a PR.
type Reason string

// The dequeue reasons. Conflict sends the ticket to todo with entry
// rework, Red sends it to repair, and Other re-queues it once for each
// head and then escalates it.
const (
	Conflict Reason = "conflict"
	Red      Reason = "ci_red"
	Other    Reason = "other"
)

// Result is one read of a handed-off PR.
type Result struct {
	Outcome Outcome
	Reason  Reason // set when Outcome is Dequeued
	SHA     string // the merge commit, when Outcome is Merged
	Detail  string
}

// PR is one ticket's PR as the merge interface sees it.
type PR struct {
	Project config.Project
	Item    string
	Number  int
	// Head is the head the review passed at. A mechanism hands off this
	// head and no other.
	Head string
	Base string // the branch the PR merges into
}

// Mechanism is one way to land an eligible PR (KTD29).
type Mechanism interface {
	Name() config.MergeMechanism
	// Check is the `project add` check of the mechanism's needs. A
	// failure names what the operator changes. It must be safe to run
	// again.
	Check(ctx context.Context, p config.Project) error
	// HandOff gives the mechanism one PR at pr.Head. It is safe to run
	// again on a PR the mechanism already holds.
	HandOff(ctx context.Context, pr PR) error
	// Requeue hands off a dequeued PR again at the same head.
	Requeue(ctx context.Context, pr PR) error
	// Withdraw takes the PR back from the mechanism. A PR the mechanism
	// does not hold is no fault.
	Withdraw(ctx context.Context, pr PR) error
	// Poll reads the PR's outcome. The direct mechanism merges here, one
	// merge at a time for each project. An unknown answer is an error,
	// never a merge.
	Poll(ctx context.Context, pr PR) (Result, error)
}

// Sweeper is a mechanism with work of its own on each tick of the
// hand-off job: github-queue posts loop/eligible on merge-group commits.
// eligible reports whether PR number is eligible at its queued head.
type Sweeper interface {
	Sweep(ctx context.Context, p config.Project, eligible func(ctx context.Context, number int) (bool, error)) error
}

// Terminal is the result of a pull request that GitHub already merged or
// closed. ok is false while the pull request is open.
func Terminal(cur github.PR) (res Result, ok bool) {
	switch {
	case cur.Merged():
		return Result{Outcome: Merged, SHA: cur.MergeCommitSHA}, true
	case cur.State != "open":
		return Result{Outcome: Closed}, true
	}
	return Result{}, false
}

// Classify maps what GitHub reports about a dequeued PR to a reason: a
// PR that conflicts with its base or is behind it is a conflict, red CI
// at its head is red, and anything else is other. It reads GitHub's
// outcomes, never a vendor's message text.
func Classify(mergeableState, ci string) Reason {
	switch {
	case mergeableState == "dirty" || mergeableState == "behind":
		return Conflict
	case ci == github.CIRed:
		return Red
	}
	return Other
}
