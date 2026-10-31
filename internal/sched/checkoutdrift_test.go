package sched

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// A person removes a kept checkout by hand and answers its alert, as the
// alert asks. The next pass settles the row as deleted, reports no error,
// and rings nobody again (F-34).
func TestCleanupSettlesARowWhosePathAPersonRemoved(t *testing.T) {
	w := newWorld(t)
	st := w.settledCheckout()
	commitIn(t, st.Checkout, "work.txt", "work\n", "work")
	w.tick()
	if rep := w.s.CleanCheckouts(context.Background()); rep.Kept[st.Checkout] == "" {
		t.Fatalf("the unpushed checkout was not kept: %+v", rep)
	}
	if err := os.RemoveAll(st.Checkout); err != nil {
		t.Fatal(err)
	}
	gitIn(t, w.repo, "worktree", "prune")
	w.exec(`UPDATE alerts SET state = 'closed', closed_at = ? WHERE kind = ?`, w.clock.Now().UnixMilli(), AlertCheckoutKept)
	rep := w.s.CleanCheckouts(context.Background())
	if why, ok := rep.Errors[st.Checkout]; ok {
		t.Fatalf("a removed checkout is an error: %s", why)
	}
	if s := w.str(`SELECT state FROM checkouts WHERE run_id = ?`, st.RunID); s != CheckoutDeleted {
		t.Fatalf("checkout state = %s, want %s", s, CheckoutDeleted)
	}
	if n := w.count(`SELECT count(*) FROM alerts WHERE kind = ? AND state = 'open'`, AlertCheckoutKept); n != 0 {
		t.Fatalf("open checkout_kept alerts = %d", n)
	}
}

// A clean loop directory under the checkout root that no row names is the
// cleanup's to report or remove (F-34).
func TestCleanupFindsALoopDirectoryNoRowNames(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.AgentHost.CheckoutRoot = w.root })
	orphan := filepath.Join(w.root, "alpha", "loop-alpha-code-reviewer-r-05da78e3bf9c")
	gitIn(t, w.repo, "worktree", "add", "-q", "-B", "loop-alpha-code-reviewer-r-05da78e3bf9c", orphan, "origin/main")
	if n := w.count(`SELECT count(*) FROM checkouts WHERE path = ?`, orphan); n != 0 {
		t.Fatalf("rows for the directory = %d", n)
	}
	rep := w.s.CleanCheckouts(context.Background())
	if !slices.Contains(rep.Deleted, orphan) && rep.Kept[orphan] == "" {
		t.Fatalf("the cleanup pass neither removed nor reported %s: %+v", orphan, rep)
	}
}

// Each loop directory that no row names goes through the content rules.
// One that holds work stays and rings a person once. One of a repository
// that no project registers belongs to another server, so it stays and
// rings nobody (F-50). The coordinator's checkout, the operator's own
// worktree and a clone are never touched (F-34).
func TestCleanupJudgesEachLoopDirectoryNoRowNames(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.AgentHost.CheckoutRoot = w.root })
	add := func(repo, branch, dir string) string {
		gitIn(t, repo, "worktree", "add", "-q", "-B", branch, dir, "origin/main")
		return dir
	}
	dirty := add(w.repo, "dirty", filepath.Join(w.root, "alpha", "loop-alpha-implementer-r-0a1b2c3d4e5f"))
	if err := os.WriteFile(filepath.Join(dirty, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	coordinator := add(w.repo, "coordinator", filepath.Join(w.root, "alpha", "loop-coordinator"))
	own := add(w.repo, "feature-x", filepath.Join(w.root, "alpha", "feature-x"))
	clone := filepath.Join(w.root, "gamma", "loop-gamma")
	gitIn(t, w.dir, "clone", "-q", w.origin, clone)
	_, beta := makeProjectRepo(t, t.TempDir())
	foreign := add(beta, "foreign", filepath.Join(w.root, "beta", "loop-beta-implementer-r-6a7b8c9d0e1f"))
	for range 2 {
		rep := w.s.CleanCheckouts(context.Background())
		if rep.Kept[dirty] != "it holds uncommitted work" || len(rep.Kept) != 1 || len(rep.Deleted) != 0 || len(rep.Errors) != 0 {
			t.Fatalf("report = %+v", rep)
		}
	}
	if !exists(dirty) || !exists(coordinator) || !exists(own) || !exists(clone) || !exists(foreign) {
		t.Fatalf("after the passes: dirty %v, coordinator %v, own %v, clone %v, foreign %v",
			exists(dirty), exists(coordinator), exists(own), exists(clone), exists(foreign))
	}
	if n := w.alerts(AlertCheckoutKept); n != 1 {
		t.Fatalf("checkout_kept alerts raised = %d, want 1", n)
	}
}

// A clean loop directory of a registered project that no row names goes
// through that project's host, and it is gone from disk (F-34). The
// host does not know the directory, so the pass removes it with git.
func TestCleanupRemovesACleanStrayThroughItsProject(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.AgentHost.CheckoutRoot = w.root })
	stray := filepath.Join(w.root, "alpha", "loop-alpha-code-reviewer-r-0f1e2d3c4b5a")
	gitIn(t, w.repo, "worktree", "add", "-q", "-B", "stray", stray, "origin/main")
	rep := w.s.CleanCheckouts(context.Background())
	if !slices.Equal(rep.Deleted, []string{stray}) || len(rep.Errors) != 0 || exists(stray) {
		t.Fatalf("report = %+v, on disk %v", rep, exists(stray))
	}
	if !slices.Equal(w.co.removedPaths(), []string{stray}) {
		t.Fatalf("host removals = %v, want the stray through project %s", w.co.removedPaths(), prj)
	}
}

// A loop directory that a row names, or that a room which is not closed
// will name, is no stray: a run's or a room's new checkout is clean and
// has no session yet (F-34). The room's name is the one its record keeps
// (factorio-pwpi).
func TestCleanupLeavesALoopDirectoryARowOrAnOpenRoomNames(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.AgentHost.CheckoutRoot = w.root })
	now := w.clock.Now().UnixMilli()
	run := filepath.Join(w.root, "alpha", "loop-alpha-implementer-r-9a8b7c6d5e4f")
	gitIn(t, w.repo, "worktree", "add", "-q", "-B", "new-run", run, "origin/main")
	w.exec(`INSERT INTO checkouts (id, project, item, path, branch, run_id, state, created_at, updated_at)
		VALUES ('r-9a8b7c6d5e4f', ?, 'tk-9', ?, 'new-run', 'r-9a8b7c6d5e4f', ?, ?, ?)`, prj, run, CheckoutActive, now, now)
	room := filepath.Join(w.root, "alpha", "loop-alpha-brainstorm-room-write-the-prd-room-1a2b3c4d")
	w.exec(`INSERT INTO rooms (id, project, state, opened_at, kind, checkout_name)
		VALUES ('room-1a2b3c4d', ?, 'opening', ?, 'brainstorm', 'loop-alpha-brainstorm-room-write-the-prd-room-1a2b3c4d')`, prj, now)
	gitIn(t, w.repo, "worktree", "add", "-q", "-B", "new-room", room, "origin/main")
	rep := w.s.CleanCheckouts(context.Background())
	if len(rep.Deleted) != 0 || len(rep.Kept) != 0 || len(rep.Errors) != 0 || !exists(run) || !exists(room) {
		t.Fatalf("report = %+v, run checkout %v, room checkout %v", rep, exists(run), exists(room))
	}
}
