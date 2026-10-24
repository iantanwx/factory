package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// GitHub answers the job-logs endpoint only to the JSON Accept header,
// with a 302 to the plain-text log, and answers 415 to text/plain. So
// `pr logs` asks with the JSON header, follows the redirect, and prints
// the tail of each failed job's log.
func TestLogsFollowTheRedirectWithTheJSONAcceptHeader(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const runID, jobID = 7001, 9001
	repo := "/repos/" + gt.Repo
	e.api.Route("GET "+repo+"/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		gt.WriteJSON(w, 200, map[string]any{"workflow_runs": []any{
			map[string]any{"id": runID, "name": "ci", "head_sha": shaA, "status": "completed", "conclusion": "failure"}}})
	})
	e.api.Route(fmt.Sprintf("GET %s/actions/runs/%d/jobs", repo, runID), func(w http.ResponseWriter, r *http.Request) {
		gt.WriteJSON(w, 200, map[string]any{"jobs": []any{
			map[string]any{"id": jobID, "run_id": runID, "name": "test", "head_sha": shaA, "conclusion": "failure",
				"html_url": "https://github.com/" + gt.Repo + "/actions/runs/7001/job/9001"}}})
	})
	e.api.Route(fmt.Sprintf("GET %s/actions/jobs/%d/logs", repo, jobID), func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "json") {
			gt.WriteJSON(w, 415, map[string]string{"message": "Unsupported Accept header: " + r.Header.Get("Accept") + ". Must accept application/json."})
			return
		}
		http.Redirect(w, r, "/blob/job-9001.log", http.StatusFound)
	})
	var log strings.Builder
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&log, "line %d\n", i)
	}
	e.api.Route("GET /blob/job-9001.log", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(log.String()))
	})

	got, err := e.svc.Logs(ctx, e.project, shaA, 0)
	if err != nil {
		t.Fatalf("pr logs: %v", err)
	}
	if len(got) != 1 || got[0].Job != jobID || got[0].Name != "test" {
		t.Fatalf("want the one failed job, got %+v", got)
	}
	ex := got[0]
	if ex.Lines != 250 || !ex.Truncated {
		t.Fatalf("want 250 lines, truncated, got lines=%d truncated=%v", ex.Lines, ex.Truncated)
	}
	lines := strings.Split(ex.Text, "\n")
	if len(lines) != logTail || lines[0] != "line 51" || lines[len(lines)-1] != "line 250" {
		t.Fatalf("want the last %d lines, line 51 to line 250, got %d lines from %q to %q",
			logTail, len(lines), lines[0], lines[len(lines)-1])
	}
}
