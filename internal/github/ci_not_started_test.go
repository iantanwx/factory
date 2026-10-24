package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// billingNotStarted is the annotation GitHub writes on a job it never
// started because the account cannot pay (F-18).
const billingNotStarted = "The job was not started because recent account payments have failed or your spending limit needs to be increased. Please check the 'Billing & plans' section in your settings"

// notStartedRun is a check run that GitHub failed in 3 seconds with no
// runner and no steps, as it reads on the REST API.
func notStartedRun(id int64, sha string, t0 time.Time) map[string]any {
	return map[string]any{"id": id, "name": "gates", "head_sha": sha, "status": "completed", "conclusion": "failure",
		"started_at": t0.Format(time.RFC3339), "completed_at": t0.Add(3 * time.Second).Format(time.RFC3339),
		"output": map[string]any{"title": nil, "summary": nil, "text": nil, "annotations_count": 1}}
}

// serveNotStarted answers the annotations and the job of a check run that
// never started: one billing annotation, no runner and no steps.
func serveNotStarted(e *env, id int64, sha string) {
	e.api.Route(fmt.Sprintf("GET /repos/%s/check-runs/%d/annotations", gt.Repo, id), func(w http.ResponseWriter, _ *http.Request) {
		gt.WriteJSON(w, 200, []any{map[string]any{"path": ".github", "start_line": 1, "end_line": 1,
			"annotation_level": "failure", "title": "", "message": billingNotStarted}})
	})
	e.api.Route(fmt.Sprintf("GET /repos/%s/actions/jobs/%d", gt.Repo, id), func(w http.ResponseWriter, _ *http.Request) {
		gt.WriteJSON(w, 200, map[string]any{"id": id, "run_id": id + 1000, "name": "gates", "head_sha": sha,
			"status": "completed", "conclusion": "failure", "runner_id": nil, "runner_name": nil, "steps": []any{}})
	})
}

// C-bug-1 (F-18): a check run that GitHub never started is not a test
// failure. A failed check with a billing annotation, no runner and no
// steps holds each ticket in review, starts no repair, and raises one
// alert that names the cause, however many tickets it reaches.
func TestCINeverStartedIsNotRed(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	const shaC = "cccccccccccccccccccccccccccccccccccccccc"
	t0 := time.Date(2026, 10, 24, 2, 20, 0, 0, time.UTC)
	var ids []string
	for i, sha := range []string{shaB, shaC} {
		id := e.ticket(machine.InReview, sha)
		w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: sha})
		run := int64(9001 + i)
		w.SetRuns(sha, notStartedRun(run, sha, t0))
		serveNotStarted(e, run, sha)
		ids = append(ids, id)
	}
	r := e.reactor()
	e.tick(r)
	e.tick(r)
	for _, id := range ids {
		if got := e.status(id).Status; got == machine.Repair {
			t.Fatalf("a job GitHub never started sent %s to repair", id)
		}
		if got := e.status(id).Status; got != machine.InReview {
			t.Fatalf("a job GitHub never started moved %s to %s, want it held in review", id, got)
		}
	}
	var n int
	var body string
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		return tx.QueryRow(`SELECT count(*), coalesce(max(body), '') FROM alerts WHERE kind = 'ci_not_started' AND state = 'open'`).
			Scan(&n, &body)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d open ci_not_started alerts, want one for the cause", n)
	}
	if !strings.Contains(body, "spending limit") && !strings.Contains(body, "payments have failed") {
		t.Fatalf("the alert does not name the billing cause: %q", body)
	}
}

