package githubtest

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The Actions side of the world: workflow runs, listed as GitHub's
// actions/runs and actions/workflows/<file>/runs answer them, newest first.

// Run is one workflow run the world holds. AddRun fills the zero fields.
type Run struct {
	ID         int64
	Workflow   string // a file name under .github/workflows, such as ci.yml
	Event      string // push when empty
	Branch     string // main when empty
	HeadSHA    string
	Attempt    int    // 1 when zero
	Status     string // completed when empty
	Conclusion string // empty while the run has not completed
	CreatedAt  time.Time
}

func (r *Run) json() map[string]any {
	out := map[string]any{
		"id": r.ID, "name": strings.TrimSuffix(strings.TrimSuffix(r.Workflow, ".yml"), ".yaml"),
		"path": ".github/workflows/" + r.Workflow, "event": r.Event, "head_branch": r.Branch, "head_sha": r.HeadSHA,
		"run_attempt": r.Attempt, "status": r.Status, "conclusion": nil,
		"created_at":     r.CreatedAt.UTC().Format(time.RFC3339),
		"run_started_at": r.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":     r.CreatedAt.Add(time.Minute).UTC().Format(time.RFC3339),
		"html_url":       fmt.Sprintf("https://example.invalid/actions/runs/%d", r.ID),
	}
	if r.Conclusion != "" {
		out["conclusion"] = r.Conclusion
	}
	return out
}

// AddRun stores a workflow run and returns it. A zero ID takes the next
// id, and a zero CreatedAt takes the world's clock.
func (w *World) AddRun(r *Run) *Run {
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.ID == 0 {
		r.ID = int64(7000 + len(w.wruns) + 1)
	}
	if r.Event == "" {
		r.Event = "push"
	}
	if r.Branch == "" {
		r.Branch = "main"
	}
	if r.Attempt == 0 {
		r.Attempt = 1
	}
	if r.Status == "" {
		r.Status = "completed"
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = w.tick()
	}
	w.wruns = append(w.wruns, r)
	return r
}

// AddWorkflow names a workflow file that holds no run yet. GitHub answers
// 404 for the runs of a file that no run and no AddWorkflow names.
func (w *World) AddWorkflow(file string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.workflows = append(w.workflows, file)
}

var reWorkflowRuns = regexp.MustCompile(`^/repos/` + Repo + `/actions/workflows/([^/]+)/runs$`)

// serveRuns answers a list of workflow runs, or reports false for any
// other request. The caller holds w.mu.
func (w *World) serveRuns(rw http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	list := slices.Clone(w.wruns)
	switch m := reWorkflowRuns.FindStringSubmatch(r.URL.Path); {
	case r.URL.Path == "/repos/"+Repo+"/actions/runs":
	case m != nil:
		file := m[1]
		known := slices.Contains(w.workflows, file) || slices.ContainsFunc(list, func(run *Run) bool { return run.Workflow == file })
		if !known {
			WriteJSON(rw, 404, map[string]string{"message": "Not Found"})
			return true
		}
		list = slices.DeleteFunc(list, func(run *Run) bool { return run.Workflow != file })
	default:
		return false
	}
	q := r.URL.Query()
	for param, field := range map[string]func(*Run) string{
		"branch": func(run *Run) string { return run.Branch }, "event": func(run *Run) string { return run.Event },
		"head_sha": func(run *Run) string { return run.HeadSHA }, "status": func(run *Run) string { return run.Status },
	} {
		if v := q.Get(param); v != "" {
			list = slices.DeleteFunc(list, func(run *Run) bool { return field(run) != v })
		}
	}
	if c := q.Get("created"); c != "" {
		since, err := time.Parse(time.RFC3339, strings.TrimPrefix(c, ">="))
		if !strings.HasPrefix(c, ">=") || err != nil {
			WriteJSON(rw, 422, map[string]string{"message": "the fake reads created=>=<RFC 3339> only"})
			return true
		}
		list = slices.DeleteFunc(list, func(run *Run) bool { return run.CreatedAt.Before(since) })
	}
	slices.SortStableFunc(list, func(a, b *Run) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return int(b.ID - a.ID)
	})
	page, _ := strconv.Atoi(q.Get("page"))
	if page == 0 {
		page = 1
	}
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per == 0 {
		per = 30
	}
	out := []any{}
	for i := (page - 1) * per; i < len(list) && i < page*per; i++ {
		out = append(out, list[i].json())
	}
	if page*per < len(list) {
		next := r.URL.Query()
		next.Set("page", strconv.Itoa(page+1))
		rw.Header().Set("Link", fmt.Sprintf(`<%s?%s>; rel="next"`, r.URL.Path, next.Encode()))
	}
	WriteJSON(rw, 200, map[string]any{"total_count": len(list), "workflow_runs": out})
	return true
}
