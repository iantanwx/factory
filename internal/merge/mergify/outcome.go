package mergify

import (
	"context"
	"slices"

	"github.com/iantanwx/loop/internal/merge"
)

// Poll reads the PR's outcome. GitHub reports a merge or a close. A PR
// that holds Mergify's dequeued label is dequeued, and the reason comes
// from what GitHub reports: a conflict with the base, or red CI at the
// head. Any other dequeue, such as a failed batch, reads as other. The
// loop never reads Mergify's message text.
func (m *Mergify) Poll(ctx context.Context, pr merge.PR) (merge.Result, error) {
	cur, err := m.gh.GetPR(ctx, pr.Project, pr.Number)
	if err != nil {
		return merge.Result{}, err
	}
	switch {
	case cur.Merged():
		return merge.Result{Outcome: merge.Merged, SHA: cur.MergeCommitSHA}, nil
	case cur.State != "open":
		return merge.Result{Outcome: merge.Closed}, nil
	}
	labels, err := m.gh.Labels(ctx, pr.Project, pr.Number)
	if err != nil {
		return merge.Result{}, err
	}
	if !slices.Contains(labels, DequeuedLabel) {
		return merge.Result{Outcome: merge.Pending}, nil
	}
	ci, err := m.gh.Checks(ctx, pr.Project, cur.Head.SHA)
	if err != nil {
		return merge.Result{}, err
	}
	return merge.Result{Outcome: merge.Dequeued, Reason: merge.Classify(cur.MergeableState, ci.State)}, nil
}
