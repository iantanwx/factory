package reviewproof

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/gitx/gittest"
)

// check.sh is the test the pull request adds. It passes only when the
// change is in: code.txt reads new, and the setup step ran first.
const checkScript = `test -f ready || { echo "the setup did not run"; exit 7; }
grep -q new code.txt || { echo "want new, got $(cat code.txt)"; exit 1; }
`

// world is a reviewed checkout: main holds the merge base, and the
// feature branch holds the change and its test.
type world struct {
	dir, base, head string
	tmp             string // TMPDIR, where the proof makes its worktree
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gittest.Command(append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func projectConfig(setup, test string) string {
	return "setup: " + setup + "\nmerge:\n  mechanism: direct\ngates:\n  - name: test\n    run: " + test + "\n"
}

// newWorld builds the repository. baseConfig and headConfig are the
// .loop/config.yaml of each commit; an empty one writes none.
func newWorld(t *testing.T, baseConfig, headConfig string) world {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "code.txt", "old\n")
	if baseConfig != "" {
		write(t, dir, ".loop/config.yaml", baseConfig)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	w := world{dir: dir, base: gitIn(t, dir, "rev-parse", "HEAD")}
	gitIn(t, dir, "switch", "-q", "-c", "feature")
	write(t, dir, "code.txt", "new\n")
	write(t, dir, "check.sh", checkScript)
	if headConfig != "" {
		write(t, dir, ".loop/config.yaml", headConfig)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "change")
	w.head = gitIn(t, dir, "rev-parse", "HEAD")
	// main moves on after the branch, so the merge base is not main's head.
	gitIn(t, dir, "switch", "-q", "main")
	write(t, dir, "later.txt", "later\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "later")
	gitIn(t, dir, "switch", "-q", "feature")
	w.tmp = t.TempDir()
	t.Setenv("TMPDIR", w.tmp)
	t.Chdir(dir)
	return w
}

func proveIt(t *testing.T, ctx context.Context, flags map[string]string) (cli.Result, string) {
	t.Helper()
	v, ok := cli.Surface().Verb("review proof")
	if !ok {
		t.Fatal("the surface has no review proof verb")
	}
	var out strings.Builder
	res := Verb(&out)(ctx, cli.Invocation{Verb: v, Args: []string{}, Flags: flags})
	return res, out.String()
}

// assertBack checks the reviewed checkout is where the review left it,
// and that no worktree of the proof is left.
func assertBack(t *testing.T, w world) {
	t.Helper()
	if got := gitIn(t, w.dir, "symbolic-ref", "--short", "HEAD"); got != "feature" {
		t.Errorf("HEAD names %q, want feature", got)
	}
	if got := gitIn(t, w.dir, "rev-parse", "HEAD"); got != w.head {
		t.Errorf("HEAD is %s, want %s", got, w.head)
	}
	if got := gitIn(t, w.dir, "status", "--porcelain"); got != "" {
		t.Errorf("the reviewed checkout is dirty: %q", got)
	}
	if got := gitIn(t, w.dir, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Errorf("a worktree of the proof is left: %s", got)
	}
	if left, err := os.ReadDir(w.tmp); err != nil || len(left) != 0 {
		t.Errorf("the proof left %v in its temporary directory (%v)", left, err)
	}
}

func refusal(t *testing.T, res cli.Result, code, rule string) string {
	t.Helper()
	if res.Refusal == nil || res.Refusal.Code != code || res.Refusal.Rule != rule {
		t.Fatalf("want %s %s, got %+v %+v", code, rule, res.Refusal, res.Data)
	}
	return res.Refusal.Hint
}

// The contract of the slice: one command runs the tests at the merge base
// in a throwaway worktree, and the reviewed checkout never moves.
func TestProofRunsTheTestsAtTheMergeBaseInAThrowawayWorktree(t *testing.T) {
	w := newWorld(t, projectConfig("touch ready", "sh check.sh"), "")
	res, out := proveIt(t, context.Background(), map[string]string{"test": "check.sh", "base": "main"})
	if res.Refusal != nil {
		t.Fatalf("refused: %+v", res.Refusal)
	}
	p := res.Data.(Proof)
	if p.Result != "red" || p.TestExit != 1 || p.Base != w.base || p.Head != w.head || p.Branch != "feature" {
		t.Fatalf("proof = %+v, want red with exit 1 at base %s and head %s", p, w.base, w.head)
	}
	if p.Run != "sh check.sh" || strings.Join(p.Tests, ",") != "check.sh" {
		t.Fatalf("proof ran %q on %v", p.Run, p.Tests)
	}
	if !strings.Contains(p.Tail, "want new, got old") || !strings.Contains(out, "want new, got old") {
		t.Fatalf("the test output is lost: tail %q, out %q", p.Tail, out)
	}
	assertBack(t, w)
}

// A test that passes at the merge base does not depend on the change.
func TestProofRefusesATestThatPassesAtTheMergeBase(t *testing.T) {
	w := newWorld(t, projectConfig("touch ready", "true"), "")
	res, _ := proveIt(t, context.Background(), map[string]string{"test": "check.sh", "base": "main", "run": "test -f check.sh"})
	hint := refusal(t, res, cli.CodeGreenAtBase, "green_at_base")
	if !strings.Contains(hint, w.base[:12]) {
		t.Fatalf("the hint names no merge base: %q", hint)
	}
	assertBack(t, w)
}

// The base config names the setup and the test command. The head's config
// cannot turn the proof: the pull request under review wrote it.
func TestProofRunsTheBaseConfigsSetupAndTestCommand(t *testing.T) {
	w := newWorld(t, projectConfig("touch ready", "sh check.sh"), projectConfig("true", "true"))
	res, _ := proveIt(t, context.Background(), map[string]string{"test": "check.sh", "base": "main"})
	if res.Refusal != nil {
		t.Fatalf("refused: %+v", res.Refusal)
	}
	if p := res.Data.(Proof); p.Result != "red" || p.TestExit != 1 || p.Run != "sh check.sh" {
		t.Fatalf("proof = %+v, want the base's sh check.sh, red with exit 1", p)
	}
	assertBack(t, w)
}

// A setup that fails decides nothing: the red it would give is red for the
// wrong reason.
func TestProofCallsAFailedSetupUndecided(t *testing.T) {
	w := newWorld(t, projectConfig("exit 3", "true"), "")
	res, _ := proveIt(t, context.Background(), map[string]string{"test": "check.sh", "base": "main", "run": "sh check.sh"})
	refusal(t, res, cli.CodeProofUndecided, "setup_failed")
	assertBack(t, w)
}

// A dirty tree is not the head under review, so the proof refuses before
// it makes a worktree.
func TestProofRefusesADirtyCheckoutBeforeItMakesAWorktree(t *testing.T) {
	w := newWorld(t, projectConfig("touch ready", "sh check.sh"), "")
	write(t, w.dir, "check.sh", "exit 1\n")
	res, _ := proveIt(t, context.Background(), map[string]string{"test": "check.sh", "base": "main"})
	hint := refusal(t, res, cli.CodeCheckoutNotClean, "dirty")
	if !strings.Contains(hint, "check.sh") {
		t.Fatalf("the hint names no dirty path: %q", hint)
	}
	if got := gitIn(t, w.dir, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Fatalf("the proof made a worktree: %s", got)
	}
}

// A test run that writes into the reviewed checkout leaves it unclean, and
// the proof says so instead of reporting red.
func TestProofReportsAReviewedCheckoutTheRunChanged(t *testing.T) {
	w := newWorld(t, projectConfig("touch ready", "true"), "")
	res, _ := proveIt(t, context.Background(), map[string]string{"test": "check.sh", "base": "main",
		"run": "touch " + filepath.Join(w.dir, "stray.txt") + "; exit 1"})
	hint := refusal(t, res, cli.CodeCheckoutNotClean, "not_back")
	if !strings.Contains(hint, "stray.txt") {
		t.Fatalf("the hint names no difference: %q", hint)
	}
}

// SIGTERM stops the test run, and the worktree still goes.
func TestProofRemovesTheWorktreeWhenASignalStopsTheTests(t *testing.T) {
	w := newWorld(t, projectConfig("touch ready", "true"), "")
	marker := filepath.Join(t.TempDir(), "started")
	go func() {
		for range 200 {
			if _, err := os.Stat(marker); err == nil {
				_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	start := time.Now()
	res, _ := proveIt(t, context.Background(), map[string]string{"test": "check.sh", "base": "main",
		"run": "touch " + marker + "; sleep 30"})
	refusal(t, res, cli.CodeProofUndecided, "interrupted")
	if time.Since(start) > 20*time.Second {
		t.Fatal("the signal did not stop the test run")
	}
	assertBack(t, w)
}

// With no --base, the proof reads the base branch from the run's brief,
// and it takes the remote ref of that branch. Each other ref here gives
// another merge base: main and a stale local trunk give the first
// commit, and origin/main does not exist.
func TestProofReadsTheBaseBranchFromTheBrief(t *testing.T) {
	w := newWorld(t, projectConfig("touch ready", "sh check.sh"), "")
	gitIn(t, w.dir, "switch", "-q", "-c", "topic", w.base)
	write(t, w.dir, "mid.txt", "mid\n")
	gitIn(t, w.dir, "add", "-A")
	gitIn(t, w.dir, "commit", "-q", "-m", "mid")
	mid := gitIn(t, w.dir, "rev-parse", "HEAD")
	gitIn(t, w.dir, "switch", "-q", "--detach", mid)
	write(t, w.dir, "trunk.txt", "trunk\n")
	gitIn(t, w.dir, "add", "-A")
	gitIn(t, w.dir, "commit", "-q", "-m", "trunk")
	gitIn(t, w.dir, "update-ref", "refs/remotes/origin/trunk", "HEAD")
	gitIn(t, w.dir, "update-ref", "refs/heads/trunk", w.base)
	gitIn(t, w.dir, "switch", "-q", "topic")
	gitIn(t, w.dir, "cherry-pick", w.head)
	write(t, w.dir, ".loop/brief.json", `{"base_branch":"trunk"}`)
	write(t, w.dir, ".git/info/exclude", ".loop/brief.json\n")
	res, _ := proveIt(t, context.Background(), map[string]string{"test": "check.sh"})
	if res.Refusal != nil {
		t.Fatalf("refused: %+v", res.Refusal)
	}
	if p := res.Data.(Proof); p.Base != mid || p.Result != "red" {
		t.Fatalf("proof = %+v, want red at %s, the merge base with origin/trunk", p, mid)
	}
}

// A test path must name a file inside the checkout.
func TestProofRefusesATestPathOutsideTheCheckout(t *testing.T) {
	newWorld(t, projectConfig("touch ready", "sh check.sh"), "")
	for _, bad := range []string{"../check.sh", "/etc/passwd", "check.sh,,"} {
		res, _ := proveIt(t, context.Background(), map[string]string{"test": bad, "base": "main"})
		refusal(t, res, cli.CodeUsage, "bad_path")
	}
}
