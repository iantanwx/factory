package sched

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// A settled run's checkout whose own settlement turned its row deleted is
// still on disk while the removal runs, half gone. The next pass is no
// stray step's to judge: a row names the directory, so the pass keeps no
// entry for it and rings nobody (F-42).
func TestCleanupRingsNobodyForACheckoutItsRowJustSettledAsDeleted(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.AgentHost.CheckoutRoot = w.root })
	now := w.clock.Now().UnixMilli()
	dir := filepath.Join(w.root, "alpha", "loop-alpha-reproducer-tk-9-r-fd03b2258e03")
	gitIn(t, w.repo, "worktree", "add", "-q", "-B", "loop/tk-9", dir, "origin/main")
	w.exec(`INSERT INTO checkouts (id, project, item, path, branch, run_id, state, created_at, updated_at)
		VALUES ('r-fd03b2258e03', ?, 'tk-9', ?, 'loop/tk-9', 'r-fd03b2258e03', ?, ?, ?)`, prj, dir, CheckoutDeleted, now, now)
	// The removal is under way: some tracked files are gone already.
	files := strings.Fields(gitIn(t, dir, "ls-files"))
	if len(files) == 0 {
		t.Fatal("the checkout tracks no file")
	}
	if err := os.Remove(filepath.Join(dir, files[0])); err != nil {
		t.Fatal(err)
	}
	rep := w.s.CleanCheckouts(context.Background())
	if why, ok := rep.Kept[dir]; ok {
		t.Fatalf("the pass keeps %s as a stray: %s", dir, why)
	}
	if n := w.alerts(AlertCheckoutKept); n != 0 {
		t.Fatalf("checkout_kept alerts raised = %d, want 0", n)
	}
}

// A checkout whose row the pass before turned deleted can still stand, half
// gone, at this pass. A row still names it, so the pass keeps no entry for
// it and rings nobody (F-42).
func TestCleanupRingsNobodyForACheckoutItsRowSettledAsDeletedOnThePassBefore(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.AgentHost.CheckoutRoot = w.root })
	gone := w.clock.Now().Add(-6 * time.Minute).UnixMilli() // one pass of 5 minutes and a bit
	dir := filepath.Join(w.root, "alpha", "loop-alpha-reproducer-tk-9-r-fd03b2258e03")
	gitIn(t, w.repo, "worktree", "add", "-q", "-B", "loop/tk-9", dir, "origin/main")
	w.exec(`INSERT INTO checkouts (id, project, item, path, branch, run_id, state, created_at, updated_at)
		VALUES ('r-fd03b2258e03', ?, 'tk-9', ?, 'loop/tk-9', 'r-fd03b2258e03', ?, ?, ?)`, prj, dir, CheckoutDeleted, gone, gone)
	files := strings.Fields(gitIn(t, dir, "ls-files"))
	if len(files) == 0 {
		t.Fatal("the checkout tracks no file")
	}
	if err := os.Remove(filepath.Join(dir, files[0])); err != nil {
		t.Fatal(err)
	}
	rep := w.s.CleanCheckouts(context.Background())
	if why, ok := rep.Kept[dir]; ok {
		t.Fatalf("the pass keeps %s as a stray: %s", dir, why)
	}
	if n := w.alerts(AlertCheckoutKept); n != 0 {
		t.Fatalf("checkout_kept alerts raised = %d, want 0", n)
	}
}

// A checkout that still stands one full pass after its row turned deleted
// truly stays. The stray step judges it again by the content rules, and a
// person hears of it (F-42).
func TestCleanupJudgesACheckoutThatStandsAFullPassAfterItsRowTurnedDeleted(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.AgentHost.CheckoutRoot = w.root })
	gone := w.clock.Now().Add(-11 * time.Minute).UnixMilli() // two passes of 5 minutes and more
	dir := filepath.Join(w.root, "alpha", "loop-alpha-reproducer-tk-9-r-fd03b2258e03")
	gitIn(t, w.repo, "worktree", "add", "-q", "-B", "loop/tk-9", dir, "origin/main")
	w.exec(`INSERT INTO checkouts (id, project, item, path, branch, run_id, state, created_at, updated_at)
		VALUES ('r-fd03b2258e03', ?, 'tk-9', ?, 'loop/tk-9', 'r-fd03b2258e03', ?, ?, ?)`, prj, dir, CheckoutDeleted, gone, gone)
	files := strings.Fields(gitIn(t, dir, "ls-files"))
	if len(files) == 0 {
		t.Fatal("the checkout tracks no file")
	}
	if err := os.Remove(filepath.Join(dir, files[0])); err != nil {
		t.Fatal(err)
	}
	rep := w.s.CleanCheckouts(context.Background())
	if why := rep.Kept[dir]; why != "it holds uncommitted work" {
		t.Fatalf("kept reason for %s = %q, want %q (report %+v)", dir, why, "it holds uncommitted work", rep)
	}
	if n := w.alerts(AlertCheckoutKept); n != 1 {
		t.Fatalf("checkout_kept alerts raised = %d, want 1", n)
	}
}
