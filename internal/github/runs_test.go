package github

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

func runCalls(e *env, path string) []string {
	var out []string
	for _, c := range e.api.Calls() {
		if strings.HasPrefix(c, "GET /repos/"+testRepo+path+"?") {
			out = append(out, c)
		}
	}
	return out
}

// WorkflowRuns decodes each run as the GitHub parts read it, with the
// workflow named by its file, and filters by the query.
func TestWorkflowRunsReadsTheQueriedRuns(t *testing.T) {
	e := newEnv(t)
	world := e.withWorld()
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	world.AddRun(&gt.Run{ID: 71, Workflow: "ci.yml", HeadSHA: shaA, Attempt: 2, Conclusion: "failure", CreatedAt: at})
	world.AddRun(&gt.Run{ID: 72, Workflow: "ci.yml", Event: "merge_group", Branch: "gh-readonly-queue/main/pr-1",
		HeadSHA: shaA, Conclusion: "success", CreatedAt: at.Add(time.Minute)})
	world.AddRun(&gt.Run{ID: 73, Workflow: "ci.yml", HeadSHA: shaA, Status: "in_progress", CreatedAt: at.Add(2 * time.Minute)})

	runs, err := e.svc.WorkflowRuns(context.Background(), e.project, RunQuery{Branch: "main", Event: "push", HeadSHA: shaA})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != 73 || runs[1].ID != 71 {
		t.Fatalf("runs = %+v, want 73 then 71", runs)
	}
	want := RunView{ID: 71, Workflow: "ci.yml", Event: "push", Branch: "main", HeadSHA: shaA, Attempt: 2,
		Status: "completed", Conclusion: "failure", CreatedAt: at, StartedAt: at, UpdatedAt: at.Add(time.Minute)}
	if runs[1] != want {
		t.Fatalf("run = %+v\nwant  %+v", runs[1], want)
	}
	if runs[0].Conclusion != "" || runs[0].Status != "in_progress" {
		t.Fatalf("an unfinished run reads %+v", runs[0])
	}
	calls := runCalls(e, "/actions/runs")
	if len(calls) != 1 || !strings.Contains(calls[0], "branch=main") || !strings.Contains(calls[0], "event=push") ||
		!strings.Contains(calls[0], "head_sha="+shaA) || !strings.Contains(calls[0], "per_page=100") {
		t.Fatalf("calls = %v", calls)
	}
}

// A query with no window reads the newest page of 100 runs only, so a
// read of a long branch history stays one call.
func TestWorkflowRunsWithNoWindowReadsTheNewestPage(t *testing.T) {
	e := newEnv(t)
	world := e.withWorld()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range 150 {
		world.AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: shaA, Conclusion: "success", CreatedAt: at.Add(time.Duration(i) * time.Minute)})
	}
	runs, err := e.svc.WorkflowRuns(context.Background(), e.project, RunQuery{Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 100 || !runs[0].CreatedAt.Equal(at.Add(149*time.Minute)) {
		t.Fatalf("read %d runs, the newest at %s; want the newest 100", len(runs), runs[0].CreatedAt)
	}
	if calls := runCalls(e, "/actions/runs"); len(calls) != 1 {
		t.Fatalf("calls = %v, want one", calls)
	}
}

// A window reads every page of runs created in it, and nothing older.
func TestWorkflowRunsReadsEveryPageOfAWindow(t *testing.T) {
	e := newEnv(t)
	world := e.withWorld()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range 160 {
		world.AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: shaA, Conclusion: "success", CreatedAt: at.Add(time.Duration(i) * time.Minute)})
	}
	since := at.Add(10 * time.Minute)
	runs, err := e.svc.WorkflowRuns(context.Background(), e.project, RunQuery{Branch: "main", Since: since})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 150 {
		t.Fatalf("read %d runs, want the 150 in the window", len(runs))
	}
	calls := runCalls(e, "/actions/runs")
	if len(calls) != 2 || !strings.Contains(calls[0], "created=%3E%3D2026-10-01T00%3A10%3A00Z") {
		t.Fatalf("calls = %v, want two pages of the window", calls)
	}
}

