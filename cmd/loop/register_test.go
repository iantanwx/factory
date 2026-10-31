package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/release"
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
	p, _, err := pack.Brief(nil)
	if err != nil {
		t.Fatal(err)
	}
	skills, err := installSkills(context.Background(), clone, p)
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

// committedClone is a clone with one commit that tracks each of files.
func committedClone(t *testing.T, files ...string) string {
	t.Helper()
	clone := t.TempDir()
	for _, f := range files {
		writeClone(t, clone, f, f+"\n")
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", clone, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return clone
}

func writeClone(t *testing.T, clone, p, body string) {
	t.Helper()
	full := filepath.Join(clone, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// C5: register refuses a clone that tracks a loop- skill directory the
// pack lacks, and writes no skill.
func TestRegisterRefusesATrackedReservedSkillDir(t *testing.T) {
	clone := committedClone(t, ".agents/skills/loop-x/SKILL.md")
	p, _, err := pack.Brief(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = installSkills(context.Background(), clone, p)
	var re *brief.ReservedError
	if !errors.As(err, &re) || !slices.Equal(re.Dirs, []string{".agents/skills/loop-x"}) {
		t.Fatalf("err = %v; want a *ReservedError for .agents/skills/loop-x", err)
	}
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		if _, err := os.Stat(filepath.Join(clone, root, "loop-init")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a refused register wrote %s/loop-init", root)
		}
	}
}

// C6: register clears each untracked loop- skill directory the pack lacks
// under both skill roots, keeps an untracked skill outside the prefix, and
// changes no tracked file.
func TestRegisterClearsOnlyStaleUntrackedLoopDirs(t *testing.T) {
	clone := committedClone(t, ".claude/skills/committed/SKILL.md")
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		writeClone(t, clone, root+"/loop-neon/SKILL.md", "# stale\n")
		writeClone(t, clone, root+"/house-style/SKILL.md", "# house style\n")
	}
	p, _, err := pack.Brief(nil)
	if err != nil {
		t.Fatal(err)
	}
	if skills, _ := p.Skills(); slices.Contains(skills, "loop-neon") {
		t.Fatal("the pack holds loop-neon, so it is no stale directory")
	}
	if _, err := installSkills(context.Background(), clone, p); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		if _, err := os.Stat(filepath.Join(clone, root, "loop-neon")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the stale %s/loop-neon is still there (%v)", root, err)
		}
		if got, err := os.ReadFile(filepath.Join(clone, root, "house-style", "SKILL.md")); err != nil || string(got) != "# house style\n" {
			t.Fatalf("%s/house-style/SKILL.md = %q, %v", root, got, err)
		}
		if _, err := os.Stat(filepath.Join(clone, root, "loop-init", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command("git", "-C", clone, "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil || len(out) != 0 {
		t.Fatalf("register changed a tracked file: %q %v", out, err)
	}
}

// tree reads each file under dir with its mode, by slash path.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out[filepath.ToSlash(rel)] = fmt.Sprintf("%o %q", fi.Mode().Perm(), data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// C14: after loop register, the clone's .claude/skills and .agents/skills
// each hold the trees a run's checkout holds for that harness from the
// same fetched set, with the same bytes and modes.
func TestARegisteredCloneHoldsWhatARunHolds(t *testing.T) {
	instance := t.TempDir()
	skills := release.Layout{Dir: instance}.Skills(cli.Version)
	writeSkill(t, skills, "impeccable")
	src := pinnedSource(t, skills, "impeccable")
	clone := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", clone).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	// Register builds its pack from the instance's fetched set. The
	// builder pins the fixture skill, as pack.Brief pins the real ones.
	build := func(fetched fs.FS) (brief.Pack, []pack.ThirdParty, error) { return pack.BriefOf(src, fetched) }
	if _, err := registerSkills(context.Background(), instance, clone, build); err != nil {
		t.Fatal(err)
	}
	p, left, err := pack.BriefOf(src, os.DirFS(skills))
	if err != nil || len(left) != 0 {
		t.Fatalf("left out %v, %v", left, err)
	}
	for _, h := range []agenthost.Harness{agenthost.Claude, agenthost.Codex} {
		run := t.TempDir()
		if out, err := exec.Command("git", "init", "-q", run).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		_, err := brief.Inject(context.Background(), brief.Injection{Checkout: run, Harness: h, Pack: p,
			Brief: brief.Brief{RunID: "r-1", Project: "alpha", Stage: "implementer", Role: "implementer"}})
		if err != nil {
			t.Fatal(err)
		}
		root := brief.SkillRoot(h)
		got, want := tree(t, filepath.Join(clone, root)), tree(t, filepath.Join(run, root))
		if !maps.Equal(got, want) {
			t.Errorf("%s: the clone holds\n%v\nand a run holds\n%v", root, got, want)
		}
		if want["loop-impeccable/scripts/impeccable"] == "" {
			t.Errorf("%s: a run holds no loop-impeccable: %v", root, want)
		}
	}
}

func TestProjectID(t *testing.T) {
	for repo, want := range map[string]string{"me/My.App": "my-app", "me/2048": "p-2048", "me/tool_x": "tool_x"} {
		if got := projectID(repo); got != want {
			t.Errorf("%s: %q, want %q", repo, got, want)
		}
	}
}

// register records the clone's default branch: project add names it for a
// new clone, and project move records it for a registration that names
// another branch or a moved repository. A registration that is already
// right sends nothing, and so does a clone with no commit, which has no
// default branch yet.
func TestRegisterRecordsTheDefaultBranch(t *testing.T) {
	registered := []config.Project{
		{ID: "app", Repo: "me/app", Path: "/src/app"},
		{ID: "tool", Repo: "me/tool", Path: "/src/tool", Branch: "trunk"},
	}
	for name, tc := range map[string]struct {
		rep           RegisterReport
		verb, project string
		flags         map[string]string
		movedFrom     string
	}{
		"new clone": {RegisterReport{Project: "web", Repo: "me/web", Path: "/src/web", Branch: "develop"},
			"project add", "web", map[string]string{"repo": "me/web", "path": "/src/web", "branch": "develop"}, ""},
		"new empty clone": {RegisterReport{Project: "web", Repo: "me/web", Path: "/src/web"},
			"project add", "web", map[string]string{"repo": "me/web", "path": "/src/web"}, ""},
		"branch not recorded": {RegisterReport{Project: "app", Repo: "me/app", Path: "/src/app", Branch: "trunk"},
			"project move", "app", map[string]string{"repo": "me/app", "branch": "trunk"}, ""},
		"branch renamed": {RegisterReport{Project: "x", Repo: "ME/tool", Path: "/src/tool", Branch: "main"},
			"project move", "tool", map[string]string{"repo": "ME/tool", "branch": "main"}, ""},
		"already right": {RegisterReport{Project: "x", Repo: "me/tool", Path: "/src/tool", Branch: "trunk"},
			"", "tool", nil, ""},
		"empty repository": {RegisterReport{Project: "x", Repo: "me/app", Path: "/src/app"},
			"", "app", nil, ""},
		"moved repository": {RegisterReport{Project: "x", Repo: "acme/tool", Path: "/src/tool", Branch: "trunk"},
			"project move", "tool", map[string]string{"repo": "acme/tool", "branch": "trunk"}, "me/tool"},
	} {
		t.Run(name, func(t *testing.T) {
			rep := tc.rep
			verb, args, flags := registration(registered, &rep)
			if verb != tc.verb || rep.Project != tc.project || rep.MovedFrom != tc.movedFrom {
				t.Fatalf("registration = %q %v, project %q moved from %q", verb, args, rep.Project, rep.MovedFrom)
			}
			if verb != "" && (!slices.Equal(args, []string{tc.project}) || !maps.Equal(flags, tc.flags)) {
				t.Fatalf("registration = %q %v %v; want flags %v", verb, args, flags, tc.flags)
			}
		})
	}
}
