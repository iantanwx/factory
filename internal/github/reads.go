package github

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// CI states of one head.
const (
	CIGreen   = "green"   // every check passed
	CIRed     = "red"     // a check on the head failed
	CIPending = "pending" // a check runs, waits, or was cancelled with no newer run
	CINone    = "none"    // the head has no check yet
)

// CheckRun is one check run as GitHub's REST API reads it.
type CheckRun struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	HeadSHA    string     `json:"head_sha"`
	Status     string     `json:"status"`
	Conclusion string     `json:"conclusion"`
	StartedAt  *time.Time `json:"started_at"`
	HTMLURL    string     `json:"html_url"`
}

// CommitStatus is one commit status context.
type CommitStatus struct {
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

// CheckView is one line of `pr checks`.
type CheckView struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"` // check or status
	State  string `json:"state"`
	ID     int64  `json:"id,omitempty"`
	URL    string `json:"url,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// CIReport is the CI state of one head.
type CIReport struct {
	Head   string      `json:"head"`
	State  string      `json:"state"`
	Checks []CheckView `json:"checks"`
	// Superseded lists checks that ran on an earlier head. A newer push
	// cancelled or replaced them, so they are never red (KTD16).
	Superseded []CheckView `json:"superseded"`
}

var redConclusions = []string{"failure", "timed_out", "action_required", "startup_failure", "stale"}

// Classify reads the CI state of head. Only checks on head count. The
// latest run of each check name decides that name. A cancelled latest
// run waits for a rerun: it is pending, never red, and never green, so an
// unknown result is never read as success. loop/eligible is the loop's
// own status and never counts as CI.
func Classify(head string, runs []CheckRun, statuses []CommitStatus) CIReport {
	rep := CIReport{Head: head, Checks: []CheckView{}, Superseded: []CheckView{}}
	latest := map[string]CheckRun{}
	for _, r := range runs {
		if r.HeadSHA != "" && r.HeadSHA != head {
			rep.Superseded = append(rep.Superseded, CheckView{Name: r.Name, Kind: "check", State: "superseded", ID: r.ID,
				URL: r.HTMLURL, Detail: "ran on " + short(r.HeadSHA)})
			continue
		}
		cur, ok := latest[r.Name]
		if !ok || newer(r, cur) {
			if ok {
				rep.Superseded = append(rep.Superseded, CheckView{Name: cur.Name, Kind: "check", State: "superseded", ID: cur.ID,
					URL: cur.HTMLURL, Detail: "a newer run replaced it"})
			}
			latest[r.Name] = r
		}
	}
	red, pending := false, false
	for _, name := range slices.Sorted(maps.Keys(latest)) {
		r := latest[name]
		v := CheckView{Name: r.Name, Kind: "check", ID: r.ID, URL: r.HTMLURL}
		switch {
		case r.Status != "completed":
			v.State, pending = CIPending, true
		case slices.Contains(redConclusions, r.Conclusion):
			v.State, red = CIRed, true
			v.Detail = r.Conclusion
		case r.Conclusion == "cancelled":
			v.State, pending = CIPending, true
			v.Detail = "cancelled; rerun it"
		default:
			v.State = CIGreen
			v.Detail = r.Conclusion
		}
		rep.Checks = append(rep.Checks, v)
	}
	for _, st := range statuses {
		if st.Context == EligibleContext {
			continue
		}
		v := CheckView{Name: st.Context, Kind: "status", URL: st.TargetURL, Detail: st.Description}
		switch st.State {
		case "failure", "error":
			v.State, red = CIRed, true
		case "pending":
			v.State, pending = CIPending, true
		default:
			v.State = CIGreen
		}
		rep.Checks = append(rep.Checks, v)
	}
	switch {
	case red:
		rep.State = CIRed
	case pending:
		rep.State = CIPending
	case len(rep.Checks) == 0:
		rep.State = CINone
	default:
		rep.State = CIGreen
	}
	return rep
}

func newer(a, b CheckRun) bool {
	at, bt := time.Time{}, time.Time{}
	if a.StartedAt != nil {
		at = *a.StartedAt
	}
	if b.StartedAt != nil {
		bt = *b.StartedAt
	}
	if c := at.Compare(bt); c != 0 {
		return c > 0
	}
	return cmp.Compare(a.ID, b.ID) > 0
}

// Checks reads the check runs and the commit statuses of head.
func (s *Service) Checks(ctx context.Context, p config.Project, head string) (CIReport, error) {
	var runs []CheckRun
	for page := 1; page <= 10; page++ {
		var batch struct {
			Total int        `json:"total_count"`
			Runs  []CheckRun `json:"check_runs"`
		}
		if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, fmt.Sprintf("/commits/%s/check-runs?filter=all&per_page=100&page=%d", head, page)), &batch); err != nil {
			return CIReport{}, err
		}
		runs = append(runs, batch.Runs...)
		if len(batch.Runs) < 100 {
			break
		}
	}
	var combined struct {
		Statuses []CommitStatus `json:"statuses"`
	}
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/commits/"+head+"/status"), &combined); err != nil {
		return CIReport{}, err
	}
	return Classify(head, runs, combined.Statuses), nil
}

// workflowRun is one Actions run of a head.
type workflowRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type job struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

// failedRuns lists the Actions runs of head that failed.
func (s *Service) failedRuns(ctx context.Context, p config.Project, head string) ([]workflowRun, error) {
	var list struct {
		Runs []workflowRun `json:"workflow_runs"`
	}
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/actions/runs?per_page=100&head_sha="+head), &list); err != nil {
		return nil, err
	}
	var out []workflowRun
	for _, r := range list.Runs {
		if r.HeadSHA == head && slices.Contains(redConclusions, r.Conclusion) {
			out = append(out, r)
		}
	}
	return out, nil
}

// LogExcerpt is the tail of one failed job's log.
type LogExcerpt struct {
	Job         int64  `json:"job"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	AuthorClass string `json:"author_class"`
	Lines       int    `json:"lines"`
	Truncated   bool   `json:"truncated"`
	Text        string `json:"text"`
}

