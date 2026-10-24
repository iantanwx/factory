package brief

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/secrets"
)

// gitRepo makes a repository with one commit that tracks .loop/config.yaml
// and .loop/secrets.yaml, as a project does, plus extra tracked files.
func gitRepo(t *testing.T, extra ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	files := append([]string{".loop/config.yaml", ".loop/secrets.yaml", "README.md"}, extra...)
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func testPack() Pack {
	return Pack{Version: "v1.0.0", FS: fstest.MapFS{
		"loop-implementer/SKILL.md":          {Data: []byte("# implementer runbook\n")},
		"loop-implementer/refs/checklist.md": {Data: []byte("- one\n")},
		"loop-code-reviewer/SKILL.md":        {Data: []byte("# reviewer runbook\n")},
	}, Agents: fstest.MapFS{
		"loop-helper.md": {Data: []byte("---\nname: loop-helper\n---\n")},
	}}
}

func testBrief() Brief {
	return Brief{
		RunID: "r-1", Project: "alpha", Stage: "implementer", Role: "implementer",
		Harness: "claude", Model: "opus", BaseBranch: "main",
		Item: &Item{ID: "tk-1", Kind: "ticket", Title: "Fix it", Status: "in_progress",
			Body: "See `docs/plans/x.md`.", Documents: []string{"docs/plans/x.md"}},
		Attempt: 2, PriorFailures: []string{"tests failed"},
		AllowedNext: []string{"pr_open", "release"},
		Secrets:     Secrets{Present: []string{"STRIPE_KEY"}},
	}
}

// inject writes env into the root .env.
func inject(t *testing.T, dir string, env map[string]string) Result {
	t.Helper()
	var files EnvFiles
	if env != nil {
		files = EnvFiles{"": env}
	}
	return injectFiles(t, dir, files)
}

func injectFiles(t *testing.T, dir string, files EnvFiles) Result {
	t.Helper()
	res, err := Inject(context.Background(), Injection{
		Checkout: dir, Harness: agenthost.Claude, Pack: testPack(), Brief: testBrief(), Env: files,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The brief holds the item, attempt n of 2 and allowed_next. It names the
// secrets and holds no value.
func TestBriefHoldsItemAttemptAndSecretNamesOnly(t *testing.T) {
	dir := gitRepo(t)
	const value = "sk_live_super_secret_value"
	res := inject(t, dir, map[string]string{"STRIPE_KEY": value})
	raw, err := os.ReadFile(filepath.Join(dir, BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if b.Item == nil || b.Item.ID != "tk-1" || b.Attempt != 2 || b.MaxAttempts != 2 ||
		!slices.Equal(b.AllowedNext, []string{"pr_open", "release"}) || !slices.Equal(b.Secrets.Present, []string{"STRIPE_KEY"}) {
		t.Fatalf("brief = %+v", b)
	}
	if b.PackHash == "" || b.PackHash != res.PackHash || b.PackVersion != "v1.0.0" {
		t.Fatalf("pack hash %q, result %q", b.PackHash, res.PackHash)
	}
	if b.Runbook != ".claude/skills/loop-implementer/SKILL.md" {
		t.Fatalf("runbook = %q", b.Runbook)
	}
	md, _ := os.ReadFile(filepath.Join(dir, BriefMD))
	prompt, _ := os.ReadFile(res.Prompt)
	for name, text := range map[string]string{"brief.json": string(raw), "brief.md": string(md), "prompt": string(prompt)} {
		if strings.Contains(text, value) {
			t.Fatalf("%s holds a secret value", name)
		}
	}
	if !strings.Contains(string(md), "attempt 2 of 2") || !strings.Contains(string(md), "STRIPE_KEY") {
		t.Fatalf("brief.md:\n%s", md)
	}
	if !strings.Contains(string(prompt), BriefMD) || !strings.Contains(string(prompt), b.Runbook) {
		t.Fatalf("the prompt names the brief and the runbook:\n%s", prompt)
	}
	env, _ := os.ReadFile(filepath.Join(dir, EnvFile))
	if !strings.Contains(string(env), value) {
		t.Fatal(".env lacks the value")
	}
	if fi, _ := os.Stat(filepath.Join(dir, EnvFile)); fi.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode = %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude/skills/loop-implementer/refs/checklist.md")); err != nil {
		t.Fatalf("the pack is not injected: %v", err)
	}
}

// Claude Code reads agents from .claude/agents. Each pack agent lands
// there, beside a project's own agents.
func TestInjectionWritesEachAgentForClaude(t *testing.T) {
	dir := gitRepo(t, ".claude/agents/mine.md")
	inject(t, dir, nil)
	body, err := os.ReadFile(filepath.Join(dir, ".claude/agents/loop-helper.md"))
	if err != nil || !strings.Contains(string(body), "name: loop-helper") {
		t.Fatalf("agent: %q, %v", body, err)
	}
	if out := git(t, dir, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("git status after injection:\n%s", out)
	}
	bad := testPack()
	bad.Agents = fstest.MapFS{"helper.md": {Data: []byte("x")}}
	if _, err := bad.AgentNames(); err == nil {
		t.Fatal("an agent outside the loop- namespace is accepted")
	}
}

// After injection git status is clean, and git add -A stages no injected
// path. The tracked project files under .loop stay visible to git.
func TestInjectionLeavesGitClean(t *testing.T) {
	dir := gitRepo(t)
	inject(t, dir, map[string]string{"A": "1"})
	// The CLI and the server write these into the checkout later.
	for _, f := range []string{".loop/token", ".loop/pending.jsonl", ".loop/pending.lock"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out := git(t, dir, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("git status after injection:\n%s", out)
	}
	git(t, dir, "add", "-A")
	if out := git(t, dir, "diff", "--cached", "--name-only"); out != "" {
		t.Fatalf("git add -A staged:\n%s", out)
	}
	// A change to a tracked project file under .loop still shows.
	if err := os.WriteFile(filepath.Join(dir, ".loop/config.yaml"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := git(t, dir, "status", "--porcelain"); !strings.Contains(out, ".loop/config.yaml") {
		t.Fatalf("a tracked .loop file is hidden:\n%s", out)
	}
	// A second injection adds no duplicate exclude line.
	inject(t, dir, nil)
	ex, _ := os.ReadFile(filepath.Join(dir, ".git/info/exclude"))
	if strings.Count(string(ex), "/.env\n") != 1 {
		t.Fatalf("exclude:\n%s", ex)
	}
	if _, err := os.Stat(filepath.Join(dir, EnvFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an injection with no secrets leaves an old .env: %v", err)
	}
}

// A linked worktree shares the common info/exclude.
func TestInjectionInLinkedWorktree(t *testing.T) {
	dir := gitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, dir, "worktree", "add", "-q", "-b", "run-1", wt)
	inject(t, wt, map[string]string{"A": "1"})
	if out := git(t, wt, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("git status in a worktree:\n%s", out)
	}
}

// A tracked file at an injected path refuses the run, and nothing is
// written.
func TestTrackedInjectedPathIsAConflict(t *testing.T) {
	for _, tracked := range []string{".loop/brief.json", ".claude/skills/loop-implementer/SKILL.md", ".claude/agents/loop-helper.md", ".env"} {
		dir := gitRepo(t, tracked)
		_, err := Inject(context.Background(), Injection{Checkout: dir, Harness: agenthost.Claude, Pack: testPack(), Brief: testBrief()})
		var ce *ConflictError
		if !errors.As(err, &ce) || !slices.Contains(ce.Paths, tracked) {
			t.Fatalf("tracked %s: err = %v", tracked, err)
		}
		if _, err := os.Stat(filepath.Join(dir, PromptFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tracked %s: a refused injection wrote the prompt", tracked)
		}
	}
	// A tracked skill or agent outside the pack's names is no conflict.
	dir := gitRepo(t, ".claude/skills/loop-other/SKILL.md", ".claude/skills/mine/SKILL.md", ".claude/agents/mine.md")
	if _, err := Inject(context.Background(), Injection{Checkout: dir, Harness: agenthost.Claude, Pack: testPack(), Brief: testBrief()}); err != nil {
		t.Fatal(err)
	}
}

// Codex reads .agents/skills. The pack hash names the content, not the
// harness.
func TestPackPerHarnessAndHash(t *testing.T) {
	dir := gitRepo(t)
	res, err := Inject(context.Background(), Injection{Checkout: dir, Harness: agenthost.Codex, Pack: testPack(), Brief: testBrief()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".agents/skills/loop-code-reviewer/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	// Codex reads no agent directory, so it receives no agent.
	if entries, _ := os.ReadDir(filepath.Join(dir, ".agents")); len(entries) != 1 {
		t.Fatalf("codex received %v under .agents, want only skills", entries)
	}
	h1, _ := testPack().Hash()
	other := testPack()
	other.FS.(fstest.MapFS)["loop-implementer/SKILL.md"] = &fstest.MapFile{Data: []byte("changed")}
	h2, _ := other.Hash()
	if res.PackHash != h1 || h1 == h2 {
		t.Fatalf("hashes %s %s %s", res.PackHash, h1, h2)
	}
	// An agent is pack content, so a change to one changes the hash.
	other = testPack()
	other.Agents.(fstest.MapFS)["loop-helper.md"] = &fstest.MapFile{Data: []byte("changed")}
	if h3, _ := other.Hash(); h3 == h1 {
		t.Fatalf("an agent change kept the hash %s", h1)
	}
	if out := git(t, dir, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("git status:\n%s", out)
	}
}

// RefreshPack writes a new pack into the checkout of a running session. It
// leaves the brief as it was and git clean, and a tracked skill directory
// refuses it with a *ConflictError before it writes.
func TestRefreshPackWritesTheNewPackOnly(t *testing.T) {
	ctx := context.Background()
	dir := gitRepo(t)
	inject(t, dir, nil)
	before, err := os.ReadFile(filepath.Join(dir, BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	next := Pack{Version: "v2.0.0", FS: fstest.MapFS{
		"loop-implementer/SKILL.md":   {Data: []byte("# implementer runbook v2\n")},
		"loop-coordinator/SKILL.md":   {Data: []byte("# coordinator runbook v2\n")},
		"loop-code-reviewer/SKILL.md": {Data: []byte("# reviewer runbook\n")},
	}, Agents: fstest.MapFS{
		"loop-other.md": {Data: []byte("---\nname: loop-other\n---\n")},
	}}
	if err := RefreshPack(ctx, dir, agenthost.Claude, next); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		Runbook(agenthost.Claude, "implementer"): "# implementer runbook v2\n",
		Runbook(agenthost.Claude, "coordinator"): "# coordinator runbook v2\n",
		".claude/agents/loop-other.md":           "---\nname: loop-other\n---\n",
	} {
		if got, err := os.ReadFile(filepath.Join(dir, path)); err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(dir, BriefJSON)); string(after) != string(before) {
		t.Fatalf("RefreshPack changed the brief:\n%s", after)
	}
	if out := git(t, dir, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("git status after RefreshPack:\n%s", out)
	}

	tracked := Runbook(agenthost.Claude, "coordinator")
	dir = gitRepo(t, tracked)
	err = RefreshPack(ctx, dir, agenthost.Claude, next)
	var ce *ConflictError
	if !errors.As(err, &ce) || !slices.Contains(ce.Paths, tracked) {
		t.Fatalf("tracked %s: err = %v", tracked, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, tracked)); string(got) != tracked+"\n" {
		t.Fatalf("a refused RefreshPack wrote %s: %q", tracked, got)
	}
}

func TestRemoveEnv(t *testing.T) {
	dir := gitRepo(t)
	inject(t, dir, map[string]string{"A": "1"})
	if err := RemoveEnv(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, EnvFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(".env stays")
	}
	if err := RemoveEnv(dir); err != nil {
		t.Fatalf("a second remove: %v", err)
	}
}

func readEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %v, %v", path, fi.Mode().Perm(), err)
	}
	env, err := secrets.ParseEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

const appManifest = `secrets:
  - name: ROOT_KEY
  - name: WEB_KEY
    dir: apps/web
  - name: BATCH_KEY
    dir: apps/batch
`

// Each .env holds only the names whose entries name its directory: the
// root .env holds no app's key, and apps/web/.env holds the web key only
// (parity 9.3). A directory the checkout lacks is made. git stays clean,
// and RemoveEnv deletes every file.
func TestEnvWritesEachDirWithOnlyItsNames(t *testing.T) {
	dir := gitRepo(t, "apps/web/package.json")
	m, err := config.ParseManifest([]byte(appManifest))
	if err != nil {
		t.Fatal(err)
	}
	injectFiles(t, dir, GroupEnv(m, map[string]string{"ROOT_KEY": "r1", "WEB_KEY": "w1", "BATCH_KEY": "b1"}))
	for rel, want := range map[string]map[string]string{
		".env":            {"ROOT_KEY": "r1"},
		"apps/web/.env":   {"WEB_KEY": "w1"},
		"apps/batch/.env": {"BATCH_KEY": "b1"},
	} {
		if got := readEnv(t, filepath.Join(dir, rel)); !maps.Equal(got, want) {
			t.Fatalf("%s = %v, want %v", rel, got, want)
		}
	}
	if out := git(t, dir, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("git status after injection:\n%s", out)
	}
	if err := RemoveEnv(dir); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".env", "apps/web/.env", "apps/batch/.env", EnvRecord} {
		if _, err := os.Lstat(filepath.Join(dir, rel)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s stays after RemoveEnv: %v", rel, err)
		}
	}
}

// A later injection with fewer files removes the .env the last one wrote,
// so a name the manifest moved or dropped leaves no copy behind.
func TestEnvRewriteRemovesTheOldFiles(t *testing.T) {
	dir := gitRepo(t)
	injectFiles(t, dir, EnvFiles{"": {"A": "1"}, "apps/web": {"B": "2"}})
	injectFiles(t, dir, EnvFiles{"": {"A": "1"}})
	if _, err := os.Stat(filepath.Join(dir, "apps/web/.env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old apps/web/.env stays: %v", err)
	}
	if got := readEnv(t, filepath.Join(dir, EnvFile)); !maps.Equal(got, map[string]string{"A": "1"}) {
		t.Fatalf(".env = %v", got)
	}
}

// A directory that is a symlink out of the checkout gets no .env, and a
// record that names a path outside the checkout deletes nothing there.
func TestEnvStaysInsideTheCheckout(t *testing.T) {
	dir := gitRepo(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "apps/web")); err != nil {
		t.Fatal(err)
	}
	if err := WriteEnv(context.Background(), dir, EnvFiles{"apps/web": {"B": "2"}}); err == nil {
		t.Fatal("WriteEnv wrote through a symlink out of the checkout")
	}
	if _, err := os.Stat(filepath.Join(outside, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a .env landed outside the checkout: %v", err)
	}
	victim := filepath.Join(outside, ".env")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(dir, victim)
	if err := os.WriteFile(filepath.Join(dir, EnvRecord), []byte(rel+"\napps/web/.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = RemoveEnv(dir)
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("RemoveEnv deleted a file outside the checkout: %v", err)
	}
}

// A tracked file at an app's .env refuses the run, as the root .env does.
func TestTrackedAppEnvIsAConflict(t *testing.T) {
	dir := gitRepo(t, "apps/web/.env")
	_, err := Inject(context.Background(), Injection{Checkout: dir, Harness: agenthost.Claude, Pack: testPack(), Brief: testBrief(),
		Env: EnvFiles{"apps/web": {"B": "2"}}})
	var ce *ConflictError
	if !errors.As(err, &ce) || !slices.Contains(ce.Paths, "apps/web/.env") {
		t.Fatalf("err = %v", err)
	}
}

// The brief names each kept checkout with its branch and dirty files, and
// counts the files it leaves out.
func TestMarkdownNamesKeptCheckouts(t *testing.T) {
	b := Brief{RunID: "r-2", KeptCheckouts: []KeptCheckout{
		{RunID: "r-1", Path: "/w/r-1", Branch: "loop-x-r-1", Dirty: []string{"a.go", "b/c.go"}, More: 3},
	}}
	md := b.Markdown()
	for _, s := range []string{
		"## Uncommitted work of earlier attempts",
		"- Run r-1: `/w/r-1` (branch loop-x-r-1)",
		"  - `a.go`\n  - `b/c.go`\n",
		"  - and 3 more: run `git -C /w/r-1 status` to list them",
	} {
		if !strings.Contains(md, s) {
			t.Fatalf("brief.md lacks %q:\n%s", s, md)
		}
	}
	if md := (Brief{RunID: "r-2"}).Markdown(); strings.Contains(md, "Uncommitted work") {
		t.Fatalf("a brief with no kept checkout names one:\n%s", md)
	}
}
