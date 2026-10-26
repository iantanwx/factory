package server

import (
	"context"
	"os"
	"testing"

	"github.com/iantanwx/loop/internal/room"
	"github.com/iantanwx/loop/internal/sched"
)

// A closed room's checkout that is gone by the next pass leaves no row
// that names a missing path: the pass settles the row as deleted, and it
// reports no error for it (F-34).
func TestCleanupSettlesAClosedRoomsMissingCheckout(t *testing.T) {
	w := newRoomWorld(t)
	ctx := context.Background()
	r := w.openRoom("intake", "", "the operator has a new idea")
	id, co := r["room"].(string), r["checkout"].(string)
	if env, code := w.coordinator("room", "close", id); code != 0 || w.room(id).State != room.Closed {
		t.Fatalf("close = %d %v", code, env)
	}
	if err := os.RemoveAll(co); err != nil {
		t.Fatal(err)
	}
	rep := w.srv.Sched.CleanCheckouts(ctx)
	if why, ok := rep.Errors[co]; ok {
		t.Fatalf("a closed room's missing checkout is an error: %s", why)
	}
	if n := w.count(`SELECT count(*) FROM checkouts WHERE path = ? AND state = ?`, co, sched.CheckoutDeleted); n != 1 {
		t.Fatal("the closed room's missing checkout keeps a row that is not deleted")
	}
}