// logTail is how many lines of a failed job's log `pr logs` prints.
const logTail = 200

// Logs returns the tail of each failed job's log on head, or of one job
// when job is not zero. The job must belong to head.
func (s *Service) Logs(ctx context.Context, p config.Project, head string, jobID int64) ([]LogExcerpt, error) {
	var jobs []job
	if jobID != 0 {
		var j job
		if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, fmt.Sprintf("/actions/jobs/%d", jobID)), &j); err != nil {
			return nil, err
		}
		if j.HeadSHA != head {
			return nil, &RefusedError{Code: cli.CodeUsage, Rule: "job_not_on_head",
				Hint: fmt.Sprintf("job %d ran on %s, not on the PR head %s", jobID, short(j.HeadSHA), short(head))}
		}
		jobs = []job{j}
	} else {
		var err error
		if jobs, err = s.failedJobs(ctx, p, head); err != nil {
			return nil, err
		}
	}
	var out []LogExcerpt
	for _, j := range jobs {
		text, err := s.jobLog(ctx, p, j.ID)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		ex := LogExcerpt{Job: j.ID, Name: j.Name, URL: j.HTMLURL, AuthorClass: AuthorOther, Lines: len(lines)}
		if len(lines) > logTail {
			lines, ex.Truncated = lines[len(lines)-logTail:], true
		}
		ex.Text = strings.Join(lines, "\n")
		out = append(out, ex)
	}
	return out, nil
}

// failedJobs lists the failed jobs of the latest attempt of each failed
// Actions run on head.
func (s *Service) failedJobs(ctx context.Context, p config.Project, head string) ([]job, error) {
	runs, err := s.failedRuns(ctx, p, head)
	if err != nil {
		return nil, err
	}
	var jobs []job
	for _, r := range runs {
		var list struct {
			Jobs []job `json:"jobs"`
		}
		if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, fmt.Sprintf("/actions/runs/%d/jobs?filter=latest&per_page=100", r.ID)), &list); err != nil {
			return nil, err
		}
		for _, j := range list.Jobs {
			if slices.Contains(redConclusions, j.Conclusion) {
				jobs = append(jobs, j)
			}
		}
	}
	return jobs, nil
}

// jobLog reads the whole log of one job as text.
func (s *Service) jobLog(ctx context.Context, p config.Project, id int64) (string, error) {
	var text string
	_, err := s.repoDo(ctx, p, ScopeRead, Call{Method: http.MethodGet,
		Path: repoPath(p, fmt.Sprintf("/actions/jobs/%d/logs", id)), Accept: "text/plain", Raw: &text}, nil)
	return text, err
}

// Rerun asks GitHub to rerun the failed jobs of every failed Actions run
// on head, and returns the run ids. A write never repeats.
func (s *Service) Rerun(ctx context.Context, p config.Project, head string) ([]int64, error) {
	runs, err := s.failedRuns(ctx, p, head)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, r := range runs {
		if _, err := s.repoDo(ctx, p, ScopeActions, Call{Method: http.MethodPost,
			Path: repoPath(p, fmt.Sprintf("/actions/runs/%d/rerun-failed-jobs", r.ID))}, nil); err != nil {
			return ids, err
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}