// C-bug-1: a failed check with no annotation, whose job got no runner and
// ran no step, never started either. The job alone decides it: the ticket
// stays in review and one alert names the no-runner cause.
func TestCINoRunnerIsNotRed(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InReview, shaB)
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaB})
	t0 := time.Date(2026, 10, 24, 2, 20, 0, 0, time.UTC)
	const run = 9201
	cr := notStartedRun(run, shaB, t0)
	cr["output"] = map[string]any{"title": nil, "summary": nil, "text": nil, "annotations_count": 0}
	w.SetRuns(shaB, cr)
	e.api.Route(fmt.Sprintf("GET /repos/%s/actions/jobs/%d", gt.Repo, run), func(w http.ResponseWriter, _ *http.Request) {
		gt.WriteJSON(w, 200, map[string]any{"id": run, "run_id": run + 1000, "name": "gates", "head_sha": shaB,
			"status": "completed", "conclusion": "failure", "runner_id": nil, "runner_name": nil, "steps": []any{}})
	})
	r := e.reactor()
	e.tick(r)
	e.tick(r)
	if got := e.status(id).Status; got != machine.InReview {
		t.Fatalf("a job with no runner and no steps moved %s to %s, want it held in review", id, got)
	}
	var n int
	var subject string
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		return tx.QueryRow(`SELECT count(*), coalesce(max(subject), '') FROM alerts WHERE kind = 'ci_not_started' AND state = 'open'`).
			Scan(&n, &subject)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d open ci_not_started alerts, want one for the no-runner cause", n)
	}
	if !strings.HasSuffix(subject, "/no_runner") {
		t.Fatalf("the alert does not name the no-runner cause: %q", subject)
	}
}

// C-bug-1: a failed job that GitHub started is still red. A check run
// with an annotation that names no billing cause, whose job had a runner
// and ran steps, sends the ticket to repair and raises no alert.
func TestCIStartedFailureIsRed(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InReview, shaB)
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaB})
	t0 := time.Date(2026, 10, 24, 2, 20, 0, 0, time.UTC)
	const run = 9101
	w.SetRuns(shaB, notStartedRun(run, shaB, t0))
	e.api.Route(fmt.Sprintf("GET /repos/%s/check-runs/%d/annotations", gt.Repo, run), func(w http.ResponseWriter, _ *http.Request) {
		gt.WriteJSON(w, 200, []any{map[string]any{"path": "internal/x_test.go", "start_line": 3, "end_line": 3,
			"annotation_level": "failure", "title": "", "message": "Process completed with exit code 1."}})
	})
	e.api.Route(fmt.Sprintf("GET /repos/%s/actions/jobs/%d", gt.Repo, run), func(w http.ResponseWriter, _ *http.Request) {
		gt.WriteJSON(w, 200, map[string]any{"id": run, "run_id": run + 1000, "name": "gates", "head_sha": shaB,
			"status": "completed", "conclusion": "failure", "runner_id": 7, "runner_name": "GitHub Actions 7",
			"steps": []any{map[string]any{"name": "test", "status": "completed", "conclusion": "failure", "number": 1}}})
	})
	e.tick(e.reactor())
	if got := e.status(id).Status; got != machine.Repair {
		t.Fatalf("a job that ran and failed left %s in %s, want repair", id, got)
	}
	if n := e.alerts(AlertCINotStarted); n != 0 {
		t.Fatalf("%d ci_not_started alerts for a job that ran", n)
	}
}

// C-bug-1: Classify reads a failed run whose job never started as
// pending, and names the cause. A real failure beside it is still red.
func TestClassifyUnstarted(t *testing.T) {
	u := &Unstarted{Cause: UnstartedBilling, Message: billingNotStarted}
	unstarted := CheckRun{ID: 1, Name: "gates", HeadSHA: shaB, Status: "completed", Conclusion: "failure", Unstarted: u}
	rep := Classify(shaB, []CheckRun{unstarted}, nil)
	if rep.State != CIPending || rep.Unstarted == nil || rep.Unstarted.Cause != UnstartedBilling {
		t.Fatalf("never started: state %s, unstarted %+v; want pending with the billing cause", rep.State, rep.Unstarted)
	}
	red := CheckRun{ID: 2, Name: "lint", HeadSHA: shaB, Status: "completed", Conclusion: "failure"}
	if got := Classify(shaB, []CheckRun{unstarted, red}, nil).State; got != CIRed {
		t.Fatalf("a real failure beside a job that never started reads %s, want red", got)
	}
}
