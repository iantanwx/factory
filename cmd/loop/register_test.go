package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeGH answers gh calls from a table keyed by the joined arguments and
// records each call with its stdin.
type fakeGH struct {
	answers map[string]string
	calls   []string
	stdin   map[string][]byte
}

func (f *fakeGH) run(_ context.Context, _ string, stdin []byte, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if stdin != nil {
		f.stdin[key] = stdin
	}
	out, ok := f.answers[key]
	if !ok {
		return nil, errors.New(key + ": HTTP 404")
	}
	return []byte(out), nil
}

func TestRegisterSteps(t *testing.T) {
	gh := &fakeGH{stdin: map[string][]byte{}, answers: map[string]string{
		"gh api repos/me/app/commits/main/check-runs":             `{"check_runs":[{"name":"test"},{"name":"loop/eligible"},{"name":"test"}]}`,
		"gh api repos/me/app/commits/main/status":                 `{"statuses":[{"context":"lint"}]}`,
		"gh api repos/me/app/rulesets":                            `[{"id":7,"name":"other"}]`,
		"gh api -X POST repos/me/app/rulesets --input - --silent": ``,
	}}
	h := &hostVerbs{cmd: gh.run}
	ctx := context.Background()
	rep := RegisterReport{Repo: "me/app", Branch: "main", Path: "/src/app"}

	ci, err := h.ciChecks(ctx, rep)
	if err != nil || !slices.Equal(ci, []string{"lint", "test"}) {
		t.Fatalf("ci = %v %v", ci, err)
	}
	rep.CI = ci
	got, err := h.ensureRuleset(ctx, rep, 42)
	if err != nil || got != "created" {
		t.Fatalf("ruleset = %q %v", got, err)
	}
	raw := string(gh.stdin["gh api -X POST repos/me/app/rulesets --input - --silent"])
	for _, want := range []string{`"context":"lint"`, `"context":"test"`, `"context":"loop/eligible","integration_id":42`,
		`"required_approving_review_count":0`, `"~DEFAULT_BRANCH"`, `"actor_type":"RepositoryRole"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("the ruleset lacks %s: %s", want, raw)
		}
	}

	// A second run updates the ruleset in place.
	gh.answers["gh api repos/me/app/rulesets"] = `[{"id":9,"name":"loop"}]`
	gh.answers["gh api -X PUT repos/me/app/rulesets/9 --input - --silent"] = ``
	if got, err := h.ensureRuleset(ctx, rep, 42); err != nil || got != "updated" {
		t.Fatalf("ruleset again = %q %v", got, err)
	}
}

// A private repository on a free plan has no rulesets; register goes on.
func TestRegisterWithoutRulesets(t *testing.T) {
	h := &hostVerbs{cmd: func(context.Context, string, []byte, string, ...string) ([]byte, error) {
		return nil, errors.New("gh: Upgrade to GitHub Pro or make this repository public to enable this feature. (HTTP 403)")
	}}
	if got, err := h.ensureRuleset(context.Background(), RegisterReport{Repo: "me/app"}, 42); err != nil || got != "unavailable" {
		t.Fatalf("ruleset = %q %v", got, err)
	}
}

// The skills land under both harnesses' roots, the agents under Claude
// Code's, and git ignores them.
func TestRegisterInstallsTheSkills(t *testing.T) {
	clone := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", clone).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	skills, err := installSkills(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(skills, "loop-init") {
		t.Fatalf("skills = %v; want loop-init", skills)
	}
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		if _, err := os.Stat(filepath.Join(clone, root, "loop-init", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(clone, ".claude/agents/loop-escalation-prep.md")); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", clone, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil || len(out) != 0 {
		t.Fatalf("git sees the skills: %q %v", out, err)
	}
}

func TestProjectID(t *testing.T) {
	for repo, want := range map[string]string{"me/My.App": "my-app", "me/2048": "p-2048", "me/tool_x": "tool_x"} {
		if got := projectID(repo); got != want {
			t.Errorf("%s: %q, want %q", repo, got, want)
		}
	}
}
