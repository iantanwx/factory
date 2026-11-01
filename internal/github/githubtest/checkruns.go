package githubtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
)

var reCheckRun = regexp.MustCompile(`^/repos/` + Repo + `/check-runs(?:/(\d+))?$`)

// CheckRunCalls lists each create and each update of a check run, in
// order: "create <id> <sha> <name> <conclusion>" or
// "update <id> <conclusion>".
func (w *World) CheckRunCalls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.checkCalls)
}

// CheckRunsAt lists the check runs of sha, oldest first.
func (w *World) CheckRunsAt(sha string) []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.runs[sha])
}

// serveCheckRuns creates, updates and lists check runs. A run the fake
// creates was posted by the fixture App. The list honours check_name and
// app_id, as GitHub's does. The caller holds w.mu.
func (w *World) serveCheckRuns(rw http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && reChecks.MatchString(path):
		q := r.URL.Query()
		runs := []map[string]any{}
		for _, run := range w.runs[reChecks.FindStringSubmatch(path)[1]] {
			app, _ := run["app"].(map[string]any)
			if n := q.Get("check_name"); n != "" && run["name"] != n {
				continue
			}
			if id := q.Get("app_id"); id != "" && fmt.Sprint(app["id"]) != id {
				continue
			}
			runs = append(runs, run)
		}
		WriteJSON(rw, 200, map[string]any{"total_count": len(runs), "check_runs": runs})
	case r.Method == http.MethodPost && reCheckRun.MatchString(path) && reCheckRun.FindStringSubmatch(path)[1] == "":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sha, _ := body["head_sha"].(string)
		w.nextCheck++
		id := 9000 + w.nextCheck
		run := map[string]any{"id": id, "name": body["name"], "head_sha": sha, "status": body["status"],
			"conclusion": body["conclusion"], "output": body["output"], "started_at": w.tick().Format("2006-01-02T15:04:05Z"),
			"app": map[string]any{"id": AppID, "slug": "loop-fixture"}}
		w.runs[sha] = append(w.runs[sha], run)
		w.checkCalls = append(w.checkCalls, fmt.Sprintf("create %d %s %v %v", id, sha, body["name"], body["conclusion"]))
		WriteJSON(rw, 201, run)
	case r.Method == http.MethodPatch && reCheckRun.MatchString(path):
		id, _ := strconv.ParseInt(reCheckRun.FindStringSubmatch(path)[1], 10, 64)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, runs := range w.runs {
			for _, run := range runs {
				if run["id"] != id {
					continue
				}
				for _, k := range []string{"status", "conclusion", "output"} {
					if v, ok := body[k]; ok {
						run[k] = v
					}
				}
				w.checkCalls = append(w.checkCalls, fmt.Sprintf("update %d %v", id, body["conclusion"]))
				WriteJSON(rw, 200, run)
				return true
			}
		}
		WriteJSON(rw, 404, map[string]string{"message": "Not Found"})
	default:
		return false
	}
	return true
}
