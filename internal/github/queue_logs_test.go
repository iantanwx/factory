package github

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// Two merge-queue drafts: the newer one holds other pull requests only,
// the older one holds the item's head.
const (
	draftOther = "dddddddddddddddddddddddddddddddddddddd01"
	draftHeld  = "dddddddddddddddddddddddddddddddddddddd02"
)

// queueRun is one completed Actions run of a merge-queue draft, as the
// repository's run listing reads it. GitHub's merge queue runs carry the
// merge_group event.
func queueRun(id int64, name, branch, sha, conclusion string) map[string]any {
	event := "pull_request"
	if strings.HasPrefix(branch, "gh-readonly-queue/") {
		event = "merge_group"
	}
	return map[string]any{"id": id, "name": name, "head_branch": branch, "head_sha": sha, "event": event,
		"status": "completed", "conclusion": conclusion,
		"html_url":      fmt.Sprintf("https://github.com/%s/actions/runs/%d", gt.Repo, id),
		"display_title": "merge queue: embarking #7 and #9 together"}
}

// serveQueue answers the run listing with runs, newest first, and the
// compare of head with each draft: only draftHeld holds head.
func serveQueue(e *env, head string, runs ...map[string]any) {
	e.api.Route("GET /repos/"+gt.Repo+"/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("head_sha") != "" {
			writeJSON(w, 200, map[string]any{"total_count": 0, "workflow_runs": []any{}})
			return
		}
		writeJSON(w, 200, map[string]any{"total_count": len(runs), "workflow_runs": runs})
	})
	for sha, status := range map[string]string{draftOther: "diverged", draftHeld: "ahead"} {
		e.api.Route(fmt.Sprintf("GET /repos/%s/compare/%s...%s", gt.Repo, head, sha), func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, map[string]any{"status": status})
		})
	}
}

// serveFailedJob answers the jobs of run with one failed job, and its log.
func serveFailedJob(e *env, run, job int64, sha, log string) {
	e.api.Route(fmt.Sprintf("GET /repos/%s/actions/runs/%d/jobs", gt.Repo, run), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"total_count": 1, "jobs": []any{map[string]any{"id": job, "run_id": run, "name": "gates",
			"head_sha": sha, "conclusion": "failure",
			"html_url": fmt.Sprintf("https://github.com/%s/actions/runs/%d/job/%d", gt.Repo, run, job)}}})
	})
	e.api.Route(fmt.Sprintf("GET /repos/%s/actions/jobs/%d/logs", gt.Repo, job), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(log))
	})
}

func repairItem(e *env) string {
	e.t.Helper()
	id := e.ticket(machine.Repair, shaB)
	e.api.World.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaB})
	return id
}

// 5.11 (parity with implementer-ci-queue.ts in alpha): the merge queue
// tests a pull request on a draft, so its failed job sits on the draft's
// head and not on the pull request's head. `pr logs --queue` reads the
// failed job of the newest draft whose head holds the pull request's
// head, and names the draft's run and title, which names the other pull
// requests of its batch.
func TestPRLogsQueueReadsTheDraftsFailedJob(t *testing.T) {
	for _, branch := range []string{"mergify/merge-queue/4f1c2a", "gh-readonly-queue/main/pr-7-" + strings.Repeat("b", 40)} {
		t.Run(strings.SplitN(branch, "/", 2)[0], func(t *testing.T) {
			e := newEnv(t)
			e.withWorld()
			id := repairItem(e)
			// A pass on a branch that is no draft never clears the draft.
			serveQueue(e, shaB,
				queueRun(703, "ci", "loop/other", draftHeld, "success"),
				queueRun(701, "ci", branch, draftOther, "failure"),
				queueRun(702, "ci", branch, draftHeld, "failure"))
			serveFailedJob(e, 702, 802, draftHeld, "ok 1\nFAIL: TestQueueOnly\n")

			res := e.runCall("pr logs", id, machine.StageImplementer, map[string]string{"queue": "true"}, nil)
			mustOK(t, res)
			logs, ok := res.Data.([]LogExcerpt)
			if !ok || len(logs) != 1 {
				t.Fatalf("pr logs --queue = %#v, want the one failed job of the draft", res.Data)
			}
			got := logs[0]
			if got.Job != 802 || got.Text != "ok 1\nFAIL: TestQueueOnly" {
				t.Fatalf("read job %d with %q, want job 802 and its log", got.Job, got.Text)
			}
			want := QueueRun{Run: 702, URL: "https://github.com/" + gt.Repo + "/actions/runs/702", HeadSHA: draftHeld,
				Branch: branch, Title: "merge queue: embarking #7 and #9 together"}
			if got.Queue == nil || *got.Queue != want {
				t.Fatalf("queue = %+v, want %+v", got.Queue, want)
			}
		})
	}
}

