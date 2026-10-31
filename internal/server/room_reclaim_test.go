package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/room"
	"github.com/iantanwx/loop/internal/sched"
)

// A closed room's clean, fully pushed checkout goes on the next cleanup
// pass, and its terminal with it (F-9).
func TestCleanupDeletesAClosedRoomsCleanCheckout(t *testing.T) {
	w := newRoomWorld(t)
	ctx := context.Background()
	r := w.openRoom("brainstorm", "", "the operator has a new idea")
	id, co := r["room"].(string), r["checkout"].(string)
	if env, code := w.coordinator("room", "close", id); code != 0 || w.room(id).State != room.Closed {
		t.Fatalf("close = %d %v", code, env)
	}
	for _, args := range [][]string{{"status", "--porcelain", "--untracked-files=all"}, {"rev-list", "HEAD", "--not", "--remotes"}} {
		out, err := exec.Command("git", append([]string{"-C", co}, args...)...).Output()
		if err != nil || len(out) != 0 {
			t.Fatalf("the room's checkout is not clean and pushed: git %v = %q %v", args, out, err)
		}
	}
	rep := w.srv.Sched.CleanCheckouts(ctx)
	if !slices.Contains(rep.Deleted, co) {
		t.Fatalf("cleanup kept the closed room's clean checkout: %+v", rep)
	}
	if n := w.count(`SELECT count(*) FROM checkouts WHERE path = ? AND state = ?`, co, sched.CheckoutDeleted); n != 1 {
		t.Fatal("the closed room's checkout row is not deleted")
	}
	inv, err := w.host.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range inv.Rows {
		if row.Checkout == co {
			t.Fatalf("the closed room's terminal is still open: %+v", row)
		}
	}
}

// A closed room's checkout that holds uncommitted work stays, and an inbox
// entry names it, so a person knows it waits (F-9).
func TestCleanupKeepsADirtyRoomCheckoutAndSaysSo(t *testing.T) {
	w := newRoomWorld(t)
	ctx := context.Background()
	r := w.openRoom("brainstorm", "", "the operator has a new idea")
	id, co := r["room"].(string), r["checkout"].(string)
	if err := os.WriteFile(filepath.Join(co, "notes.md"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if env, code := w.coordinator("room", "close", id); code != 0 {
		t.Fatalf("close = %d %v", code, env)
	}
	rep := w.srv.Sched.CleanCheckouts(ctx)
	if slices.Contains(rep.Deleted, co) || !exists(co) {
		t.Fatalf("cleanup deleted a dirty checkout: %+v", rep)
	}
	var named bool
	for _, e := range w.entries("person") {
		b, _ := json.Marshal(e)
		named = named || strings.Contains(string(b), co)
	}
	if !named {
		t.Fatalf("no inbox entry for a person names the kept room checkout; cleanup = %+v", rep)
	}
}

// A closed room's checkout with a commit that no remote and no base holds
// stays, and the inbox entry for a person names it with that reason (F-9).
func TestCleanupKeepsAnUnpushedRoomCommitAndSaysWhy(t *testing.T) {
	w := newRoomWorld(t)
	ctx := context.Background()
	r := w.openRoom("brainstorm", "", "the operator has a new idea")
	id, co := r["room"].(string), r["checkout"].(string)
	if err := os.WriteFile(filepath.Join(co, "notes.md"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, co, "add", "notes.md")
	gitT(t, co, "commit", "-q", "-m", "draft")
	if env, code := w.coordinator("room", "close", id); code != 0 {
		t.Fatalf("close = %d %v", code, env)
	}
	rep := w.srv.Sched.CleanCheckouts(ctx)
	why := rep.Kept[co]
	if slices.Contains(rep.Deleted, co) || !exists(co) || !strings.Contains(why, "commit that no remote and no base holds") {
		t.Fatalf("report = %+v", rep)
	}
	var named bool
	for _, e := range w.entries("person") {
		b, _ := json.Marshal(e)
		named = named || strings.Contains(string(b), co) && strings.Contains(string(b), why)
	}
	if !named {
		t.Fatalf("no inbox entry for a person names the checkout and %q", why)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
