package mergify

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/merge"
)

// Poll reads the PR's outcome. GitHub reports a merge or a close. A PR
// that conflicts with its base is dequeued as a conflict, with or without
// Mergify's dequeued label: the template's -conflict condition keeps
// Mergify from queueing it, so Mergify never dequeues it either. A PR
// that holds the dequeued label is dequeued, and the reason comes from
// what GitHub reports: a conflict with the base, or red CI at the head.
// Any other dequeue, such as a failed batch, reads as other. The loop
// never reads Mergify's message text, except the one config check below.
//
// A broken queue config never queues and never dequeues a PR, so the PR
// would wait forever. Poll reads Mergify's Configuration changed check at
// the head first, and a broken config is a dequeue for merge config.
func (m *Mergify) Poll(ctx context.Context, pr merge.PR) (merge.Result, error) {
	cur, err := m.gh.GetPR(ctx, pr.Project, pr.Number)
	if err != nil {
		return merge.Result{}, err
	}
	if res, ok := merge.Terminal(cur); ok {
		return res, nil
	}
	if res, ok := merge.Conflicting(cur); ok {
		return res, nil
	}
	runs, err := m.gh.CheckRunsNamed(ctx, pr.Project, cur.Head.SHA, ConfigCheck)
	if err != nil {
		return merge.Result{}, err
	}
	if detail := configBroken(runs); detail != "" {
		return merge.Result{Outcome: merge.Dequeued, Reason: merge.MergeConfig, Detail: detail}, nil
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
	return merge.Classify(cur.MergeableState, ci.State), nil
}

// mergifyApp is the slug of Mergify's GitHub App. A check of the same
// name from any other App is not Mergify's word.
const mergifyApp = "mergify"

// unusedConfig matches Mergify's warnings that a config it accepted will
// not run: a rule it ignores, or a feature the plan lacks. A queue rule
// so disabled held #2567 in the queue until the product was activated
// (alpha mergify-client.ts).
var unusedConfig = regexp.MustCompile(`(?i)will not be used|needs to be activated|not supported|not available`)

// configBroken says why Mergify's Configuration changed check reports a
// broken config, or "" when it reports none. A head with no such check
// changed no config, and a check that still runs has no verdict yet.
func configBroken(runs []github.CheckRun) string {
	for _, r := range runs {
		if r.App.Slug != mergifyApp || r.Status != "completed" {
			continue
		}
		if r.Conclusion != "success" {
			return fmt.Sprintf("Mergify's %s check concluded %q at this head", ConfigCheck, r.Conclusion)
		}
		if unusedConfig.MatchString(strings.Join([]string{r.Output.Title, r.Output.Summary, r.Output.Text}, "\n")) {
			return fmt.Sprintf("Mergify reports unused or unavailable config; resolve its %s check", ConfigCheck)
		}
	}
	return ""
}
