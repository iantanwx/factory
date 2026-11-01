package github

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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
	App        CheckApp   `json:"app"`
	Output     CheckOut   `json:"output"`
	// Unstarted says why GitHub never started the run's job. Checks
	// fills it in; GitHub's answer never holds it.
	Unstarted *Unstarted `json:"-"`
}

// CheckApp is the GitHub App that posted a check run.
type CheckApp struct {
	Slug string `json:"slug"`
}

// CheckOut is the text a check run reports.
type CheckOut struct {
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Text        string `json:"text"`
	Annotations int    `json:"annotations_count"`
}

// Causes of a check run that GitHub never started.
const (
	UnstartedBilling  = "billing"   // the account cannot pay for Actions
	UnstartedNoRunner = "no_runner" // the job got no runner and ran no step
)

// Unstarted is why GitHub failed a check run whose job it never started.
// Such a run built nothing, so it is neither red nor green (F-18).
type Unstarted struct {
	Cause   string `json:"cause"`
	Message string `json:"message"`
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
	// Unstarted is set when a check on the head failed because GitHub
	// never started its job. The head is then pending, not red.
	Unstarted *Unstarted `json:"unstarted,omitempty"`
}

var redConclusions = []string{"failure", "timed_out", "action_required", "startup_failure", "stale"}

