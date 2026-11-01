package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx/gittest"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/userspace"
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

// TestC31TheRulesetRequiresLoopQAFromTheLoopApp: the ruleset that register
// makes requires loop/qa and loop/eligible from the loop App, and the CI
// discovery leaves both out (C30).
func TestC31TheRulesetRequiresLoopQAFromTheLoopApp(t *testing.T) {
	gh := &fakeGH{stdin: map[string][]byte{}, answers: map[string]string{
		"gh api repos/me/app/commits/main/check-runs":             `{"check_runs":[{"name":"test"},{"name":"loop/qa"}]}`,
		"gh api repos/me/app/commits/main/status":                 `{"statuses":[{"context":"loop/eligible"}]}`,
		"gh api repos/me/app/rulesets":                            `[]`,
		"gh api -X POST repos/me/app/rulesets --input - --silent": ``,
	}}
	h := &hostVerbs{cmd: gh.run}
	ctx := context.Background()
	rep := RegisterReport{Repo: "me/app", Branch: "main", Path: "/src/app"}
	ci, err := h.ciChecks(ctx, rep)
	if err != nil || !slices.Equal(ci, []string{"test"}) {
		t.Fatalf("ci = %v %v, want [test]", ci, err)
	}
	rep.CI = ci
	if _, err := h.ensureRuleset(ctx, rep, 42); err != nil {
		t.Fatal(err)
	}
	raw := string(gh.stdin["gh api -X POST repos/me/app/rulesets --input - --silent"])
	for _, want := range []string{`"context":"loop/qa","integration_id":42`, `"context":"loop/eligible","integration_id":42`} {
		if !strings.Contains(raw, want) {
			t.Errorf("the ruleset lacks %s: %s", want, raw)
		}
	}
	if strings.Count(raw, `"context":"loop/qa"`) != 1 {
		t.Errorf("the ruleset requires loop/qa more than once: %s", raw)
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
	if out, err := gittest.Command("init", "-q", clone).CombinedOutput(); err != nil {
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
	out, err := gittest.Command("-C", clone, "status", "--porcelain", "--untracked-files=all").Output()
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
		cmd := gittest.Command(append([]string{"-C", clone, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
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
	out, err := gittest.Command("-C", clone, "status", "--porcelain", "--untracked-files=no").Output()
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
	if out, err := gittest.Command("init", "-q", clone).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	// Register builds its pack from the instance's fetched set. The
	// builder pins the fixture skill, as pack.Brief pins the real ones.
	build := func(fetched fs.FS) (brief.Pack, []pack.ThirdParty, error) { return pack.BriefOf(src, fetched) }
	if err := registerSkills(context.Background(), instance, &RegisterReport{Path: clone}, pinSource{}, build); err != nil {
		t.Fatal(err)
	}
	p, left, err := pack.BriefOf(src, os.DirFS(skills))
	if err != nil || len(left) != 0 {
		t.Fatalf("left out %v, %v", left, err)
	}
	for _, h := range []agenthost.Harness{agenthost.Claude, agenthost.Codex} {
		run := t.TempDir()
		if out, err := gittest.Command("init", "-q", run).CombinedOutput(); err != nil {
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

// c17Tree is a skill tree whose SKILL.md names name, with one executable
// file.
func c17Tree(name string) fstest.MapFS {
	return fstest.MapFS{
		"SKILL.md":         {Data: []byte("---\nname: " + name + "\n---\n\nUse " + name + ".\n"), Mode: 0o644},
		"scripts/check.sh": {Data: []byte("#!/bin/sh\necho " + name + "\n"), Mode: 0o755},
	}
}

// c17Pin is the pin named name whose sha256 is tree's hash.
func c17Pin(t *testing.T, name string, tree fs.FS) config.SkillPin {
	t.Helper()
	sum, err := pack.TreeHash(tree)
	if err != nil {
		t.Fatal(err)
	}
	return config.SkillPin{Name: name, Repo: "neondatabase/agent-skills",
		Commit: "ccca3334a7542d3133134cc6926f2fafc6edeb1f", Path: "skills/" + name, SHA256: sum}
}

// skillsConfig is a project config that pins each of pins.
func skillsConfig(pins ...config.SkillPin) string {
	out := "merge:\n  mechanism: direct\nskills:\n"
	for _, p := range pins {
		out += fmt.Sprintf("  - name: %s\n    repo: %s\n    commit: %s\n    path: %s\n    sha256: %s\n",
			p.Name, p.Repo, p.Commit, p.Path, p.SHA256)
	}
	return out
}

// originClone is a clone with one commit that holds each file of files
// with its body, and origin/main at that commit.
func originClone(t *testing.T, files map[string]string) string {
	t.Helper()
	clone := t.TempDir()
	for f, body := range files {
		writeClone(t, clone, f, body)
	}
	gitIn(t, clone, "init", "-q")
	gitIn(t, clone, "add", "-A")
	gitIn(t, clone, "commit", "-q", "-m", "init")
	gitIn(t, clone, "update-ref", "refs/remotes/origin/main", "HEAD")
	return clone
}

func gitIn(t *testing.T, clone string, args ...string) {
	t.Helper()
	cmd := gittest.Command(append([]string{"-C", clone, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// fakeFetch serves each pin from trees, and records each name it fetched.
type fakeFetch struct {
	trees   map[string]fs.FS
	fetched []string
}

func (f *fakeFetch) fetch(_ context.Context, pin config.SkillPin) (fs.FS, error) {
	f.fetched = append(f.fetched, pin.Name)
	if tree, ok := f.trees[pin.Name]; ok {
		return tree, nil
	}
	return nil, errors.New("fetch " + pin.Name + ": the network is down")
}

// C17: register writes the pins of its project's config at origin/<branch>
// into the clone, from the store or a fetch, and no other project's pins.
// A failed pin does not fail the register. A config that does not load
// writes the kernel skills only, and a skills: list that only the
// worktree holds writes no pin.
func TestC17RegisterWritesItsProjectsPinsAndNoOther(t *testing.T) {
	ctx := context.Background()
	neonTree, pgTree, otherTree := c17Tree("neon"), c17Tree("neon-postgres"), c17Tree("house-style")
	neon, pg, other := c17Pin(t, "neon", neonTree), c17Pin(t, "neon-postgres", pgTree), c17Pin(t, "house-style", otherTree)
	wantNeon := map[string]string{
		"SKILL.md":         `644 "---\nname: neon\n---\n\nUse neon.\n"`,
		"scripts/check.sh": `755 "#!/bin/sh\necho neon\n"`,
	}
	noDir := func(t *testing.T, clone string, names ...string) {
		t.Helper()
		for _, root := range []string{".claude/skills", ".agents/skills"} {
			for _, n := range names {
				if _, err := os.Stat(filepath.Join(clone, root, n)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("the clone holds %s/%s (%v)", root, n, err)
				}
			}
			if _, err := os.Stat(filepath.Join(clone, root, "loop-init", "SKILL.md")); err != nil {
				t.Errorf("the clone holds no kernel skill: %v", err)
			}
		}
	}

	t.Run("its own pins", func(t *testing.T) {
		store := userspace.Store{Dir: t.TempDir()}
		if err := store.Put("p1", pg, pgTree); err != nil {
			t.Fatal(err)
		}
		if err := store.Put("p2", other, otherTree); err != nil {
			t.Fatal(err)
		}
		clone := originClone(t, map[string]string{".loop/config.yaml": skillsConfig(neon, pg)})
		f := &fakeFetch{trees: map[string]fs.FS{"neon": neonTree}}
		rep := RegisterReport{Project: "p1", Branch: "main", Path: clone}
		if err := registerSkills(ctx, t.TempDir(), &rep, pinSource{store: store, fetch: f.fetch}, nil); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rep.UserSkills, []string{"neon", "neon-postgres"}) || rep.UserSkillsFailed != nil || rep.UserSkillsConfig != "" {
			t.Fatalf("user_skills = %v, failed %v, config %q", rep.UserSkills, rep.UserSkillsFailed, rep.UserSkillsConfig)
		}
		if !slices.Equal(f.fetched, []string{"neon"}) {
			t.Fatalf("fetched %v; want neon only, as the store holds neon-postgres", f.fetched)
		}
		for _, root := range []string{".claude/skills", ".agents/skills"} {
			if got := tree(t, filepath.Join(clone, root, "neon")); !maps.Equal(got, wantNeon) {
				t.Errorf("%s/neon = %v", root, got)
			}
			if got := tree(t, filepath.Join(clone, root, "neon-postgres")); got["SKILL.md"] != `644 "---\nname: neon-postgres\n---\n\nUse neon-postgres.\n"` {
				t.Errorf("%s/neon-postgres = %v", root, got)
			}
		}
		noDir(t, clone, "house-style")
		if _, ok := store.Held("p1", neon); !ok {
			t.Error("the fetched neon is not in the store")
		}
		out, err := gittest.Command("-C", clone, "status", "--porcelain", "--untracked-files=all").Output()
		if err != nil || len(out) != 0 {
			t.Fatalf("git sees the skills: %q %v", out, err)
		}
	})

	t.Run("a failed pin", func(t *testing.T) {
		bad := c17Pin(t, "broken", c17Tree("broken"))
		clone := originClone(t, map[string]string{".loop/config.yaml": skillsConfig(neon, bad)})
		f := &fakeFetch{trees: map[string]fs.FS{"neon": neonTree}}
		rep := RegisterReport{Project: "p1", Branch: "main", Path: clone}
		if err := registerSkills(ctx, t.TempDir(), &rep, pinSource{store: userspace.Store{Dir: t.TempDir()}, fetch: f.fetch}, nil); err != nil {
			t.Fatalf("a failed pin failed the register: %v", err)
		}
		want := []PinFailure{{Name: "broken", Reason: "fetch broken: the network is down"}}
		if !slices.Equal(rep.UserSkills, []string{"neon"}) || !slices.Equal(rep.UserSkillsFailed, want) {
			t.Fatalf("user_skills = %v, failed %v; want neon, and %v", rep.UserSkills, rep.UserSkillsFailed, want)
		}
		noDir(t, clone, "broken")
	})

	t.Run("a config that does not load", func(t *testing.T) {
		clone := originClone(t, map[string]string{
			".loop/config.yaml":            skillsConfig(neon),
			".claude/skills/neon/SKILL.md": "# committed\n",
		})
		f := &fakeFetch{trees: map[string]fs.FS{"neon": neonTree}}
		rep := RegisterReport{Project: "p1", Branch: "main", Path: clone}
		if err := registerSkills(ctx, t.TempDir(), &rep, pinSource{store: userspace.Store{Dir: t.TempDir()}, fetch: f.fetch}, nil); err != nil {
			t.Fatal(err)
		}
		if rep.UserSkills != nil || f.fetched != nil || !strings.Contains(rep.UserSkillsConfig, "origin/main tracks .claude/skills/neon") {
			t.Fatalf("user_skills = %v, fetched %v, config %q", rep.UserSkills, f.fetched, rep.UserSkillsConfig)
		}
		if got, err := os.ReadFile(filepath.Join(clone, ".claude/skills/neon/SKILL.md")); err != nil || string(got) != "# committed\n" {
			t.Fatalf(".claude/skills/neon/SKILL.md = %q, %v", got, err)
		}
		noDir(t, clone)
		if _, err := os.Stat(filepath.Join(clone, ".agents/skills/neon")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the clone holds .agents/skills/neon (%v)", err)
		}
	})

	t.Run("a skills list only the worktree holds", func(t *testing.T) {
		clone := originClone(t, map[string]string{".loop/config.yaml": "merge:\n  mechanism: direct\n"})
		writeClone(t, clone, ".loop/config.yaml", skillsConfig(neon))
		gitIn(t, clone, "commit", "-q", "-am", "pin neon on a local branch")
		f := &fakeFetch{trees: map[string]fs.FS{"neon": neonTree}}
		rep := RegisterReport{Project: "p1", Branch: "main", Path: clone}
		if err := registerSkills(ctx, t.TempDir(), &rep, pinSource{store: userspace.Store{Dir: t.TempDir()}, fetch: f.fetch}, nil); err != nil {
			t.Fatal(err)
		}
		if rep.UserSkills != nil || f.fetched != nil || rep.UserSkillsConfig != "" {
			t.Fatalf("user_skills = %v, fetched %v, config %q", rep.UserSkills, f.fetched, rep.UserSkillsConfig)
		}
		noDir(t, clone, "neon")
	})
}

// C19: register gives the clone the skills of the epic level, from the
// config at origin/<branch>. A clone of a project with no ui key holds no
// loop-impeccable, and loses the untracked one an earlier register wrote.
// A ui block gives it the skill. A config that does not load gives no
// level, and the register still succeeds.
func TestC19RegisterFollowsTheEpicLevel(t *testing.T) {
	instance := t.TempDir()
	skills := release.Layout{Dir: instance}.Skills(cli.Version)
	writeSkill(t, skills, "impeccable")
	src := pinnedSource(t, skills, "impeccable")
	src["manifest.json"] = &fstest.MapFile{Data: []byte(`{"runbooks": {"implementer": {"skills": []}}, "skills": [],
		"conditional": {"loop-impeccable": {"key": "ui"}}}`)}
	build := func(fetched fs.FS) (brief.Pack, []pack.ThirdParty, error) { return pack.BriefOf(src, fetched) }
	held := func(t *testing.T, clone string) []string {
		t.Helper()
		var out []string
		for _, root := range []string{".claude/skills", ".agents/skills"} {
			if _, err := os.Stat(filepath.Join(clone, root, "loop-impeccable", "SKILL.md")); err == nil {
				out = append(out, root)
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
		return out
	}

	for name, tc := range map[string]struct {
		config     string
		want       []string
		configFail bool
	}{
		"no ui key":                   {config: "merge:\n  mechanism: direct\n"},
		"a ui block":                  {config: "merge:\n  mechanism: direct\nui:\n  kind: web\n  paths: [web/**]\n", want: []string{".claude/skills", ".agents/skills"}},
		"a config that does not load": {config: "merge:\n  mechanism: direct\nui:\n  kind: desktop\n  paths: [web/**]\n", configFail: true},
	} {
		t.Run(name, func(t *testing.T) {
			clone := originClone(t, map[string]string{".loop/config.yaml": tc.config})
			// An earlier register wrote the skill, untracked.
			for _, root := range []string{".claude/skills", ".agents/skills"} {
				writeClone(t, clone, root+"/loop-impeccable/SKILL.md", "# an earlier register\n")
			}
			rep := RegisterReport{Project: "p1", Branch: "main", Path: clone}
			if err := registerSkills(context.Background(), instance, &rep, pinSource{store: userspace.Store{Dir: t.TempDir()}}, build); err != nil {
				t.Fatalf("the register failed: %v", err)
			}
			if got := held(t, clone); !slices.Equal(got, tc.want) {
				t.Fatalf("loop-impeccable under %v; want %v", got, tc.want)
			}
			if slices.Contains(rep.Skills, "loop-impeccable") != (tc.want != nil) {
				t.Fatalf("skills = %v", rep.Skills)
			}
			if (rep.UserSkillsConfig != "") != tc.configFail {
				t.Fatalf("user_skills_config = %q", rep.UserSkillsConfig)
			}
			if !slices.Contains(rep.Skills, "loop-implementer") {
				t.Fatalf("skills = %v; want the kernel", rep.Skills)
			}
		})
	}
}
