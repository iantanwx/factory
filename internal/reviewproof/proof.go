// Package reviewproof runs `loop review proof`: the code-reviewer's proof
// that the tests of a pull request fail at its merge base.
//
// The proof was five steps of prose in the code-reviewer runbook: make a
// worktree at the merge base, copy the tests in, run them, read the
// result, and remove the worktree. Nothing ran the last step, and nothing
// checked that the reviewed checkout was still clean at its head when the
// verdict went in. Tickertown lost a checkout that way on 2026-10-12
// (review-base-proof.ts, alpha): a child left HEAD detached at the merge
// base with three test files staged.
//
// So the proof is one command, and the removal sits in a defer:
//
//  1. It refuses a dirty tree before it makes anything. A dirty tree is
//     not the head under review.
//  2. It makes a detached worktree at the merge base, in a new temporary
//     directory. The reviewed checkout never moves.
//  3. It brings each --test and --bring path from HEAD, and nothing else.
//  4. It runs the setup of the base's .loop/config.yaml, then the tests:
//     --run, or the base config's test gate. The base's config, because
//     the pull request under review can change its own.
//  5. Whatever happened, it removes the worktree, then checks that the
//     reviewed checkout is on its branch, at its head, with a clean tree.
//     SIGINT, SIGTERM and SIGHUP stop the tests and reach the removal too.
//
// A red answer says only that the tests failed. A missing import fails
// for the wrong reason, and the runbook states what to do then.
package reviewproof

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
)

// Proof is the answer of a red proof.
type Proof struct {
	Result string `json:"result"` // always "red"; any other outcome is a refusal
	// Base is the merge base the tests ran at, and Head the commit they
	// came from.
	Base   string   `json:"base"`
	Head   string   `json:"head"`
	Branch string   `json:"branch"` // the branch the reviewed checkout holds, or "" when detached
	Run    string   `json:"run"`
	Tests  []string `json:"tests"`
	// TestExit is the exit code of the test command.
	TestExit int `json:"test_exit"`
	// Tail is the end of the test output. Read it to learn why the tests
	// failed. The whole output goes to stderr.
	Tail string `json:"tail"`
}

// tailLines bounds Proof.Tail.
const tailLines = 40

// Verb is the handler of `review proof`. The setup and test output goes to
// out as it runs.
func Verb(out io.Writer) cli.Handler {
	return func(ctx context.Context, inv cli.Invocation) cli.Result {
		cwd, err := os.Getwd()
		if err != nil {
			return cli.Internal(err)
		}
		return prove(ctx, cwd, inv.Flags, out)
	}
}

