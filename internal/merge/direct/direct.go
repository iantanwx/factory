// Package direct is the direct merge mechanism (KTD29), for a
// repository with no merge queue. The loop is the queue: a hand-off
// records the PR, and each poll merges it through GitHub's merge API with
// a merge commit. The call carries the reviewed head sha, so GitHub
// refuses the merge when the head moved. A PR whose head lacks the tip of
// the base gets no merge call, because its CI ran on an older base. When
// the base change touches no file of the PR, the mechanism merges the
// base into the head with no conflict, and the PR stays with it until CI
// passes on that merge commit (F-66). A PR that conflicts with the base,
// or that shares a file with the base change, goes to rework.
package direct

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
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
// project, once every required check passed there and the head holds the
// tip of the base. A draft, or a PR whose base is not the project's
// default branch, is dequeued under a reason that names it, with no merge
// call. A PR that conflicts with its base goes to rework with no merge
// call. A head that lacks the tip of the base gets no merge call either:
// mergeBase merges the base into it, or sends it to rework. A head that
// moved is refused by GitHub, and the PR stays pending until the hand-off
// job or the reactor returns the ticket to review. Any other refusal is read
// from what GitHub reports about the PR: a conflict, a branch behind the
// base, or red CI. A refusal while GitHub still computes the mergeable
// state stays pending, and the next poll tries again.
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
	if res, ok, err := d.refused(ctx, pr.Project, cur); err != nil || ok {
		return res, err
	}
	if res, ok := merge.Conflicting(cur); ok {
		return res, nil
	}
	// No queue tests the PR on the current base here. A head that lacks
	// the tip of the base has CI from an older base, so it gets no merge
	// call.
	behind, err := d.gh.Behind(ctx, pr.Project, cur.Base.Ref, pr.Head)
	if err != nil {
		return merge.Result{}, err
	}
	if behind {
		return d.mergeBase(ctx, pr, cur.Base.Ref)
	}
	// A repository with no rulesets lets any merge through, so the loop
	// reads CI on the reviewed head itself before it merges. Each
	// required check must have passed there: a head with no check, or
	// one whose required check has not reported, waits and never merges.
	required, err := d.gh.RequiredChecks(ctx, pr.Project)
	if err != nil {
		return merge.Result{}, err
	}
	ci, err := d.gh.Checks(ctx, pr.Project, pr.Head)
	if err != nil {
		return merge.Result{}, err
	}
	switch v := ci.Against(required); v.State {
	case github.CIRed:
		return merge.Result{Outcome: merge.Dequeued, Reason: merge.Red, Detail: "CI failed on the reviewed head"}, nil
	case github.CIPending:
		return merge.Result{Outcome: merge.Pending,
			Detail: "CI has not passed on the reviewed head: " + strings.Join(v.Waiting, ", ")}, nil
	}
	// A ticket's merge of a moved base takes loop/eligible on a later
	// reactor tick than the hand-off, and a ruleset that requires it
	// refuses the merge until then. A settings change posts its own.
	if pr.Item != "" {
		if ok, err := d.gh.EligiblePosted(ctx, pr.Project.ID, pr.Head); err != nil || !ok {
			return merge.Result{Outcome: merge.Pending, Detail: github.EligibleContext + " is not posted on the reviewed head yet"}, err
		}
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
		return d.mergeBase(ctx, pr, fresh.Base.Ref)
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

// mergeBase merges base into the reviewed head of a PR that lacks its
// tip. A base change that touches a file of the PR may change what the
// review passed, so it goes to rework with no merge, as a conflict does.
// GitHub's compare lists at most 300 files, and a list at that cap goes
// to rework too.
func (d *Direct) mergeBase(ctx context.Context, pr merge.PR, base string) (merge.Result, error) {
	if pr.Item == "" {
		// A settings change has no item branch to merge the base into.
		return merge.Result{Outcome: merge.Dequeued, Reason: merge.Conflict,
			Detail: "the head lacks the tip of " + base + ", so its CI ran on an older base"}, nil
	}
	changed, complete, err := d.gh.BaseChanges(ctx, pr.Project, base, pr.Head)
	if err != nil {
		return merge.Result{}, err
	}
	if !complete {
		return merge.Result{Outcome: merge.Dequeued, Reason: merge.Conflict,
			Detail: "the base changed more files since the reviewed head than GitHub's compare lists"}, nil
	}
	files, err := d.gh.PRFiles(ctx, pr.Project, pr.Number)
	if err != nil {
		return merge.Result{}, err
	}
	for _, f := range files {
		if slices.Contains(changed, f.Path) || (f.Previous != "" && slices.Contains(changed, f.Previous)) {
			return merge.Result{Outcome: merge.Dequeued, Reason: merge.Conflict,
				Detail: "the base changed " + f.Path + ", a file of the PR, since the reviewed head"}, nil
		}
	}
	sha, err := d.gh.MergeBase(ctx, pr.Project, pr.Item, base, pr.Head)
	var api *github.APIError
	switch {
	case errors.As(err, &api) && api.Status == http.StatusConflict:
		return merge.Result{Outcome: merge.Dequeued, Reason: merge.Conflict, Detail: "the base conflicts with the reviewed head"}, nil
	case errors.Is(err, github.ErrHeadMoved):
		return merge.Result{Outcome: merge.Pending, Detail: "the head moved after review"}, nil
	case err != nil:
		return merge.Result{}, err
	case sha == pr.Head:
		return merge.Result{Outcome: merge.Pending, Detail: "the reviewed head already holds the tip of " + base}, nil
	}
	return merge.Result{Outcome: merge.BaseMerged, SHA: sha, Detail: "merged " + base + " into the reviewed head"}, nil
}

// refused is the result of an open PR that the loop must not merge as it
// stands: a draft, or a PR whose base is not the project's default
// branch. ok is false for any other PR. GitHub refuses a draft with no
// reason the loop reads, and it merges a PR into any base, where the
// merge never moves the ticket to done. So the refusal comes before the
// merge call and names its reason.
func (d *Direct) refused(ctx context.Context, p config.Project, cur github.PR) (res merge.Result, ok bool, err error) {
	if cur.Draft {
		return merge.Result{Outcome: merge.Dequeued, Reason: merge.Draft, Detail: "the PR is a draft"}, true, nil
	}
	base, err := d.gh.DefaultBranch(ctx, p)
	if err != nil {
		return merge.Result{}, false, err
	}
	if cur.Base.Ref != base {
		return merge.Result{Outcome: merge.Dequeued, Reason: merge.WrongBase,
			Detail: fmt.Sprintf("the PR merges into %s, not %s", cur.Base.Ref, base)}, true, nil
	}
	return merge.Result{}, false, nil
}
