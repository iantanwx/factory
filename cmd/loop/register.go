package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/server"
	"github.com/iantanwx/loop/internal/userspace"
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
	// Branch is the default branch, which the registration records as the
	// base of the project's runs; empty while the repository has no commit.
	Branch string `json:"branch"`
	// Ruleset is "created", "updated", or "unavailable" when GitHub offers
	// no rulesets on the repository's plan.
	Ruleset string `json:"ruleset"`
	// App is "created" when register made the owner's loop App, or
	// "installed" when the owner's App was installed on the repository.
	App    string   `json:"app"`
	CI     []string `json:"ci"`
	Skills []string `json:"skills"`
	// UserSkills names each userspace pin of the project's config at
	// origin/<branch> that register wrote into the clone (ADR 0013).
	UserSkills []string `json:"user_skills"`
	// UserSkillsFailed is each pin that register could not write.
	UserSkillsFailed []PinFailure `json:"user_skills_failed,omitempty"`
	// UserSkillsConfig says why the config did not load. The clone then
	// holds the kernel skills only.
	UserSkillsConfig string `json:"user_skills_config,omitempty"`
	Added            bool   `json:"added"` // false when the project was registered already
	// MovedFrom is the repository the project named before this register
	// moved it.
	MovedFrom string `json:"moved_from,omitempty"`
	Next      string `json:"next,omitempty"`
}

// PinFailure is a userspace pin that register could not write, and why.
type PinFailure struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
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

	appID, err := h.ensureApp(ctx, dir, &rep)
	if err != nil {
		return cli.Reject(cli.CodeNotFound, "no_app", err.Error())
	}
	if rep.Branch != "" {
		if rep.CI, err = h.ciChecks(ctx, rep); err != nil {
			return fail(err)
		}
	}
	if rep.Ruleset, err = h.ensureRuleset(ctx, rep, appID); err != nil {
		return fail(err)
	}
	// The store keys each pin by the project the registration names.
	verb, args, flags := registration(host.Projects, &rep)
	pins := pinSource{store: userspace.Store{Dir: filepath.Join(host.StateDir, userSkillsDir)}, fetch: fetchSkill(release.SkillFetcher{})}
	if err := registerSkills(ctx, dir, &rep, pins, nil); err != nil {
		return fail(err)
	}
	var next []string
	if len(rep.CI) == 0 {
		next = append(next, "the repository has no CI yet; start a harness session in "+rep.Path+" and say \"set up my loop\"")
	}
	rep.Next = strings.Join(next, "; then ")

	if verb == "" {
		return cli.Found(rep)
	}
	if res := h.remote(ctx, ctl, verb, args, flags); res.Refusal != nil {
		return res
	}
	rep.Added = verb == "project add"
	return cli.Found(rep)
}

// registration names the server verb that makes the registration match
// the clone, with its arguments: project add for a new clone, project move
// for a repository that moved on GitHub or a default branch that the
// registration does not name. It answers no verb when the registration is
// already right. It sets the report's project and the repository the
// project moved from.
func registration(projects []config.Project, rep *RegisterReport) (verb string, args []string, flags map[string]string) {
	branchFlag := func(flags map[string]string) map[string]string {
		if rep.Branch != "" {
			flags["branch"] = rep.Branch
		}
		return flags
	}
	for _, p := range projects {
		if strings.EqualFold(p.Repo, rep.Repo) {
			rep.Project = p.ID
			// A clone with no commit has no default branch to record.
			if rep.Branch == "" || p.Branch == rep.Branch {
				return "", nil, nil
			}
			return "project move", []string{p.ID}, branchFlag(map[string]string{"repo": rep.Repo})
		}
	}
	// The project of this clone names another repository: the repository
	// moved on GitHub, and origin follows it.
	for _, p := range projects {
		if p.Path == rep.Path {
			rep.Project, rep.MovedFrom = p.ID, p.Repo
			return "project move", []string{p.ID}, branchFlag(map[string]string{"repo": rep.Repo})
		}
	}
	return "project add", []string{rep.Project}, branchFlag(map[string]string{"repo": rep.Repo, "path": rep.Path})
}

// remote sends one server verb, and returns its answer with the App hint
// on a refusal that names the installation.
func (h *hostVerbs) remote(ctx context.Context, ctl *serverControl, path string, args []string, flags map[string]string) cli.Result {
	v, ok := h.reg.Verb(path)
	if !ok {
		return fail(errors.New("this build has no " + path + " verb"))
	}
	res := ctl.client().Remote(ctx, cli.Invocation{Verb: v, Args: args, Flags: flags, RequestID: newRequestID()})
	if r := res.Refusal; r != nil && strings.Contains(r.Hint, "installation") {
		return cli.Reject(r.Code, r.Rule, r.Hint+"; install the loop App on "+flags["repo"]+" from the App's page on GitHub, then run loop register again")
	}
	return res
}

