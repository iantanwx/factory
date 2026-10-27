package sched

import (
	"os"
	"testing"
)

// A person removed a settled checkout by hand, and the server restarts on
// a new release. The first tick after the start settles the row as
// deleted. An upgrade restarts the server, so a pass that waits a whole
// cleanup interval after each start may never run (F-40).
func TestTheFirstTickAfterARestartSettlesARowWhosePathIsGone(t *testing.T) {
	w := newWorld(t)
	st := w.settledCheckout()
	w.tick() // closes the finished session
	if err := os.RemoveAll(st.Checkout); err != nil {
		t.Fatal(err)
	}
	gitIn(t, w.repo, "worktree", "prune")
	w.restart()
	w.tick()
	if s := w.str(`SELECT state FROM checkouts WHERE run_id = ?`, st.RunID); s != CheckoutDeleted {
		t.Fatalf("checkout state after the first tick of the restart = %s, want %s", s, CheckoutDeleted)
	}
}
