package github

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/iantanwx/loop/internal/config"
)

// actionsAppID is the GitHub Actions App. It reports the check run of
// each workflow job.
const actionsAppID = 15368

const workflowsDir = ".github/workflows"

// CheckRequiredJobs compares the status checks that the default branch's
// rules require with the jobs that the workflows on that branch define
// (parity 10.8). A required check that no job reports never reports on a
// PR head, so no review can pass there: a renamed job stalls every PR at
// its pass. The line names that drift before a PR meets it.
//
// It compares only a check that GitHub Actions may report: one with no
// expected source, or with Actions as its source. Another App reports its
// own check. A branch with no workflow takes its checks from another CI,
// and a plan with no rulesets requires no check by name.
func (s *Service) CheckRequiredJobs(ctx context.Context, p config.Project) (CheckLine, error) {
	line := CheckLine{Check: "required_jobs", OK: true}
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return line, err
	}
	var rules []rule
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/rules/branches/"+pathEscapeBranch(base)), &rules); err != nil {
		if noRulesets(err) {
			line.Detail = fmt.Sprintf("GitHub offers no rulesets for %s on its plan; the loop requires no check by name, so none can drift from the workflows", p.Repo)
			return line, nil
		}
		return line, err
	}
	var required []string
	for _, r := range rules {
		if r.Type != "required_status_checks" || len(r.Parameters) == 0 {
			continue
		}
		var params struct {
			Checks []requiredCheck `json:"required_status_checks"`
		}
		if err := json.Unmarshal(r.Parameters, &params); err != nil {
			return line, fmt.Errorf("github: read the required checks of %s: %w", base, err)
		}
		for _, c := range params.Checks {
			if c.Context != "" && c.Context != EligibleContext && (c.IntegrationID == nil || *c.IntegrationID == actionsAppID) {
				required = append(required, c.Context)
			}
		}
	}
	if len(required) == 0 {
		line.Detail = fmt.Sprintf("the rules of %s require no check that a workflow job reports", base)
		return line, nil
	}
	jobs, err := s.workflowJobs(ctx, p, base)
	var bad *workflowError
	switch {
	case errors.As(err, &bad):
		line.OK, line.Detail = false, err.Error()
		return line, nil
	case err != nil:
		return line, err
	case len(jobs) == 0:
		line.Detail = fmt.Sprintf("%s holds no workflow in %s; the required checks come from another CI", base, workflowsDir)
		return line, nil
	}
	var missing []string
	for _, name := range required {
		if !slices.ContainsFunc(jobs, func(j workflowJob) bool { return j.reports(name) }) {
			missing = append(missing, fmt.Sprintf("%q", name))
		}
	}
	if len(missing) == 0 {
		line.Detail = fmt.Sprintf("a workflow job on %s reports each required check", base)
		return line, nil
	}
	var labels []string
	for _, j := range jobs {
		labels = append(labels, j.label)
	}
	line.OK = false
	line.Detail = fmt.Sprintf("no workflow job on %s reports %s, so no PR can pass; rename the job or the required check of %s to match, or require it from the App that reports it (jobs: %s)",
		base, strings.Join(missing, ", "), base, strings.Join(labels, ", "))
	return line, nil
}

// workflowJob is one job of a workflow, and the check run names it
// reports.
type workflowJob struct {
	label string // the job's name, or its id when it has none
	names *regexp.Regexp
}

func (j workflowJob) reports(check string) bool { return j.names.MatchString(check) }

// workflowError names a workflow file that does not parse.
type workflowError struct {
	path string
	err  error
}

func (e *workflowError) Error() string {
	return fmt.Sprintf("%s does not parse, so its jobs are unknown: %v", e.path, e.err)
}

// expression is a ${{ }} expression in a job name. It can expand to any
// text.
var expression = regexp.MustCompile(`\$\{\{.*?\}\}`)

// workflowJobs reads every workflow on branch through the contents API,
// and returns each job. A branch with no workflow directory has no job.
func (s *Service) workflowJobs(ctx context.Context, p config.Project, branch string) ([]workflowJob, error) {
	var entries []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	ref := "?ref=" + url.QueryEscape(branch)
	var api *APIError
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/contents/"+workflowsDir+ref), &entries); err != nil {
		if errors.As(err, &api) && api.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	var jobs []workflowJob
	for _, e := range entries {
		if ext := path.Ext(e.Path); e.Type != "file" || ext != ".yml" && ext != ".yaml" {
			continue
		}
		var file struct {
			Content string `json:"content"`
		}
		if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/contents/"+pathEscapeBranch(e.Path)+ref), &file); err != nil {
			return nil, err
		}
		raw, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			return nil, fmt.Errorf("github: read %s on %s: %w", e.Path, branch, err)
		}
		found, err := parseJobs(raw)
		if err != nil {
			return nil, &workflowError{path: e.Path, err: err}
		}
		jobs = append(jobs, found...)
	}
	return jobs, nil
}

// parseJobs returns the jobs of one workflow file. A job reports a check
// run named after its name, or its id when it has none. Each expression
// in the name can expand to any text. A matrix job adds its matrix values
// in parentheses, and a job that calls a reusable workflow reports each
// called job as "<name> / <called job>".
func parseJobs(raw []byte) ([]workflowJob, error) {
	var wf struct {
		Jobs map[string]struct {
			Name     string `yaml:"name"`
			Uses     string `yaml:"uses"`
			Strategy struct {
				Matrix any `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return nil, err
	}
	var out []workflowJob
	for _, id := range slices.Sorted(maps.Keys(wf.Jobs)) {
		j := wf.Jobs[id]
		label := cmp.Or(j.Name, id)
		var parts []string
		for _, lit := range expression.Split(label, -1) {
			parts = append(parts, regexp.QuoteMeta(lit))
		}
		re := strings.Join(parts, ".+")
		if j.Strategy.Matrix != nil {
			re += `( \(.+\))?`
		}
		if j.Uses != "" {
			re += `( / .+)?`
		}
		out = append(out, workflowJob{label: label, names: regexp.MustCompile("^" + re + "$")})
	}
	return out, nil
}