// A draft with two failed workflows yields jobs of two runs. Each
// excerpt names the run its own job ran in, so its URL opens that run.
func TestPRLogsQueueNamesEachJobsOwnRun(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	id := repairItem(e)
	const branch = "mergify/merge-queue/4f1c2a"
	serveQueue(e, shaB,
		queueRun(732, "lint", branch, draftHeld, "failure"),
		queueRun(731, "ci", branch, draftHeld, "failure"))
	serveFailedJob(e, 732, 832, draftHeld, "FAIL: lint\n")
	serveFailedJob(e, 731, 831, draftHeld, "FAIL: ci\n")

	res := e.runCall("pr logs", id, machine.StageImplementer, map[string]string{"queue": "true"}, nil)
	mustOK(t, res)
	logs, ok := res.Data.([]LogExcerpt)
	if !ok || len(logs) != 2 {
		t.Fatalf("pr logs --queue = %#v, want the failed job of each of the two runs", res.Data)
	}
	for _, want := range []struct{ job, run int64 }{{832, 732}, {831, 731}} {
		i := slices.IndexFunc(logs, func(l LogExcerpt) bool { return l.Job == want.job })
		if i < 0 {
			t.Fatalf("pr logs --queue read no job %d", want.job)
		}
		url := fmt.Sprintf("https://github.com/%s/actions/runs/%d", gt.Repo, want.run)
		if q := logs[i].Queue; q == nil || q.Run != want.run || q.URL != url {
			t.Fatalf("job %d: queue = %+v, want run %d at %s", want.job, q, want.run, url)
		}
	}
}

// The newest run of a check on the draft decides, so a pass after a
// failure clears it: there is nothing to read.
func TestPRLogsQueuePassAfterFailureClears(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	id := repairItem(e)
	const branch = "mergify/merge-queue/4f1c2a"
	serveQueue(e, shaB,
		queueRun(712, "ci", branch, draftHeld, "success"),
		queueRun(711, "ci", branch, draftHeld, "failure"))
	serveFailedJob(e, 711, 811, draftHeld, "FAIL: stale\n")

	res := e.runCall("pr logs", id, machine.StageImplementer, map[string]string{"queue": "true"}, nil)
	mustOK(t, res)
	if logs, ok := res.Data.([]LogExcerpt); ok && len(logs) > 0 {
		t.Fatalf("pr logs --queue read %d jobs of a run a newer pass cleared", len(logs))
	}
}

// Without --queue the read stays on the pull request's head: a failed
// draft run is not the head's failure.
func TestPRLogsWithoutQueueReadsTheHeadOnly(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	id := repairItem(e)
	serveQueue(e, shaB, queueRun(721, "ci", "mergify/merge-queue/4f1c2a", draftHeld, "failure"))
	serveFailedJob(e, 721, 821, draftHeld, "FAIL: queue\n")

	res := e.runCall("pr logs", id, machine.StageImplementer, nil, nil)
	mustOK(t, res)
	if logs, ok := res.Data.([]LogExcerpt); ok && len(logs) > 0 {
		t.Fatalf("pr logs read %d draft jobs with no --queue", len(logs))
	}
}

// --queue and --job name two different reads.
func TestPRLogsQueueRefusesAJob(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	id := repairItem(e)
	res := e.runCall("pr logs", id, machine.StageImplementer, map[string]string{"queue": "true", "job": "5"}, nil)
	if res.Refusal == nil || res.Refusal.Rule != "queue_and_job" {
		t.Fatalf("pr logs --queue --job = %+v, want the queue_and_job refusal", res)
	}
}
