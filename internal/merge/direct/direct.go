// Package direct is the direct merge mechanism (KTD29), for a
// repository with no merge queue. The loop is the queue: a hand-off
// records the PR, and each poll merges it through GitHub's merge API with
// a merge commit. The call carries the reviewed head sha, so GitHub
// refuses the merge when the head moved. A PR that GitHub refuses because
// it conflicts or is behind the base goes to rework. On a busy base this
// costs another review and another CI run for each merge, and that cost
// is accepted for a repository with no queue.
package direct

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/merge"
)

// Direct is the mechanism.
//
// One merge is in flight for each project. The lock is an in-process
// mutex: one server process owns the instance (the instance lock), and a
// merge call is one synchronous request inside a poll, so nothing needs
// holding across a crash. A crash during the call leaves an unknown
// answer, which the next poll or the reactor reads from GitHub as merged
// or not. A stage mutex in SQLite would add a lease to clean up after
// that crash and protect nothing more.
type Direct struct {
	gh *github.Service

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New returns the mechanism over the server's GitHub actor.
func New(gh *github.Service) *Direct { return &Direct{gh: gh, locks: map[string]*sync.Mutex{}} }

// Name implements merge.Mechanism.
func (d *Direct) Name() config.MergeMechanism { return config.MergeDirect }

// Check needs nothing beyond the branch rules that the GitHub hook checks.
func (d *Direct) Check(context.Context, config.Project) error { return nil }

// HandOff records nothing outside the loop: the hand-off job's record is
// the queue, and the next poll merges.
func (d *Direct) HandOff(context.Context, merge.PR) error { return nil }

// Requeue lets the next poll try the merge again.
func (d *Direct) Requeue(context.Context, merge.PR) error { return nil }

// Withdraw has nothing to take back: no merge is pending outside a poll.
func (d *Direct) Withdraw(context.Context, merge.PR) error { return nil }

func (d *Direct) lock(project string) func() {
	d.mu.Lock()
	l, ok := d.locks[project]
	if !ok {
		l = &sync.Mutex{}
		d.locks[project] = l
	}
	d.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Poll merges the PR at its reviewed head, one merge at a time for each
// project. A PR that conflicts with its base goes to rework with no merge
// call. A head that moved is refused by GitHub, and the PR stays pending
// until the hand-off job or the reactor returns the ticket to review. Any
// other refusal is read from what GitHub reports about the PR: a
// conflict, a branch behind the base, or red CI. A refusal while GitHub
// still computes the mergeable state stays pending, and the next poll
// tries again.
func (d *Direct) Poll(ctx context.Context, pr merge.PR) (merge.Result, error) {
	unlock := d.lock(pr.Project.ID)
	defer unlock()
	cur, err := d.gh.GetPR(ctx, pr.Project, pr.Number)
	if err != nil {
		return merge.Result{}, err
	}
	if res, ok := merge.Terminal(cur); ok {
		return res, nil
	}
	if res, ok := merge.Conflicting(cur); ok {
		return res, nil
	}
	// A repository with no rulesets lets any merge through, so the loop
	// reads CI on the reviewed head itself before it merges.
	ci, err := d.gh.Checks(ctx, pr.Project, pr.Head)
	if err != nil {
		return merge.Result{}, err
	}
	switch ci.State {
	case github.CIRed:
		return merge.Result{Outcome: merge.Dequeued, Reason: merge.Red, Detail: "CI failed on the reviewed head"}, nil
	case github.CIPending:
		return merge.Result{Outcome: merge.Pending, Detail: "CI still runs on the reviewed head"}, nil
	}
	sha, err := d.gh.MergePR(ctx, pr.Project, pr.Number, pr.Head)
	if err == nil {
		return merge.Result{Outcome: merge.Merged, SHA: sha}, nil
	}
	var api *github.APIError
	if !errors.As(err, &api) || api.Transient() {
		return merge.Result{}, err // an unknown answer: the next poll reads GitHub again
	}
	if api.Status == http.StatusConflict {
		return merge.Result{Outcome: merge.Pending, Detail: "the head moved after review"}, nil
	}
	fresh, err := d.gh.GetPR(ctx, pr.Project, pr.Number)
	if err != nil {
		return merge.Result{}, err
	}
	if fresh.Head.SHA != pr.Head {
		return merge.Result{Outcome: merge.Pending, Detail: "the head moved after review"}, nil
	}
	if fresh.MergeableState == merge.MergeableBehind {
		// The base's rules need an up-to-date branch. No queue tests
		// the PR on the current base here, so only a merge of the base
		// into the branch lands it.
		return merge.Result{Outcome: merge.Dequeued, Reason: merge.Conflict, Detail: api.Message}, nil
	}
	ci, err = d.gh.Checks(ctx, pr.Project, fresh.Head.SHA)
	if err != nil {
		return merge.Result{}, err
	}
	res := merge.Classify(fresh.MergeableState, ci.State)
	if res.Outcome == merge.Dequeued {
		res.Detail = api.Message
	}
	return res, nil
}
