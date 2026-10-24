// Package ghqueue is the github-queue merge mechanism (KTD29). A
// hand-off adds the PR to GitHub's merge queue with the App token, at the
// reviewed head only. GitHub checks required statuses on the merge-group
// commit, not on the PR head, so the sweep posts loop/eligible on a group
// commit only when every PR in the group is eligible at its queued head.
package ghqueue

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/merge"
)

// RuleType is the branch rule GitHub's merge queue needs.
const RuleType = "merge_queue"

// Queue is the mechanism.
type Queue struct {
	gh *github.Service

	mu sync.Mutex
	// posted holds the group commits the sweep already marked, so a group
	// gets one status post. A restart posts once more, and a status post
	// is idempotent by sha and context.
	posted map[string]map[string]bool // project -> group sha
}

// New returns the mechanism over the server's GitHub actor.
func New(gh *github.Service) *Queue { return &Queue{gh: gh, posted: map[string]map[string]bool{}} }

// Name implements merge.Mechanism.
func (q *Queue) Name() config.MergeMechanism { return config.MergeGitHubQueue }

// Check refuses a default branch with no merge-queue rule, or one whose
// ruleset leaves out a required check (github.RequiredChecks): GitHub's
// queue merges once the ruleset's checks pass, and the loop cannot add
// one. It asserts the rules, not a response layout.
func (q *Queue) Check(ctx context.Context, p config.Project) error {
	types, rules, err := q.gh.BranchRules(ctx, p)
	if err != nil {
		return err
	}
	required, err := q.gh.RequiredChecks(ctx, p)
	if err != nil {
		return err
	}
	missing := slices.DeleteFunc(required, func(c string) bool { return slices.Contains(rules, c) })
	if slices.Contains(types, RuleType) && len(missing) == 0 {
		return nil
	}
	base, err := q.gh.DefaultBranch(ctx, p)
	if err != nil {
		return err
	}
	var lines []string
	if !slices.Contains(types, RuleType) {
		lines = append(lines, fmt.Sprintf("- %s: the merge mechanism github-queue needs a %s rule on %s: "+
			"turn on \"Require merge queue\" for %s, with the merge method set to merge commits", RuleType, RuleType, base, base))
	}
	if len(missing) > 0 {
		lines = append(lines, fmt.Sprintf("- required_status_checks: the merge queue merges unverified code on %s unless the ruleset "+
			"requires %s: add each to \"Require status checks to pass\" for %s", base, strings.Join(missing, ", "), base))
	}
	return &github.RulesError{Lines: lines}
}

const entryQuery = `query LoopQueueEntry($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) { id headRefOid mergeQueueEntry { state } }
  }
}`

const enqueueMutation = `mutation LoopEnqueue($pr: ID!, $head: GitObjectID!) {
  enqueuePullRequest(input: {pullRequestId: $pr, expectedHeadOid: $head}) { mergeQueueEntry { state } }
}`

const dequeueMutation = `mutation LoopDequeue($pr: ID!) {
  dequeuePullRequest(input: {id: $pr}) { mergeQueueEntry { state } }
}`

type entry struct {
	id     string
	head   string
	queued bool
}

func (q *Queue) entry(ctx context.Context, pr merge.PR) (entry, error) {
	owner, name, _ := strings.Cut(pr.Project.Repo, "/")
	var out struct {
		Repository struct {
			PullRequest *struct {
				ID              string                  `json:"id"`
				HeadRefOid      string                  `json:"headRefOid"`
				MergeQueueEntry *struct{ State string } `json:"mergeQueueEntry"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := q.gh.GraphQL(ctx, pr.Project, github.ScopeRead, entryQuery,
		map[string]any{"owner": owner, "name": name, "number": pr.Number}, &out); err != nil {
		return entry{}, err
	}
	p := out.Repository.PullRequest
	if p == nil || p.ID == "" {
		return entry{}, fmt.Errorf("ghqueue: GitHub names no PR #%d", pr.Number)
	}
	return entry{id: p.ID, head: p.HeadRefOid, queued: p.MergeQueueEntry != nil}, nil
}

// HandOff adds the PR to the merge queue at pr.Head. GitHub refuses the
// add when the head moved. A PR already in the queue is left there.
func (q *Queue) HandOff(ctx context.Context, pr merge.PR) error {
	e, err := q.entry(ctx, pr)
	if err != nil || e.queued {
		return err
	}
	return q.gh.GraphQL(ctx, pr.Project, github.ScopeMerge, enqueueMutation, map[string]any{"pr": e.id, "head": pr.Head}, nil)
}

// Requeue adds a dequeued PR to the queue again at the same head.
func (q *Queue) Requeue(ctx context.Context, pr merge.PR) error { return q.HandOff(ctx, pr) }

// Withdraw removes the PR from the merge queue. GitHub removes a PR on a
// push by itself, so a PR not in the queue is no fault.
func (q *Queue) Withdraw(ctx context.Context, pr merge.PR) error {
	e, err := q.entry(ctx, pr)
	if err != nil || !e.queued {
		return err
	}
	return q.gh.GraphQL(ctx, pr.Project, github.ScopeMerge, dequeueMutation, map[string]any{"pr": e.id}, nil)
}

// Poll reads the PR's outcome. A PR that conflicts with its base is
// dequeued as a conflict, whether or not GitHub still lists it in the
// queue. A PR that is open, unmerged and no longer in the queue was
// dequeued, and the reason comes from what GitHub reports about it: a
// conflict with the base, or red CI at the head.
func (q *Queue) Poll(ctx context.Context, pr merge.PR) (merge.Result, error) {
	cur, err := q.gh.GetPR(ctx, pr.Project, pr.Number)
	if err != nil {
		return merge.Result{}, err
	}
	if res, ok := merge.Terminal(cur); ok {
		return res, nil
	}
	if res, ok := merge.Conflicting(cur); ok {
		return res, nil
	}
	e, err := q.entry(ctx, pr)
	if err != nil {
		return merge.Result{}, err
	}
	if e.queued {
		return merge.Result{Outcome: merge.Pending}, nil
	}
	ci, err := q.gh.Checks(ctx, pr.Project, cur.Head.SHA)
	if err != nil {
		return merge.Result{}, err
	}
	return merge.Classify(cur.MergeableState, ci.State), nil
}