func prove(ctx context.Context, cwd string, flags map[string]string, out io.Writer) (res cli.Result) {
	tests, err := paths(flags["test"])
	if err != nil {
		return cli.Reject(cli.CodeUsage, "bad_path", "--test: "+err.Error())
	}
	bring, err := paths(flags["bring"])
	if err != nil {
		return cli.Reject(cli.CodeUsage, "bad_path", "--bring: "+err.Error())
	}
	top, err := gitx.Run(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return cli.Reject(cli.CodeUsage, "not_a_checkout", err.Error())
	}
	root := strings.TrimSpace(string(top))
	head, err := revParse(ctx, root, "HEAD")
	if err != nil {
		return cli.Internal(err)
	}
	branch := branchOf(ctx, root)
	dirty, err := dirtyOf(ctx, root)
	if err != nil {
		return cli.Internal(err)
	}
	if len(dirty) > 0 {
		return cli.Reject(cli.CodeCheckoutNotClean, "dirty", fmt.Sprintf(
			"the reviewed checkout holds changes that are not at its head (%s); keep it at the head under review with a clean tree", strings.Join(dirty, ", ")))
	}
	ref := flags["base"]
	if ref == "" {
		if ref, err = briefBase(root); err != nil {
			return cli.Reject(cli.CodeUsage, "no_base", err.Error()+"; name the base ref with --base")
		}
	}
	mb, err := gitx.Run(ctx, root, "merge-base", "HEAD", ref)
	if err != nil {
		return cli.Reject(cli.CodeUsage, "no_merge_base", err.Error())
	}
	base := strings.TrimSpace(string(mb))

	// The signals stay caught until the removal below ends, so no second
	// signal skips it.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	tmp, err := os.MkdirTemp("", "loop-review-proof-")
	if err != nil {
		return cli.Internal(err)
	}
	wt := filepath.Join(tmp, "base")
	defer func() {
		after := context.WithoutCancel(ctx)
		diffs := remove(after, root, tmp, wt)
		diffs = append(diffs, differences(after, root, branch, head)...)
		if len(diffs) > 0 {
			res = cli.Reject(cli.CodeCheckoutNotClean, "not_back", fmt.Sprintf(
				"the proof decides nothing until the reviewed checkout is back: %s", strings.Join(diffs, "; ")))
		}
	}()
	interrupted := func() cli.Result {
		return cli.Reject(cli.CodeProofUndecided, "interrupted", fmt.Sprintf("%v stopped the proof, so it decides nothing", context.Cause(sigCtx)))
	}

	if _, err := gitx.Run(sigCtx, root, "worktree", "add", "--quiet", "--detach", wt, base); err != nil {
		if sigCtx.Err() != nil {
			return interrupted()
		}
		return cli.Internal(err)
	}
	if _, err := gitx.Run(sigCtx, wt, append([]string{"checkout", head, "--"}, append(slices.Clone(tests), bring...)...)...); err != nil {
		if sigCtx.Err() != nil {
			return interrupted()
		}
		return cli.Reject(cli.CodeUsage, "not_at_head", err.Error())
	}
	cfg, err := baseConfig(wt)
	if err != nil {
		return cli.Reject(cli.CodeUsage, "bad_config", "the base's "+config.ProjectConfigPath+": "+err.Error())
	}
	command := flags["run"]
	if command == "" {
		command = testGate(cfg)
	}
	if command == "" {
		return cli.Reject(cli.CodeUsage, "no_test_command", "the base's "+config.ProjectConfigPath+" names no test gate; name the command with --run")
	}
	if cfg != nil && strings.TrimSpace(cfg.Setup) != "" {
		code, err := shell(sigCtx, wt, cfg.Setup, nil, out)
		switch {
		case sigCtx.Err() != nil:
			return interrupted()
		case err != nil:
			return cli.Internal(err)
		case code != 0:
			return cli.Reject(cli.CodeProofUndecided, "setup_failed", fmt.Sprintf(
				"the setup %q exits %d at %s, and a red run after it is red for the wrong reason; stderr holds its output", cfg.Setup, code, base[:12]))
		}
	}
	var tail tailBuffer
	code, err := shell(sigCtx, wt, command, tests, io.MultiWriter(out, &tail))
	switch {
	case sigCtx.Err() != nil:
		return interrupted()
	case err != nil:
		return cli.Internal(err)
	case code == 0:
		return cli.Reject(cli.CodeGreenAtBase, "green_at_base", fmt.Sprintf(
			"%q passes at the merge base %s, so the tests do not depend on the change", command, base[:12]))
	}
	return cli.Found(Proof{Result: "red", Base: base, Head: head, Branch: branch, Run: command, Tests: tests, TestExit: code, Tail: tail.String()})
}

// paths splits a comma-separated flag into clean paths inside the
// checkout.
func paths(flag string) ([]string, error) {
	if flag == "" {
		return nil, nil
	}
	var out []string
	for p := range strings.SplitSeq(flag, ",") {
		p = strings.TrimSpace(p)
		clean := filepath.ToSlash(filepath.Clean(p))
		switch {
		case p == "":
			return nil, errors.New("an empty path")
		case filepath.IsAbs(p), clean == "..", strings.HasPrefix(clean, "../"):
			return nil, fmt.Errorf("%q is not a path inside the checkout", p)
		}
		out = append(out, clean)
	}
	return out, nil
}

func revParse(ctx context.Context, dir, rev string) (string, error) {
	out, err := gitx.Run(ctx, dir, "rev-parse", "--verify", rev)
	return strings.TrimSpace(string(out)), err
}

