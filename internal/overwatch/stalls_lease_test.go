package overwatch

import (
	"context"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
)

// C10, F-45: a live run of another stage that holds the item's lease
// blocks every claim on the item, so the fires of the item's own stage
// say nothing about it. The code-reviewer run started at 08:40, the item
// moved to repair at 08:45 while that run still reviewed, and the
// implementer fired at 08:48, 08:53, 08:58 and 09:03 since.
func TestC10ALiveRunThatHoldsTheLeaseIsNoStall(t *testing.T) {
	w := newStallsWorld(t)
	w.item("factorio-j48b", machine.KindTicket, machine.Repair, at(8, 45))
	w.entered(t, "factorio-j48b", machine.InReview, machine.Repair, at(8, 45))
	w.run(t, "r-c733f4f7982a", "code-reviewer", "factorio-j48b", at(8, 40), time.Time{})
	w.exec(t, `INSERT INTO leases (project, item, epoch, owner_run_id, granted_at) VALUES ('factorio', 'factorio-j48b', 1, 'r-c733f4f7982a', ?)`,
		at(8, 40).UnixMilli())
	r := readStallsPart(t, w)
	if f, ok := findingOf(r, "overwatch:stalls:factorio-j48b@repair"); ok {
		t.Fatalf("a live code-reviewer run holds the lease of factorio-j48b, and the item is work: %q", f.Line)
	}
	mustContain(t, noteOf(t, r, "factorio-j48b"), "r-c733f4f7982a", "code-reviewer", "holds its lease")
	if code := Sweep(context.Background(), w, stallsOnly(t)).Code; code != Quiet {
		t.Fatalf("the sweep's code = %d, want 0", code)
	}
}

// C10, F-45: a lease whose owner run ended, or a lease with no owner, holds
// nothing, so the implementer's fires at 08:48 and 08:53 still count.
func TestC10ALeaseThatNoLiveRunHoldsIsStillAStall(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, w *stallsWorld)
	}{
		{"the owner run ended", func(t *testing.T, w *stallsWorld) {
			w.run(t, "r-c733f4f7982a", "code-reviewer", "factorio-j48b", at(8, 40), at(8, 44))
			w.exec(t, `INSERT INTO leases (project, item, epoch, owner_run_id, granted_at) VALUES ('factorio', 'factorio-j48b', 1, 'r-c733f4f7982a', ?)`,
				at(8, 40).UnixMilli())
		}},
		{"the lease names no owner", func(t *testing.T, w *stallsWorld) {
			w.run(t, "r-c733f4f7982a", "code-reviewer", "factorio-j48b", at(8, 40), time.Time{})
			w.exec(t, `INSERT INTO leases (project, item, epoch, owner_run_id) VALUES ('factorio', 'factorio-j48b', 2, '')`)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newStallsWorld(t)
			w.item("factorio-j48b", machine.KindTicket, machine.Repair, at(8, 45))
			w.entered(t, "factorio-j48b", machine.InReview, machine.Repair, at(8, 45))
			c.setup(t, w)
			r := readStallsPart(t, w)
			if _, ok := findingOf(r, "overwatch:stalls:factorio-j48b@repair"); !ok {
				t.Fatalf("factorio-j48b is not work: %+v", r)
			}
		})
	}
}