// pinSource is where register takes a project's userspace pins: the
// host's store, which the server shares, and the fetch of each pin the
// store lacks.
type pinSource struct {
	store userspace.Store
	fetch userspace.Fetch
}

// registerSkills builds the pack from the instance's fetched skill set,
// as loop serve does, selects its skills at the epic level, adds the
// project's userspace pins, and installs it into the clone at rep.Path.
// It records the skills in rep. A pin that fails, or a config that does
// not load, does not fail the register. A config that does not load gives
// no level. build is the test seam: nil is pack.Brief, whose pins the
// binary embeds.
func registerSkills(ctx context.Context, instance string, rep *RegisterReport, pins pinSource, build func(fetched fs.FS) (brief.Pack, []pack.ThirdParty, error)) error {
	if build == nil {
		build = pack.Brief
	}
	p, _, err := build(fetchedSkills(instance))
	if err != nil {
		return err
	}
	cfg, err := projectConfig(ctx, rep)
	if err != nil {
		rep.UserSkillsConfig = err.Error()
	}
	p = sched.Select(p, cfg, sched.Scope{Level: sched.EpicLevel}).WithUser(userSkills(ctx, rep, pins, cfg))
	rep.Skills, err = installSkills(ctx, rep.Path, p)
	return err
}

// projectConfig reads the project's config at origin/<branch> of the
// clone, as the server reads it (KTD20). Its error is a config that does
// not load.
func projectConfig(ctx context.Context, rep *RegisterReport) (*config.ProjectConfig, error) {
	refs, _, err := server.LocalRefs(ctx, config.Project{Path: rep.Path, Branch: rep.Branch})
	if err != nil {
		return nil, err
	}
	snap, err := config.ReadProjectSnapshot(ctx, refs.Read, rep.Branch)
	if err != nil {
		return nil, err
	}
	if err := config.SkillDirs(ctx, snap.Config, refs.Tree, config.DefaultRef(rep.Branch)); err != nil {
		return nil, err
	}
	return snap.Config, nil
}

// userSkills returns each pin of cfg that the store holds or that a fetch
// puts into it. It records each written and each failed pin in rep. A nil
// cfg holds no pin.
func userSkills(ctx context.Context, rep *RegisterReport, pins pinSource, cfg *config.ProjectConfig) []brief.UserSkill {
	if cfg == nil {
		return nil
	}
	var out []brief.UserSkill
	for _, pin := range cfg.Skills {
		h, err := pins.take(ctx, rep.Project, pin)
		if err != nil {
			rep.UserSkillsFailed = append(rep.UserSkillsFailed, PinFailure{Name: pin.Name, Reason: err.Error()})
			continue
		}
		out = append(out, brief.UserSkill{Pin: h.Pin, Tree: h.Tree})
		rep.UserSkills = append(rep.UserSkills, pin.Name)
	}
	return out
}

// take returns pin of project from the store, or fetches it and puts it
// into the store first.
func (s pinSource) take(ctx context.Context, project string, pin config.SkillPin) (userspace.Held, error) {
	if h, ok := s.store.Held(project, pin); ok {
		return h, nil
	}
	tree, err := s.fetch(ctx, pin)
	if err != nil {
		return userspace.Held{}, err
	}
	if err := s.store.Put(project, pin, tree); err != nil {
		return userspace.Held{}, err
	}
	if h, ok := s.store.Held(project, pin); ok {
		return h, nil
	}
	return userspace.Held{}, fmt.Errorf("the store holds no tree of %s after its fetch", pin.Name)
}

// installSkills writes the pack's skills, user skills and agents into the
// clone for Claude Code and Codex, and keeps them out of git through
// info/exclude, as a run's checkout does. Register passes the pack the
// server injects, so a clone holds each fetched skill a run holds. A
// tracked loop- skill directory the pack lacks refuses the install, and
// each stale untracked one is cleared. It returns the kernel skills.
func installSkills(ctx context.Context, clone string, p brief.Pack) ([]string, error) {
	skills, err := p.Skills()
	if err != nil {
		return nil, err
	}
	users, err := p.UserSkills()
	if err != nil {
		return nil, err
	}
	if err := brief.CheckReserved(ctx, clone, p); err != nil {
		return nil, err
	}
	var files, dirs []string
	for _, h := range []agenthost.Harness{agenthost.Claude, agenthost.Codex} {
		if err := brief.Prune(ctx, clone, h, p); err != nil {
			return nil, err
		}
		if err := p.Write(clone, h); err != nil {
			return nil, err
		}
		for _, s := range slices.Concat(skills, users) {
			dirs = append(dirs, brief.SkillRoot(h)+"/"+s)
		}
		agents, err := p.AgentFiles(h)
		if err != nil {
			return nil, err
		}
		files = append(files, agents...)
	}
	return skills, brief.Exclude(ctx, clone, files, dirs)
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
