package overwatch

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
)

// mainPart reads the newest completed run of each workflow at the head of
// each project's base branch. A red one is work (spec, C24).
var mainPart = Part{Name: "main", Read: readMain}

// redRun reports whether a completed run judged its commit red. cancelled,
// skipped and neutral judged nothing (overwatch-main.ts, alpha, SILENT).
func redRun(conclusion string) bool { return conclusion == "failure" || conclusion == "timed_out" }

func readMain(ctx context.Context, w World) (Reading, error) {
	if w.GitHub() == nil {
		return noGitHub, nil
	}
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, err
	}
	var r Reading
	for _, p := range projects {
		runs, err := w.GitHub().WorkflowRuns(ctx, p, github.RunQuery{Branch: p.BaseBranch()})
		if err != nil {
			r.Notes = append(r.Notes, unreadLine(p, "the workflow runs of "+p.BaseBranch(), err))
			continue
		}
		r.add(classifyMain(p, runs))
	}
	return r, nil
}

// newestFirst orders runs by creation, newest first, and by id when two
// runs share a time.
func newestFirst(a, b github.RunView) int {
	return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
}

// classifyMain reads the base branch's runs. The head is the commit of
// the newest run, since a run is what a push to the branch starts. Each
// workflow's newest completed run at the head decides that workflow. A
// head that moved leaves the runs of the old head unread, so a red old
// head ends there.
func classifyMain(p config.Project, runs []github.RunView) Reading {
	base := p.BaseBranch()
	runs = slices.DeleteFunc(slices.Clone(runs), func(run github.RunView) bool { return run.Branch != base })
	if len(runs) == 0 {
		return Reading{Notes: []string{fmt.Sprintf("%s: no workflow run on %s yet", p.ID, base)}}
	}
	slices.SortStableFunc(runs, newestFirst)
	head := runs[0].HeadSHA
	decided := map[string]github.RunView{}
	var order []string
	for _, run := range runs {
		if run.HeadSHA != head || run.Status != "completed" {
			continue
		}
		if _, ok := decided[run.Workflow]; !ok {
			decided[run.Workflow] = run
			order = append(order, run.Workflow)
		}
	}
	slices.Sort(order)
	var r Reading
	for _, wf := range order {
		run := decided[wf]
		if !redRun(run.Conclusion) {
			r.Notes = append(r.Notes, fmt.Sprintf("%s: %s concluded %s at %s's head %s (run %d)", p.ID, wf, run.Conclusion, base, short12(head), run.ID))
			continue
		}
		key := Key("main", p.ID, Slug(wf)+"@"+short12(head))
		r.Findings = append(r.Findings, Finding{Key: key, Project: p.ID,
			Line: fmt.Sprintf("%s: %s concluded %s at %s's head %s (run %d). The base branch is red. %s",
				p.ID, wf, run.Conclusion, base, short12(head), run.ID, key)})
	}
	if len(order) == 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%s: no workflow run at %s's head %s has completed yet", p.ID, base, short12(head)))
	}
	return r
}

// short12 is the first 12 characters of a sha.
func short12(sha string) string { return sha[:min(12, len(sha))] }
