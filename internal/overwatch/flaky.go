package overwatch

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
)

// flakyPart reads the reruns of the flaky window. A job that failed in
// one attempt and passed in a later one is work (spec, C28).
var flakyPart = Part{Name: "flaky", Read: readFlaky}

// FlakyWindow is how far back the flaky and ci parts read runs
// (FLAKY_WINDOW_HOURS, flaky-tests.ts, alpha).
const FlakyWindow = 48 * time.Hour

func readFlaky(ctx context.Context, w World) (Reading, error) {
	if w.GitHub() == nil {
		return noGitHub, nil
	}
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, err
	}
	var r Reading
projects:
	for _, p := range projects {
		runs, err := w.GitHub().WorkflowRuns(ctx, p, github.RunQuery{Since: w.Now().Add(-FlakyWindow)})
		if err != nil {
			r.Notes = append(r.Notes, unreadLine(p, "the workflow runs", err))
			continue
		}
		runs = reruns(runs, w.Now())
		jobs := map[int64][][]github.JobView{}
		for _, run := range runs {
			for a := 1; a <= run.Attempt; a++ {
				js, err := w.GitHub().RunJobs(ctx, p, run.ID, a)
				if err != nil {
					r.Notes = append(r.Notes, unreadLine(p, fmt.Sprintf("the jobs of attempt %d of run %d", a, run.ID), err))
					continue projects
				}
				jobs[run.ID] = append(jobs[run.ID], js)
			}
		}
		r.add(classifyFlaky(p, runs, jobs))
	}
	return r, nil
}

// reruns keeps the completed runs of the flaky window that hold more than
// one attempt, newest first.
func reruns(runs []github.RunView, now time.Time) []github.RunView {
	runs = slices.DeleteFunc(slices.Clone(runs), func(run github.RunView) bool {
		return run.Attempt < 2 || run.Status != "completed" || run.CreatedAt.Before(now.Add(-FlakyWindow))
	})
	slices.SortStableFunc(runs, newestFirst)
	return runs
}

// classifyFlaky reads each rerun's jobs, attempt by attempt: jobs[id][0]
// is attempt 1 of run id. A job that concluded red in one attempt and
// success in a later one flaked. Every attempt of a run has the run's
// head. A job that never passed is main's (C24), so it holds no work
// here. The key leaves the head out, so one job keeps one key, and the
// newest flake of it gives the line.
func classifyFlaky(p config.Project, runs []github.RunView, jobs map[int64][][]github.JobView) Reading {
	var r Reading
	seen := map[string]bool{}
	for _, run := range runs {
		red := map[string]int{} // a job's name, and the first attempt it failed
		for a, attempt := range jobs[run.ID] {
			for _, j := range attempt {
				failed, ok := red[j.Name]
				switch {
				case !ok && redRun(j.Conclusion):
					red[j.Name] = a + 1
				case ok && j.Conclusion == "success":
					key := Key("flaky", p.ID, Slug(run.Workflow)+"/"+Slug(j.Name))
					if seen[key] {
						continue
					}
					seen[key] = true
					r.Findings = append(r.Findings, Finding{Key: key,
						Line: fmt.Sprintf("%s: job %q of %s failed in attempt %d and passed in attempt %d of run %d at head %s. %s",
							p.ID, j.Name, run.Workflow, failed, a+1, run.ID, short12(run.HeadSHA), key)})
				}
			}
		}
	}
	if len(r.Findings) == 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%s: no job failed and then passed at one head in %s (%d reruns read)", p.ID, FlakyWindow, len(runs)))
	}
	return r
}
