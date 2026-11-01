package sched

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx/gittest"
	"github.com/iantanwx/loop/internal/machine"
)

// C-bug-1: while a run is live, its checkout refuses a plain
// `git worktree remove --force` from another checkout of the same
// repository. Run r-c75a391983e4 removed the live checkout of run
// r-e19012298c4d that way, and the run lost its brief and token (F-67).
func TestALiveRunsCheckoutRefusesAWorktreeRemoveFromAnotherRun(t *testing.T) {
	w := newWorld(t)
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)

	cmd := gittest.Command("-C", w.repo, "worktree", "remove", "--force", st.Checkout)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("git worktree remove --force removed the checkout of live run %s, want a refusal\n%s", st.RunID, out)
	}
	if _, err := os.Stat(filepath.Join(st.Checkout, brief.BriefJSON)); err != nil {
		t.Fatalf("the live run's brief is gone after the refused remove: %v", err)
	}
}

// C-bug-1: a clean stray that a run's lock still holds goes. No row names
// it, so no live run holds it, and the stray pass lifts the lock first.
func TestCleanupRemovesALockedCleanStray(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.AgentHost.CheckoutRoot = w.root })
	stray := filepath.Join(w.root, "alpha", "loop-alpha-code-reviewer-r-0f1e2d3c4b5a")
	gitIn(t, w.repo, "worktree", "add", "-q", "-B", "stray", stray, "origin/main")
	gitIn(t, w.repo, "worktree", "lock", "--reason", "loop run r-0f1e2d3c4b5a is live", stray)
	rep := w.s.CleanCheckouts(context.Background())
	if !slices.Equal(rep.Deleted, []string{stray}) || len(rep.Errors) != 0 || exists(stray) {
		t.Fatalf("report = %+v, on disk %v", rep, exists(stray))
	}
}