// Ported from MAX_PAGES (graphql-pages.ts, alpha): a window that meets
// the page cap with another page to come fails, and never answers from
// the pages it read.
func TestWorkflowRunsFailClosedAtThePageCap(t *testing.T) {
	e := newEnv(t)
	world := e.withWorld()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range 1001 {
		world.AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: shaA, Conclusion: "success", CreatedAt: at.Add(time.Duration(i) * time.Second)})
	}
	runs, err := e.svc.WorkflowRuns(context.Background(), e.project, RunQuery{Since: at})
	if err == nil || !strings.Contains(err.Error(), "more than 1000 workflow runs") {
		t.Fatalf("runs = %d, err = %v; want the page cap", len(runs), err)
	}
}

// A named workflow reads its own runs. A file GitHub does not know
// answers 404, and the error says so.
func TestWorkflowRunsOfANamedWorkflow(t *testing.T) {
	e := newEnv(t)
	world := e.withWorld()
	world.AddRun(&gt.Run{ID: 81, Workflow: "audit.yml", Event: "schedule", HeadSHA: shaA, Conclusion: "success"})
	world.AddRun(&gt.Run{ID: 82, Workflow: "ci.yml", HeadSHA: shaA, Conclusion: "failure"})

	runs, err := e.svc.WorkflowRuns(context.Background(), e.project, RunQuery{Workflow: "audit.yml", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != 81 || runs[0].Workflow != "audit.yml" || runs[0].Event != "schedule" {
		t.Fatalf("runs = %+v", runs)
	}
	if calls := runCalls(e, "/actions/workflows/audit.yml/runs"); len(calls) != 1 {
		t.Fatalf("calls = %v", e.api.Calls())
	}

	_, err = e.svc.WorkflowRuns(context.Background(), e.project, RunQuery{Workflow: "nightly.yml"})
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404", err)
	}
}

// RunJobs reads the jobs of the attempt it names, not of the run's latest
// attempt, so a job that failed and then passed shows both conclusions.
// An attempt GitHub does not hold answers 404.
func TestRunJobsReadsOneAttempt(t *testing.T) {
	e := newEnv(t)
	world := e.withWorld()
	world.AddRun(&gt.Run{ID: 91, Workflow: "ci.yml", HeadSHA: shaA, Attempt: 2, Conclusion: "success", Jobs: [][]gt.Job{
		{{ID: 1, Name: "lint", Conclusion: "success"}, {ID: 2, Name: "Go tests (race)", Conclusion: "failure"}},
		{{ID: 3, Name: "lint", Conclusion: "success"}, {ID: 4, Name: "Go tests (race)", Conclusion: "success"}},
	}})

	jobs, err := e.svc.RunJobs(context.Background(), e.project, 91, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []JobView{{ID: 1, Name: "lint", Conclusion: "success"}, {ID: 2, Name: "Go tests (race)", Conclusion: "failure"}}
	if len(jobs) != 2 || jobs[0] != want[0] || jobs[1] != want[1] {
		t.Fatalf("jobs = %+v, want %+v", jobs, want)
	}
	if calls := runCalls(e, "/actions/runs/91/attempts/1/jobs"); len(calls) != 1 || !strings.Contains(calls[0], "per_page=100") {
		t.Fatalf("calls = %v", e.api.Calls())
	}
	if jobs, err := e.svc.RunJobs(context.Background(), e.project, 91, 2); err != nil || len(jobs) != 2 || jobs[1].Conclusion != "success" {
		t.Fatalf("attempt 2: jobs = %+v, err = %v", jobs, err)
	}

	_, err = e.svc.RunJobs(context.Background(), e.project, 91, 3)
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404", err)
	}
}
