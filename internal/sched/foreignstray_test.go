package sched

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// A loop directory under the checkout root whose repository no project of
// this server registers belongs to another loop server, such as the orca
// acceptance lane's fixture server. The pass never deletes it, never keeps
// it and never rings a person for it, clean or not (F-50).
func TestCleanupLeavesAWorktreeOfAnUnregisteredRepository(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.AgentHost.CheckoutRoot = w.root })
	_, fixture := makeProjectRepo(t, t.TempDir())
	clean := filepath.Join(w.root, "fixture", "loop-demo-code-reviewer-demo-9pg-add-a-greeting-r-645f1481b0ab")
	gitIn(t, fixture, "worktree", "add", "-q", "-B", "reviewer", clean, "origin/main")
	unpushed := filepath.Join(w.root, "fixture", "loop-demo-implementer-demo-9pg-add-a-greeting-r-3151a00d268a")
	gitIn(t, fixture, "worktree", "add", "-q", "-B", "implementer", unpushed, "origin/main")
	commitIn(t, unpushed, "greeting.txt", "hello\n", "add a greeting")

	rep := w.s.CleanCheckouts(context.Background())
	for _, dir := range []string{clean, unpushed} {
		if !exists(dir) || slices.Contains(rep.Deleted, dir) || rep.Kept[dir] != "" {
			t.Errorf("%s: on disk %v, report = %+v; another server's checkout is not this pass's to judge", filepath.Base(dir), exists(dir), rep)
		}
	}
	if n := w.alerts(AlertCheckoutKept); n != 0 {
		t.Fatalf("checkout_kept alerts raised = %d, want 0", n)
	}
}
