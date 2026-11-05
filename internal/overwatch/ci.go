package overwatch

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
)

// ciPart reads how long the base branch's runs and the merge queue's runs
// took in the flaky window. In a project with health.ci_budget, a newest
// run over the budget is work (spec, C27).
var ciPart = Part{Name: "ci", Read: readCI}

// ciEvents are the events whose runs gate a merge: a push to the base
// branch, and a merge queue's group.
var ciEvents = []string{"push", "merge_group"}

func readCI(ctx context.Context, w World) (Reading, error) {
	if w.GitHub() == nil {
		return noGitHub, nil
	}
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, err
	}
	var r Reading
	for _, p := range projects {
		snap, err := w.ProjectConfig(ctx, p.ID)
		if err != nil {
			r.Notes = append(r.Notes, fmt.Sprintf("UNREAD: %s: read the project config: %v", p.ID, err))
			continue
		}
		var budget time.Duration
		if snap != nil && snap.Config != nil {
			budget = snap.Config.Health.CIBudget
		}
		for _, event := range ciEvents {
			q := github.RunQuery{Event: event, Since: w.Now().Add(-FlakyWindow)}
			if event == "push" {
				q.Branch = p.BaseBranch()
			}
			runs, err := w.GitHub().WorkflowRuns(ctx, p, q)
			if err != nil {
				r.Notes = append(r.Notes, unreadLine(p, "the "+event+" runs", err))
				continue
			}
			r.add(classifyCI(p, event, budget, runs, w.Now()))
		}
	}
	return r, nil
}

// classifyCI reads the completed runs of one event in the flaky window.
// A run's time is from its start to its last update. A cancelled or
// skipped run measures no CI, so it counts for nothing. The newest run
// decides: over a budget it is work, and at the budget it fits. With no
// budget, the lines hold the same reading and no work.
func classifyCI(p config.Project, event string, budget time.Duration, runs []github.RunView, now time.Time) Reading {
	runs = slices.DeleteFunc(slices.Clone(runs), func(run github.RunView) bool {
		return run.Event != event || event == "push" && run.Branch != p.BaseBranch() || run.Status != "completed" ||
			run.Conclusion == "cancelled" || run.Conclusion == "skipped" || run.CreatedAt.Before(now.Add(-FlakyWindow))
	})
	if len(runs) == 0 {
		return Reading{Notes: []string{fmt.Sprintf("%s: no completed %s run in %s", p.ID, event, FlakyWindow)}}
	}
	slices.SortStableFunc(runs, newestFirst)
	took := make([]time.Duration, len(runs))
	for i, run := range runs {
		took[i] = run.UpdatedAt.Sub(run.StartedAt)
	}
	newest := runs[0]
	line := fmt.Sprintf("%s: the newest %s run, %s %d, took %s. Over %d runs in %s: median %s, 90th percentile %s",
		p.ID, event, newest.Workflow, newest.ID, took[0], len(runs), FlakyWindow, percentile(took, 50), percentile(took, 90))
	key := Key("ci", p.ID, Slug(event))
	switch {
	case budget == 0:
		return Reading{Notes: []string{line + ". Project config names no health.ci_budget, so no run is work"}}
	case took[0] > budget:
		return Reading{Findings: []Finding{{Key: key, Project: p.ID, Line: fmt.Sprintf("%s, over the budget of %s. %s", line, budget, key)}}}
	}
	return Reading{Notes: []string{fmt.Sprintf("%s, inside the budget of %s", line, budget)}}
}

// percentile is the nearest-rank p-th percentile of d, which holds at
// least one value.
func percentile(d []time.Duration, p int) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[max(0, (p*len(s)+99)/100-1)]
}