// branchOf names the branch HEAD holds, or "" for a detached HEAD.
func branchOf(ctx context.Context, dir string) string {
	out, err := gitx.Run(ctx, dir, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// dirtyOf lists `git status --porcelain`. An untracked file that git does
// not ignore counts.
func dirtyOf(ctx context.Context, dir string) ([]string, error) {
	out, err := gitx.Run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var lines []string
	for l := range strings.SplitSeq(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// briefBase is the remote ref of the base branch the run's brief names.
func briefBase(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, brief.BriefJSON))
	if err != nil {
		return "", fmt.Errorf("read the brief: %w", err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		return "", fmt.Errorf("read the brief: %w", err)
	}
	if b.BaseBranch == "" {
		return "", errors.New("the brief names no base branch")
	}
	return config.DefaultRef(b.BaseBranch), nil
}

// baseConfig reads the project config at the merge base. A base with none
// gives nil.
func baseConfig(wt string) (*config.ProjectConfig, error) {
	raw, err := os.ReadFile(filepath.Join(wt, config.ProjectConfigPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return config.ParseProjectConfig(raw)
}

func testGate(cfg *config.ProjectConfig) string {
	if cfg == nil {
		return ""
	}
	i := slices.IndexFunc(cfg.Gates, func(c config.Command) bool { return c.Name == "test" })
	if i < 0 {
		return ""
	}
	return cfg.Gates[i].Run
}

// shell runs command with sh in dir, with args in "$@". It returns the
// exit code. A cancelled ctx kills the whole process group, so no child
// of the tests outlives the proof.
func shell(ctx context.Context, dir, command string, args []string, out io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, "sh", append([]string{"-c", command, "sh"}, args...)...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// remove deletes the worktree and its temporary directory, and returns
// each step that failed.
func remove(ctx context.Context, root, tmp, wt string) []string {
	var diffs []string
	if _, err := os.Stat(wt); err == nil {
		if _, err := gitx.Run(ctx, root, "worktree", "remove", "--force", wt); err != nil {
			diffs = append(diffs, err.Error())
		}
	}
	if err := os.RemoveAll(tmp); err != nil {
		diffs = append(diffs, err.Error())
	}
	if _, err := gitx.Run(ctx, root, "worktree", "prune"); err != nil {
		diffs = append(diffs, err.Error())
	}
	if list, err := gitx.Run(ctx, root, "worktree", "list", "--porcelain"); err != nil {
		diffs = append(diffs, err.Error())
	} else if strings.Contains(string(list), "worktree "+wt+"\n") {
		diffs = append(diffs, "the worktree "+wt+" is still registered; run git worktree remove --force "+wt)
	}
	return diffs
}

// differences names each way the reviewed checkout left its branch, its
// head or a clean tree.
func differences(ctx context.Context, root, branch, head string) []string {
	var diffs []string
	if now := branchOf(ctx, root); now != branch {
		diffs = append(diffs, fmt.Sprintf("HEAD names %q, not %q", now, branch))
	}
	if now, err := revParse(ctx, root, "HEAD"); err != nil {
		diffs = append(diffs, err.Error())
	} else if now != head {
		diffs = append(diffs, fmt.Sprintf("HEAD is at %s, not at %s", now[:12], head[:12]))
	}
	if dirty, err := dirtyOf(ctx, root); err != nil {
		diffs = append(diffs, err.Error())
	} else if len(dirty) > 0 {
		diffs = append(diffs, "the tree is dirty: "+strings.Join(dirty, ", "))
	}
	return diffs
}

// tailBuffer keeps the last 64 KiB written to it.
type tailBuffer struct{ b bytes.Buffer }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b.Write(p)
	if over := t.b.Len() - 64<<10; over > 0 {
		t.b.Next(over)
	}
	return len(p), nil
}

// String is the last tailLines lines.
func (t *tailBuffer) String() string {
	lines := strings.Split(strings.TrimRight(t.b.String(), "\n"), "\n")
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	return strings.Join(lines, "\n")
}
