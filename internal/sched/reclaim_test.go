package sched

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/beadsx"
	beadsfake "github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// outageGuard is admission's ClaimGuard that stops beads once it accepts
// a claim, so the claim's intent is recorded and its write meets an
// outage.
type outageGuard struct {
	a     *admission.Admission
	bx    *beadsfake.Client
	armed atomic.Bool
}

func (g *outageGuard) AdmitClaim(ctx context.Context, c machine.ClaimCheck) *cli.Refusal {
	ref := g.a.AdmitClaim(ctx, c)
	if ref == nil && g.armed.Swap(false) {
		g.bx.SetUnavailable(true)
	}
	return ref
}

// A claim whose write meets a beads outage stays recorded, and it lands
// once beads is back. The refused run keeps a settled row, so the next
// tick's sweep frees the lease that landed claim gave it.
func TestClaimLeftPendingByAnOutageReleasesItsLease(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.ticket(machine.Todo)
	g := &outageGuard{a: w.a, bx: w.bx}
	g.armed.Store(true)
	m, err := machine.New(machine.Config{Store: w.st, Beads: func(string) (beadsx.Client, error) { return w.bx, nil },
		Claims: g, Registry: cli.Surface(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	w.a.Attach(m)
	w.m, w.cfg.Machine = m, m
	w.rebuild()

	st, ref := w.s.Dispatch(ctx, prj, machine.StageImplementer)
	if st != nil || ref == nil || ref.Code != cli.CodeBeadsUnavailable {
		t.Fatalf("dispatch = %+v, %+v; want BEADS_UNAVAILABLE", st, ref)
	}
	run := w.str(`SELECT run_id FROM runs`)
	if state, class := w.runState(run); state != "settled" || class != string(admission.ClassNotReady) {
		t.Fatalf("run %q is %s/%s; want settled not-ready", run, state, class)
	}
	if w.count(`SELECT count(*) FROM intents WHERE run_id = ? AND state = 'pending'`, run) != 1 {
		t.Fatal("the claim's intent is not pending")
	}

	w.bx.SetUnavailable(false)
	if err := w.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if l := w.lease(id); l.Owner != run {
		t.Fatalf("lease owner = %q after the claim landed, want %s", l.Owner, run)
	}
	w.tick()
	if l := w.lease(id); l.Owner != "" || w.status(id) != machine.Todo || w.failures(id) != 0 {
		t.Fatalf("lease %+v, status %s, failures %d", l, w.status(id), w.failures(id))
	}
}

// A lease whose owner has no run row counts as settled, and the sweep
// frees it.
func TestSweepFreesALeaseWhoseRunRowIsGone(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.ticket(machine.Todo)
	ad, ref := w.a.Admit(ctx, admissionRequest(id))
	if ref != nil {
		t.Fatal(ref)
	}
	if err := w.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM runs WHERE run_id = ?`, ad.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.tick()
	if l := w.lease(id); l.Owner != "" || w.status(id) != machine.Todo {
		t.Fatalf("lease %+v, status %s", l, w.status(id))
	}
}
