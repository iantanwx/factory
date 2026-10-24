package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/room"
)

// mergeElsewhere merges a change into the origin of the project clone repo
// from another clone, so the remote base moves on and the clone's local
// main stays where it was. It returns both heads.
func mergeElsewhere(t *testing.T, repo string) (stale, head string) {
	t.Helper()
	origin := filepath.Join(filepath.Dir(repo), "origin.git")
	stale = revParse(t, repo, "main")
	other := filepath.Join(t.TempDir(), "other")
	gitT(t, filepath.Dir(other), "clone", "-q", origin, other)
	if err := os.WriteFile(filepath.Join(other, "merged.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, other, "add", "-A")
	gitT(t, other, "commit", "-q", "-m", "merged")
	gitT(t, other, "push", "-q", "origin", "main")
	head = revParse(t, origin, "main")
	if head == stale {
		t.Fatal("the remote base did not move")
	}
	return stale, head
}

func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := gitx.Run(context.Background(), dir, "rev-parse", rev)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func (w *humanWorld) repo() string {
	w.t.Helper()
	p, err := w.srv.Projects.Show("alpha")
	if err != nil {
		w.t.Fatal(err)
	}
	return p.Path
}

// C-bug-1 (F-14): a room's checkout starts at the head the remote holds
// for the base branch, not at the clone's local main.
func TestRoomCheckoutStartsAtTheRemoteBaseHead(t *testing.T) {
	w := newRoomWorld(t)
	stale, head := mergeElsewhere(t, w.repo())
	item := todoTicket(t, w.bx)

	r := w.openRoom("review", item, "the operator asked to see the diff")
	if got := revParse(t, r["checkout"].(string), "HEAD"); got != head {
		t.Fatalf("room checkout HEAD = %s, want the remote base head %s (the clone's local main is %s)", got, head, stale)
	}
}

// C-bug-1: when the server cannot read the remote base head, the room
// does not start. The coordinator's open is refused with the cause, and
// no checkout starts from the clone's local main.
func TestRoomWithNoReadableBaseHeadDoesNotStart(t *testing.T) {
	w := newRoomWorld(t)
	origin := filepath.Join(filepath.Dir(w.repo()), "origin.git")
	if err := os.Rename(origin, origin+".gone"); err != nil {
		t.Fatal(err)
	}
	item := todoTicket(t, w.bx)

	env, code := w.coordinator("room", "open", "--kind", "review", "--project", "alpha", "--item", item, "--reason", "r", "--score", "4")
	if code == 0 {
		t.Fatalf("a room opened with no readable base head: %v", env)
	}
	if hint, _ := env["hint"].(string); !strings.Contains(hint, "read the head of main") {
		t.Fatalf("the refusal does not name the base head: %v", env)
	}
	if n := w.count(`SELECT count(*) FROM rooms WHERE state = ? AND checkout_id IS NULL`, room.Interrupted); n != 1 {
		t.Fatalf("%d interrupted rooms with no checkout, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM checkouts`); n != 0 {
		t.Fatalf("%d checkouts, want none", n)
	}
}