// Classify reads the CI state of head. Only checks on head count. The
// latest run of each check name decides that name. A cancelled latest
// run waits for a rerun: it is pending, never red, and never green, so an
// unknown result is never read as success. A failed run whose job GitHub
// never started is pending too: it has no result, so it is neither green
// nor red. A failed run from an earlier attempt of a rerun workflow run
// waits for the rerun to report its job: it is pending, never red (F-69).
// loop/eligible is the loop's own status and never counts as CI.
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
	earlier := earlierAttempt(head, runs, latest)
	red, pending := false, false
	for _, name := range slices.Sorted(maps.Keys(latest)) {
		r := latest[name]
		v := CheckView{Name: r.Name, Kind: "check", ID: r.ID, URL: r.HTMLURL}
		switch {
		case r.Status != "completed":
			v.State, pending = CIPending, true
		case r.Unstarted != nil:
			v.State, pending = CIPending, true
			v.Detail = "never started: " + r.Unstarted.Message
			if rep.Unstarted == nil {
				rep.Unstarted = r.Unstarted
			}
		case earlier[name] && slices.Contains(redConclusions, r.Conclusion):
			v.State, pending = CIPending, true
			v.Detail = r.Conclusion + " in an earlier attempt; the rerun has not reported it"
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

// CIVerdict is a head's CI read against the checks a merge requires.
type CIVerdict struct {
	State   string   // CIGreen, CIRed or CIPending; never CINone
	Red     []string // the checks that failed
	Waiting []string // the checks that run, or that must report and have not
}

// Against reads the report against the checks a merge requires. Every
// check on the head must be green, and each required check must have
// reported there. A required check that has not reported waits, and a
// head with no check at all is never green: absence is never evidence of
// success (alpha premerge.ts:99-110).
func (r CIReport) Against(required []string) CIVerdict {
	var v CIVerdict
	for _, c := range r.Checks {
		switch c.State {
		case CIRed:
			v.Red = append(v.Red, c.Name)
		case CIPending:
			v.Waiting = append(v.Waiting, c.Name)
		}
	}
	for _, name := range required {
		if !slices.ContainsFunc(r.Checks, func(c CheckView) bool { return c.Name == name }) {
			v.Waiting = append(v.Waiting, name+" (not reported)")
		}
	}
	if len(r.Checks) == 0 && len(v.Waiting) == 0 {
		v.Waiting = []string{"no check has reported"}
	}
	switch {
	case len(v.Red) > 0:
		v.State = CIRed
	case len(v.Waiting) > 0:
		v.State = CIPending
	default:
		v.State = CIGreen
	}
	return v
}

// actionsJob matches the workflow run id in an Actions job's URL.
var actionsJob = regexp.MustCompile(`/actions/runs/(\d+)/job/`)

// earlierAttempt names each check whose latest run on head belongs to an
// earlier attempt of its Actions workflow run. A rerun runs each of its
// jobs once more, so in one workflow run a job of the newest attempt has
// run the most times, and a job the rerun has not reached has run fewer.
func earlierAttempt(head string, runs []CheckRun, latest map[string]CheckRun) map[string]bool {
	type key struct{ run, name string }
	times, most := map[key]int{}, map[string]int{}
	for _, r := range runs {
		m := actionsJob.FindStringSubmatch(r.HTMLURL)
		if m == nil || r.HeadSHA != "" && r.HeadSHA != head {
			continue
		}
		k := key{m[1], r.Name}
		times[k]++
		most[k.run] = max(most[k.run], times[k])
	}
	out := map[string]bool{}
	for name, r := range latest {
		if m := actionsJob.FindStringSubmatch(r.HTMLURL); m != nil && times[key{m[1], name}] < most[m[1]] {
			out[name] = true
		}
	}
	return out
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

// maxCheckPages bounds the read of a head's check runs: 1000 of them.
const maxCheckPages = 10

// Checks reads the check runs and the commit statuses of head. Ported from
// MAX_PAGES in scripts/sdlc/graphql-pages.ts (alpha): a read that reaches
// the page cap with a full page fails, and never classifies the runs it
// did read, since a red run on a later page would leave the head green.
func (s *Service) Checks(ctx context.Context, p config.Project, head string) (CIReport, error) {
	var runs []CheckRun
	for page := 1; ; page++ {
		if page > maxCheckPages {
			return CIReport{}, fmt.Errorf("github: %s has more than %d check runs on %s", p.Repo, maxCheckPages*100, head)
		}
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
	for i := range runs {
		r := &runs[i]
		if r.HeadSHA != head || r.Status != "completed" || r.Conclusion != "failure" {
			continue
		}
		u, err := s.unstarted(ctx, p, *r)
		if err != nil {
			return CIReport{}, err
		}
		r.Unstarted = u
	}
	var combined struct {
		Statuses []CommitStatus `json:"statuses"`
	}
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/commits/"+head+"/status"), &combined); err != nil {
		return CIReport{}, err
	}
	return Classify(head, runs, combined.Statuses), nil
}

// CheckRunsNamed reads the latest check runs named name on head, from
// every App.
func (s *Service) CheckRunsNamed(ctx context.Context, p config.Project, head, name string) ([]CheckRun, error) {
	var batch struct {
		Runs []CheckRun `json:"check_runs"`
	}
	q := url.Values{"check_name": {name}, "filter": {"latest"}, "per_page": {"100"}}
	// Encode writes a space as "+"; a literal "+" is already "%2B".
	query := strings.ReplaceAll(q.Encode(), "+", "%20")
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/commits/"+head+"/check-runs?"+query), &batch); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(batch.Runs, func(r CheckRun) bool { return r.Name != name }), nil
}

// notStarted matches the annotation GitHub writes on a job it never
// started, such as "The job was not started because recent account
// payments have failed or your spending limit needs to be increased".
var (
	notStarted = regexp.MustCompile(`(?i)\bjob was not started\b`)
	billing    = regexp.MustCompile(`(?i)payments have failed|spending limit`)
)

type annotation struct {
	Message string `json:"message"`
}

// unstarted reads why GitHub never started the job of a failed check run,
// or returns nil for a run that ran. A billing or not-started annotation
// names the cause. Otherwise an Actions job with no runner and no steps
// never started. For an Actions check run, the check run id is the job
// id; another App's check run has no job, and a failure there is red.
func (s *Service) unstarted(ctx context.Context, p config.Project, r CheckRun) (*Unstarted, error) {
	if r.Output.Annotations > 0 {
		var as []annotation
		if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, fmt.Sprintf("/check-runs/%d/annotations?per_page=100", r.ID)), &as); err != nil {
			return nil, err
		}
		for _, a := range as {
			switch {
			case billing.MatchString(a.Message):
				return &Unstarted{Cause: UnstartedBilling, Message: a.Message}, nil
			case notStarted.MatchString(a.Message):
				return &Unstarted{Cause: UnstartedNoRunner, Message: a.Message}, nil
			}
		}
	}
	var j struct {
		RunnerID *int64 `json:"runner_id"`
		Steps    []any  `json:"steps"`
	}
	err := s.repoGet(ctx, p, ScopeRead, repoPath(p, fmt.Sprintf("/actions/jobs/%d", r.ID)), &j)
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if j.RunnerID == nil && len(j.Steps) == 0 {
		return &Unstarted{Cause: UnstartedNoRunner, Message: "GitHub gave the job no runner, and it ran no step"}, nil
	}
	return nil, nil
}

// workflowRun is one Actions run of a head.
type workflowRun struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	HeadSHA      string `json:"head_sha"`
	HeadBranch   string `json:"head_branch"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	HTMLURL      string `json:"html_url"`
	DisplayTitle string `json:"display_title"`
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
	// Queue names the merge-queue run the job ran in, for a read of the
	// queue's draft. A draft can hold other pull requests of one batch.
	Queue *QueueRun `json:"queue,omitempty"`
}

// QueueRun is the merge-queue run of a draft that holds a pull request's
// head. Its title names the pull requests of the draft's batch.
type QueueRun struct {
	Run     int64  `json:"run"`
	URL     string `json:"url"`
	HeadSHA string `json:"head_sha"`
	Branch  string `json:"branch"`
	Title   string `json:"title"`
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
	return s.excerpts(ctx, p, jobs, nil)
}

// excerpts reads the tail of each job's log. queue maps a run id to the
// merge-queue run of that id, so each job names the run it ran in. It is
// nil for the head's own jobs.
func (s *Service) excerpts(ctx context.Context, p config.Project, jobs []job, queue map[int64]*QueueRun) ([]LogExcerpt, error) {
	var out []LogExcerpt
	for _, j := range jobs {
		text, err := s.jobLog(ctx, p, j.ID)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		ex := LogExcerpt{Job: j.ID, Name: j.Name, URL: j.HTMLURL, AuthorClass: AuthorOther, Lines: len(lines), Queue: queue[j.RunID]}
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
	return s.runJobs(ctx, p, runs)
}

// runJobs lists the failed jobs of the latest attempt of each run.
func (s *Service) runJobs(ctx context.Context, p config.Project, runs []workflowRun) ([]job, error) {
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

// The branch prefixes of a merge queue's drafts. Mergify tests a batch
// on a draft pull request from mergify/merge-queue/<id>. GitHub's merge
// queue tests a group on gh-readonly-queue/<base>/pr-<number>-<sha>.
// Both mechanisms merge with merge commits, so a draft's head holds each
// queued pull request's head.
var draftPrefixes = []string{"mergify/merge-queue/", "gh-readonly-queue/"}

// Ported from MOST_DRAFTS and LISTED_RUNS in
// scripts/sdlc/implementer-ci-queue.ts (alpha). The run that tested a
// pull request ran after its head was pushed, so it sits near the top of
// the listing.
const (
	mostDrafts = 10  // the most drafts one read compares with the head
	listedRuns = 100 // the completed runs one listing reads, newest first
)

// QueueLogs returns the tail of each failed job of the newest merge-queue
// draft whose head holds head. The newest run of each workflow on that
// draft decides, so a pass after a failure clears it. It returns nothing
// when that draft passed or no draft in reach holds head. Ported from
// queueFailure in scripts/sdlc/implementer-ci-queue.ts (alpha).
func (s *Service) QueueLogs(ctx context.Context, p config.Project, head string) ([]LogExcerpt, error) {
	var list struct {
		Runs []workflowRun `json:"workflow_runs"`
	}
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, fmt.Sprintf("/actions/runs?status=completed&per_page=%d", listedRuns)), &list); err != nil {
		return nil, err
	}
	byDraft := map[string][]workflowRun{}
	var drafts []string
	for _, r := range list.Runs {
		if !queueDraft(r) {
			continue
		}
		if _, seen := byDraft[r.HeadSHA]; !seen {
			drafts = append(drafts, r.HeadSHA)
		}
		byDraft[r.HeadSHA] = append(byDraft[r.HeadSHA], r)
	}
	for _, sha := range drafts[:min(len(drafts), mostDrafts)] {
		ok, err := s.holds(ctx, p, head, sha)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var failed []workflowRun
		newest := map[string]bool{}
		for _, r := range byDraft[sha] {
			if newest[r.Name] {
				continue
			}
			newest[r.Name] = true
			if slices.Contains(redConclusions, r.Conclusion) {
				failed = append(failed, r)
			}
		}
		if len(failed) == 0 {
			return nil, nil
		}
		jobs, err := s.runJobs(ctx, p, failed)
		if err != nil {
			return nil, err
		}
		queue := map[int64]*QueueRun{}
		for _, r := range failed {
			queue[r.ID] = &QueueRun{Run: r.ID, URL: r.HTMLURL, HeadSHA: sha, Branch: r.HeadBranch, Title: r.DisplayTitle}
		}
		return s.excerpts(ctx, p, jobs, queue)
	}
	return nil, nil
}

// queueDraft is true for a finished run of a merge-queue draft that
// passed or failed.
func queueDraft(r workflowRun) bool {
	if r.Conclusion != "success" && !slices.Contains(redConclusions, r.Conclusion) {
		return false
	}
	return slices.ContainsFunc(draftPrefixes, func(pre string) bool { return strings.HasPrefix(r.HeadBranch, pre) })
}

// holds is true when commit draft holds commit head. A draft whose
// commit GitHub no longer holds tested nothing that can be read.
func (s *Service) holds(ctx context.Context, p config.Project, head, draft string) (bool, error) {
	var cmp struct {
		Status string `json:"status"`
	}
	err := s.repoGet(ctx, p, ScopeRead, repoPath(p, fmt.Sprintf("/compare/%s...%s", head, draft)), &cmp)
	var api *APIError
	if errors.As(err, &api) && (api.Status == http.StatusNotFound || api.Status == http.StatusUnprocessableEntity) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return cmp.Status == "ahead" || cmp.Status == "identical", nil
}

// jobLog reads the whole log of one job as text.
func (s *Service) jobLog(ctx context.Context, p config.Project, id int64) (string, error) {
	var text string
	_, err := s.repoDo(ctx, p, ScopeRead, Call{Method: http.MethodGet,
		Path: repoPath(p, fmt.Sprintf("/actions/jobs/%d/logs", id)), Raw: &text}, nil)
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

// RunQuery selects workflow runs. Empty fields select every value.
type RunQuery struct {
	Branch   string
	Event    string // "push", "merge_group", "pull_request"
	Workflow string // a file name under .github/workflows
	HeadSHA  string
	Since    time.Time // created at or after
}

// RunView is one workflow run, as the GitHub parts of overwatch read it.
// workflowRun stays the decode type of the existing reads.
type RunView struct {
	ID         int64     `json:"id"`
	Workflow   string    `json:"workflow"` // the file name under .github/workflows
	Event      string    `json:"event"`
	Branch     string    `json:"branch"`
	HeadSHA    string    `json:"head_sha"`
	Attempt    int       `json:"attempt"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// runJSON is one workflow run as GitHub's REST API reads it.
type runJSON struct {
	ID           int64     `json:"id"`
	Path         string    `json:"path"` // .github/workflows/<file>, with an @<ref> for a called workflow
	Event        string    `json:"event"`
	HeadBranch   string    `json:"head_branch"`
	HeadSHA      string    `json:"head_sha"`
	RunAttempt   int       `json:"run_attempt"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	CreatedAt    time.Time `json:"created_at"`
	RunStartedAt time.Time `json:"run_started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (r runJSON) view() RunView {
	file, _, _ := strings.Cut(r.Path, "@")
	if i := strings.LastIndex(file, "/"); i >= 0 {
		file = file[i+1:]
	}
	return RunView{ID: r.ID, Workflow: file, Event: r.Event, Branch: r.HeadBranch, HeadSHA: r.HeadSHA, Attempt: r.RunAttempt,
		Status: r.Status, Conclusion: r.Conclusion, CreatedAt: r.CreatedAt, StartedAt: r.RunStartedAt, UpdatedAt: r.UpdatedAt}
}

// maxRunPages bounds the read of a window of workflow runs: 1000 of them.
const maxRunPages = 10

// WorkflowRuns lists the workflow runs that q selects, newest first. A
// query with no Since reads the newest page of 100 only, so a read of a
// branch's whole history stays one call. A query with Since reads every
// page of its window, and fails at the page cap rather than answer from
// part of the window. A Workflow that GitHub does not know answers an
// *APIError with status 404.
func (s *Service) WorkflowRuns(ctx context.Context, p config.Project, q RunQuery) ([]RunView, error) {
	path := "/actions/runs"
	if q.Workflow != "" {
		path = "/actions/workflows/" + url.PathEscape(q.Workflow) + "/runs"
	}
	v := url.Values{"per_page": {"100"}, "exclude_pull_requests": {"true"}}
	for k, val := range map[string]string{"branch": q.Branch, "event": q.Event, "head_sha": q.HeadSHA} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if !q.Since.IsZero() {
		v.Set("created", ">="+q.Since.UTC().Format(time.RFC3339))
	}
	var out []RunView
	for page := 1; page <= maxRunPages; page++ {
		v.Set("page", strconv.Itoa(page))
		var batch struct {
			Runs []runJSON `json:"workflow_runs"`
		}
		resp, err := s.repoDo(ctx, p, ScopeRead, Call{Method: http.MethodGet, Path: repoPath(p, path+"?"+v.Encode())}, &batch)
		if err != nil {
			return nil, err
		}
		for _, r := range batch.Runs {
			out = append(out, r.view())
		}
		if q.Since.IsZero() || len(batch.Runs) < 100 && !nextPage(resp.Header) {
			return out, nil
		}
	}
	return nil, fmt.Errorf("github: %s has more than %d workflow runs since %s", p.Repo, maxRunPages*100, q.Since.UTC().Format(time.RFC3339))
}

// JobView is one job of one attempt of a run.
type JobView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// RunJobs lists the jobs of one attempt of a run, every page of them. The
// run listing shows the latest attempt only, so an earlier attempt's
// failed job is read here.
func (s *Service) RunJobs(ctx context.Context, p config.Project, runID int64, attempt int) ([]JobView, error) {
	var out []JobView
	for page := 1; ; page++ {
		var batch struct {
			Jobs []JobView `json:"jobs"`
		}
		resp, err := s.repoDo(ctx, p, ScopeRead, Call{Method: http.MethodGet,
			Path: repoPath(p, fmt.Sprintf("/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d", runID, attempt, page))}, &batch)
		if err != nil {
			return nil, err
		}
		out = append(out, batch.Jobs...)
		if !nextPage(resp.Header) {
			return out, nil
		}
	}
}
