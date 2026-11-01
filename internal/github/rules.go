package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
)

// EligibleContext is the commit status a review pass posts (KTD15).
const EligibleContext = "loop/eligible"

// loopContext reports whether a required check is one the loop posts,
// loop/eligible or loop/qa, which never counts as CI.
func loopContext(name string) bool { return name == EligibleContext || name == QAContext }

// CheckLine is one line of a setup checklist. A line that fails names the
// rule and what the operator changes.
type CheckLine struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// failing joins the failed lines into one error, or returns nil.
func failing(lines []CheckLine) error {
	var bad []string
	for _, l := range lines {
		if !l.OK {
			bad = append(bad, "- "+l.Check+": "+l.Detail)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return &RulesError{Lines: bad}
}

// RulesError lists every rule a setup check found missing.
type RulesError struct{ Lines []string }

func (e *RulesError) Error() string {
	return "github: the repository is not ready for the loop:\n" + strings.Join(e.Lines, "\n")
}

// rule is one active rule on a branch, read loosely: the check needs the
// rule type and the required contexts, and it ignores every other field.
// GitHub may change this endpoint's shape, so the check asserts the rules
// and never a response layout.
type rule struct {
	Type       string          `json:"type"`
	Parameters json.RawMessage `json:"parameters"`
}

type requiredCheck struct {
	Context       string `json:"context"`
	IntegrationID *int64 `json:"integration_id"`
}

func pathEscapeBranch(b string) string {
	parts := strings.Split(b, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// CheckRules reads the rules that apply to the default branch, through
// GitHub's rules-for-a-branch endpoint, and returns one checklist line
// for each rule the loop depends on:
//
//   - CI is a required status check;
//   - loop/eligible is a required status check from the loop App;
//   - loop/qa is a required check from the loop App;
//   - a direct push to the branch is blocked: a pull request is required,
//     or updates are restricted.
//
// The App's metadata read is enough to read the endpoint.
func (s *Service) CheckRules(ctx context.Context, p config.Project) ([]CheckLine, error) {
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return nil, err
	}
	var rules []rule
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/rules/branches/"+pathEscapeBranch(base)), &rules); err != nil {
		// Nothing enforces the rules on a plan with no rulesets, so the
		// direct merge checks CI itself.
		if noRulesets(err) {
			return []CheckLine{{Check: "branch_rules", OK: true,
				Detail: fmt.Sprintf("GitHub offers no rulesets for %s on its plan; the loop checks CI itself before a direct merge", p.Repo)}}, nil
		}
		return nil, err
	}
	var required []requiredCheck
	blocked := false
	for _, r := range rules {
		switch r.Type {
		case "pull_request", "update":
			blocked = true
		case "required_status_checks":
			var params struct {
				Checks []requiredCheck `json:"required_status_checks"`
			}
			if len(r.Parameters) > 0 {
				if err := json.Unmarshal(r.Parameters, &params); err != nil {
					return nil, fmt.Errorf("github: read the required checks of %s: %w", base, err)
				}
			}
			required = append(required, params.Checks...)
		}
	}
	appID, err := s.AppID(ctx, p)
	if err != nil {
		return nil, err
	}
	fromApp := func(c requiredCheck) bool { return c.IntegrationID != nil && *c.IntegrationID == appID }
	eligible := slices.IndexFunc(required, func(c requiredCheck) bool { return c.Context == EligibleContext })
	ci := slices.ContainsFunc(required, func(c requiredCheck) bool { return !loopContext(c.Context) && c.Context != "" })
	lines := []CheckLine{
		{Check: "ci_required", OK: ci,
			Detail: fmt.Sprintf("add the CI check to the required status checks of %s", base)},
		{Check: "eligible_required", OK: eligible >= 0,
			Detail: fmt.Sprintf("add %s to the required status checks of %s", EligibleContext, base)},
	}
	if eligible >= 0 {
		lines = append(lines, CheckLine{Check: "eligible_source", OK: fromApp(required[eligible]),
			Detail: fmt.Sprintf("require %s from the loop App (app id %d) as its expected source", EligibleContext, appID)})
	}
	lines = append(lines, CheckLine{Check: "qa_required", OK: slices.ContainsFunc(required, func(c requiredCheck) bool {
		return c.Context == QAContext && fromApp(c)
	}), Detail: fmt.Sprintf("require %s from the loop App (app id %d) in the required status checks of %s", QAContext, appID, base)})
	lines = append(lines, CheckLine{Check: "direct_push_blocked", OK: blocked,
		Detail: fmt.Sprintf("block direct pushes to %s: require a pull request before merging", base)})
	return lines, nil
}

// noRulesets reports whether err is GitHub's answer for a plan with no
// rulesets: a private repository on a free account answers 404, or 403
// with an upgrade message.
func noRulesets(err error) bool {
	var api *APIError
	return errors.As(err, &api) && (api.Status == http.StatusNotFound ||
		api.Status == http.StatusForbidden && strings.Contains(api.Message, "Upgrade to GitHub Pro"))
}

// Doctor runs every GitHub check of one project for `loop doctor`: the
// installation's permissions, the branch rules, and the workflow job of
// each required check. A check that cannot run is one failed line, never
// a pass.
func (s *Service) Doctor(ctx context.Context, p config.Project) []CheckLine {
	var lines []CheckLine
	inst, err := s.CheckInstallation(ctx, p)
	if err != nil {
		lines = append(lines, CheckLine{Check: "installation", Detail: "the installation could not be read: " + err.Error()})
	}
	lines = append(lines, inst...)
	rules, err := s.CheckRules(ctx, p)
	var re *RulesError
	if err != nil && !errors.As(err, &re) {
		lines = append(lines, CheckLine{Check: "branch_rules", Detail: "the branch rules could not be read: " + err.Error()})
	}
	lines = append(lines, rules...)
	jobs, err := s.CheckRequiredJobs(ctx, p)
	if err != nil {
		jobs.OK, jobs.Detail = false, "the required checks and the workflow jobs could not be compared: "+err.Error()
	}
	return append(lines, jobs)
}
