package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/pack"
)

// RulesetName is the name of the ruleset `loop register` keeps on a
// project's default branch.
const RulesetName = "loop"

// cmdRunner runs one command in dir with stdin and returns its stdout. An
// error holds the command's stderr.
type cmdRunner func(ctx context.Context, dir string, stdin []byte, name string, args ...string) ([]byte, error)

func execCmd(ctx context.Context, dir string, stdin []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	if stdin != nil {
		c.Stdin = strings.NewReader(string(stdin))
	}
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// RegisterReport is the JSON of `loop register`.
type RegisterReport struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	// Branch is the default branch; empty while the repository has no
	// commit.
	Branch string `json:"branch"`
	// Ruleset is "created", "updated", or "unavailable" when GitHub offers
	// no rulesets on the repository's plan.
	Ruleset string   `json:"ruleset"`
	CI      []string `json:"ci"`
	Skills  []string `json:"skills"`
	Added   bool     `json:"added"` // false when the project was registered already
	Next    string   `json:"next,omitempty"`
}

// registerProject is `loop register`: it tells the loop about the git clone it
// runs in. It keeps the loop ruleset on the default branch with the
// operator's gh login, registers the project with the server, and writes
// the loop skills into the clone, so a harness session there can run
// loop-init. Each step leaves alone what is already right, so it runs
// again after loop-init adds CI.
func (h *hostVerbs) registerProject(ctx context.Context, inv cli.Invocation) cli.Result {
	dir, ctl, res := h.prepare(ctx, inv)
	if res != nil {
		return *res
	}
	host, err := config.LoadHost(filepath.Join(dir, config.HostConfigFile))
	if err != nil {
		return cli.Reject(cli.CodeNotFound, "not_installed", "run loop install first: "+err.Error())
	}
	appID := host.Credentials.GitHubApp.AppID
	if appID == 0 {
		return cli.Reject(cli.CodeNotFound, "no_app", "host config names no GitHub App; run loop install")
	}
	if !ctl.Reachable(ctx) {
		return cli.Reject(cli.CodeServerUnavailable, "server_down", "the loop service is not running; run loop install")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(err)
	}
	top, err := h.cmd(ctx, cwd, nil, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return cli.Reject(cli.CodeUsage, "not_a_clone", "run loop register inside the project's git clone: "+err.Error())
	}
	rep := RegisterReport{Path: filepath.Clean(strings.TrimSpace(string(top)))}
	var view struct {
		NameWithOwner    string `json:"nameWithOwner"`
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	if err := h.ghJSON(ctx, rep.Path, &view, "repo", "view", "--json", "nameWithOwner,defaultBranchRef"); err != nil {
		return cli.Reject(cli.CodeUsage, "no_github_repo", "the clone has no GitHub repository gh can read: "+err.Error())
	}
	rep.Repo, rep.Branch = view.NameWithOwner, view.DefaultBranchRef.Name
	rep.Project = inv.Arg("project")
	if rep.Project == "" {
		rep.Project = projectID(rep.Repo)
	}

	if rep.Branch != "" {
		if rep.CI, err = h.ciChecks(ctx, rep); err != nil {
			return fail(err)
		}
	}
	if rep.Ruleset, err = h.ensureRuleset(ctx, rep, appID); err != nil {
		return fail(err)
	}
	if rep.Skills, err = installSkills(ctx, rep.Path); err != nil {
		return fail(err)
	}
	if len(rep.CI) == 0 {
		rep.Next = "the repository has no CI yet; start a harness session in " + rep.Path + " and run the loop-init skill"
	}

	for _, p := range host.Projects {
		if strings.EqualFold(p.Repo, rep.Repo) {
			rep.Project = p.ID
			return cli.Found(rep)
		}
	}
	v, ok := h.reg.Verb("project add")
	if !ok {
		return fail(errors.New("this build has no project add verb"))
	}
	added := ctl.client().Remote(ctx, cli.Invocation{Verb: v, Args: []string{rep.Project},
		Flags: map[string]string{"repo": rep.Repo, "path": rep.Path}, RequestID: newRequestID()})
	if r := added.Refusal; r != nil {
		return cli.Reject(r.Code, r.Rule, r.Hint+"; is the loop App installed on "+rep.Repo+"? Add it from the App's page on GitHub, then run loop register again")
	}
	rep.Added = true
	return cli.Found(rep)
}

// installSkills writes the loop skills into the clone for Claude Code and
// Codex, and keeps them out of git through info/exclude, as a run's
// checkout does.
func installSkills(ctx context.Context, clone string) ([]string, error) {
	p, err := pack.Brief()
	if err != nil {
		return nil, err
	}
	skills, err := p.Skills()
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, h := range []agenthost.Harness{agenthost.Claude, agenthost.Codex} {
		if err := p.Write(clone, h); err != nil {
			return nil, err
		}
		for _, s := range skills {
			dirs = append(dirs, brief.SkillRoot(h)+"/"+s)
		}
	}
	return skills, brief.Exclude(ctx, clone, nil, dirs)
}

// ciChecks names the checks that ran on the head of the default branch:
// check runs and commit statuses, except loop/eligible.
func (h *hostVerbs) ciChecks(ctx context.Context, rep RegisterReport) ([]string, error) {
	ref := "repos/" + rep.Repo + "/commits/" + rep.Branch
	var runs struct {
		CheckRuns []struct {
			Name string `json:"name"`
		} `json:"check_runs"`
	}
	if err := h.ghJSON(ctx, rep.Path, &runs, "api", ref+"/check-runs"); err != nil {
		return nil, err
	}
	var status struct {
		Statuses []struct {
			Context string `json:"context"`
		} `json:"statuses"`
	}
	if err := h.ghJSON(ctx, rep.Path, &status, "api", ref+"/status"); err != nil {
		return nil, err
	}
	var out []string
	for _, r := range runs.CheckRuns {
		out = append(out, r.Name)
	}
	for _, s := range status.Statuses {
		out = append(out, s.Context)
	}
	out = slices.DeleteFunc(out, func(n string) bool { return n == "" || n == github.EligibleContext })
	slices.Sort(out)
	return slices.Compact(out), nil
}

// ensureRuleset creates or updates the loop ruleset on the default
// branch: a pull request with no approvals, the CI checks, and
// loop/eligible from the loop App. Repository admins bypass it, so the
// operator signs off a PRD or spec by merging it. A plan with no rulesets
// answers "unavailable", and the loop checks CI itself before a direct
// merge.
func (h *hostVerbs) ensureRuleset(ctx context.Context, rep RegisterReport, appID int64) (string, error) {
	checks := []map[string]any{}
	for _, c := range rep.CI {
		checks = append(checks, map[string]any{"context": c})
	}
	checks = append(checks, map[string]any{"context": github.EligibleContext, "integration_id": appID})
	body, err := json.Marshal(map[string]any{
		"name": RulesetName, "target": "branch", "enforcement": "active",
		"conditions": map[string]any{"ref_name": map[string]any{"include": []string{"~DEFAULT_BRANCH"}, "exclude": []string{}}},
		// Role 5 is the repository admin role.
		"bypass_actors": []map[string]any{{"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always"}},
		"rules": []map[string]any{
			{"type": "pull_request", "parameters": map[string]any{
				"required_approving_review_count": 0, "dismiss_stale_reviews_on_push": false,
				"require_code_owner_review": false, "require_last_push_approval": false,
				"required_review_thread_resolution": false,
			}},
			{"type": "required_status_checks", "parameters": map[string]any{
				"strict_required_status_checks_policy": false, "required_status_checks": checks,
			}},
		},
	})
	if err != nil {
		return "", err
	}
	var existing []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := h.ghJSON(ctx, rep.Path, &existing, "api", "repos/"+rep.Repo+"/rulesets"); err != nil {
		if strings.Contains(err.Error(), "Upgrade to GitHub Pro") || strings.Contains(err.Error(), "HTTP 403") {
			return "unavailable", nil
		}
		return "", err
	}
	for _, r := range existing {
		if r.Name == RulesetName {
			_, err := h.cmd(ctx, rep.Path, body, "gh", "api", "-X", "PUT", fmt.Sprintf("repos/%s/rulesets/%d", rep.Repo, r.ID), "--input", "-", "--silent")
			return "updated", err
		}
	}
	_, err = h.cmd(ctx, rep.Path, body, "gh", "api", "-X", "POST", "repos/"+rep.Repo+"/rulesets", "--input", "-", "--silent")
	return "created", err
}

func (h *hostVerbs) ghJSON(ctx context.Context, dir string, v any, args ...string) error {
	out, err := h.cmd(ctx, dir, nil, "gh", args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

var notIdent = regexp.MustCompile(`[^a-z0-9_-]+`)

// projectID makes a project id from a repository name.
func projectID(repo string) string {
	_, name, _ := strings.Cut(repo, "/")
	id := strings.Trim(notIdent.ReplaceAllString(strings.ToLower(name), "-"), "-_")
	if id == "" || id[0] < 'a' || id[0] > 'z' {
		id = "p-" + id
	}
	return id
}
