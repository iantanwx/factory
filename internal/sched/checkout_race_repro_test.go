package sched

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
)

// A tick can remove one run's checkout while it adds another's, as the
// cleanup pass and a dispatch do in one tick. The test checkouts remove the
// first even when the second's worktree entry is still half made: git add
// has made its directory under .git/worktrees, and not yet its commondir.
func TestGitCheckoutsRemoveOneWhileASiblingIsHalfMade(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	co, err := w.co.CreateCheckout(ctx, agenthost.CheckoutSpec{Name: "loop-alpha-nightly-qa-r-1"})
	if err != nil {
		t.Fatal(err)
	}
	// The sibling as git worktree add leaves it in the instant after it
	// opens commondir and before it writes the file's text.
	half := filepath.Join(w.repo, ".git", "worktrees", "loop-alpha-nightly-qa-r-2")
	if err := os.MkdirAll(half, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"gitdir":    filepath.Join(w.root, "loop-alpha-nightly-qa-r-2", ".git") + "\n",
		"HEAD":      "ref: refs/heads/main\n",
		"commondir": "",
	} {
		if err := os.WriteFile(filepath.Join(half, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.co.RemoveCheckout(ctx, co.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(co.Path); !os.IsNotExist(err) {
		t.Fatalf("the checkout of r-1 still stands: %v", err)
	}
}
