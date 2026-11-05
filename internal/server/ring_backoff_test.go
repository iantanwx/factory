package server

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/overwatch"
	"github.com/iantanwx/loop/internal/sched"
)

// ringAlert is one alerts row whose subject or body names a terminal.
type ringAlert struct {
	body  string
	state string
	count int
}

// ringAlerts reads every alerts row, open or closed, whose subject or body
// names terminal, oldest first.
func (w *humanWorld) ringAlerts(terminal string) []ringAlert {
	w.t.Helper()
	ctx := context.Background()
	var out []ringAlert
	if err := w.srv.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT subject, body, state, count FROM alerts WHERE dedupe_key != 'u' ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var s string
			var a ringAlert
			if err := rows.Scan(&s, &a.body, &a.state, &a.count); err != nil {
				return err
			}
			if strings.Contains(s+" "+a.body, terminal) {
				out = append(out, a)
			}
		}
		return rows.Err()
	}); err != nil {
		w.t.Fatal(err)
	}
	return out
}

// refusedRingWorld is a human world whose overwatch job always has work
// and whose agent host counts each Notify.
func refusedRingWorld(t *testing.T) (*humanWorld, *countingHost, *overwatch.Job) {
	t.Helper()
	w := newHumanWorld(t, "")
	counter := &countingHost{Host: w.host}
	w.srv.opts.Agents = counter
	w.srv.setOverwatchParts(workParts())
	return w, counter, w.srv.overwatchJob()
}

// C-bug-1: a ring that the host accepts ends the backoff, and it closes
// the alert. Repeat refusals count up on the one open alert, and a new run
// of refusals after the accepted ring rings RefusalLimit times again
// before it backs off.
func TestCBug1AnAcceptedRingResetsTheBackoffAndClearsTheAlert(t *testing.T) {
	w, counter, j := refusedRingWorld(t)
	ctx := context.Background()
	session := w.srv.Sched.Coordinator().Session.ID
	w.host.RefuseNotify(session, true)

	for i := 0; i < 3; i++ {
		_ = j.Tick(ctx)
	}
	if got := counter.notifies.Load(); got != 3 {
		t.Fatalf("Notify calls after three refused rings = %d, want 3", got)
	}
	if a := w.ringAlerts(session.Terminal); len(a) != 1 || a[0].state != "open" || a[0].count != 1 {
		t.Fatalf("alerts that name %s after three refusals = %+v, want one open row", session.Terminal, a)
	}
	// The alert quotes the host's own refusal, as the ring row stores it.
	refused, _ := w.lastRing("")
	if !strings.Contains(refused.Detail, "host refused input to "+session.String()) {
		t.Fatalf("the stored refusal = %q, want the fake host's refusal of %s", refused.Detail, session)
	}
	if a := w.ringAlerts(session.Terminal); !strings.Contains(a[0].body, refused.Detail) {
		t.Fatalf("the alert body = %q, want it to quote the refusal %q", a[0].body, refused.Detail)
	}
	_ = j.Tick(ctx)
	if got := counter.notifies.Load(); got != 3 {
		t.Fatalf("Notify calls in the backoff = %d, want 3: the server rang a held session", got)
	}

	// The hold passes, and the fourth ring is refused too: it counts up on
	// the open alert.
	w.clock.Advance(time.Hour)
	_ = j.Tick(ctx)
	if got := counter.notifies.Load(); got != 4 {
		t.Fatalf("Notify calls after the hold = %d, want 4", got)
	}
	if a := w.ringAlerts(session.Terminal); len(a) != 1 || a[0].state != "open" || a[0].count != 2 {
		t.Fatalf("alerts that name %s after four refusals = %+v, want one open row with count 2", session.Terminal, a)
	}

	// The host takes the next ring: the alert closes.
	w.host.RefuseNotify(session, false)
	w.clock.Advance(time.Hour)
	if err := j.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if o, _ := w.lastRing(""); o.Result != overwatch.RingAccepted {
		t.Fatalf("the ring after the host took input = %+v, want accepted", o)
	}
	if a := w.ringAlerts(session.Terminal); len(a) != 1 || a[0].state != "closed" {
		t.Fatalf("alerts that name %s after an accepted ring = %+v, want one closed row", session.Terminal, a)
	}

	// The count starts again: with no time passed, three new refusals each
	// reach the host before the backoff holds the fourth.
	w.host.RefuseNotify(session, true)
	before := counter.notifies.Load()
	for i := 0; i < 4; i++ {
		_ = j.Tick(ctx)
	}
	if got := counter.notifies.Load() - before; got != 3 {
		t.Fatalf("Notify calls in four rings after the reset = %d, want 3", got)
	}
}

// C-bug-1: when the stored coordinator session changes, the backoff of the
// old handle ends. The first ring goes to the new handle at once, and an
// accepted ring to it closes the old handle's alert.
func TestCBug1TheBackoffResetsWhenTheStoredSessionChanges(t *testing.T) {
	w, counter, j := refusedRingWorld(t)
	ctx := context.Background()
	old := w.srv.Sched.Coordinator().Session.ID
	w.host.RefuseNotify(old, true)
	if err := config.StoreAlerter(w.srv.Store).Raise(ctx, config.Alert{Project: "alpha", Kind: sched.AlertRunUnknown, DedupeKey: "u",
		Subject: "a run's liveness is unknown"}); err != nil {
		t.Fatal(err)
	}

	// Both doorbells refuse until the backoff holds the old handle.
	for i := 0; i < 3; i++ {
		_ = w.srv.Notifier.Poke(ctx)
		_ = j.Tick(ctx)
	}
	if got := counter.notifies.Load(); got != 3 {
		t.Fatalf("Notify calls to the refused session = %d, want 3 before the backoff", got)
	}
	if a := w.ringAlerts(old.Terminal); len(a) != 1 || a[0].state != "open" {
		t.Fatalf("alerts that name %s = %+v, want one open row", old.Terminal, a)
	}

	// The host moves the session to a new handle, and the scheduler
	// rebinds the stored session. No time passes.
	w.host.Restart(false)
	w.host.RefuseNotify(old, false)
	if err := w.srv.Sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	moved := w.srv.Sched.Coordinator().Session.ID
	if moved == old {
		t.Fatalf("fixture: the scheduler did not rebind %s", old)
	}
	before := counter.notifies.Load()
	if err := j.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := counter.notifies.Load() - before; got != 1 {
		t.Fatalf("Notify calls to the rebound session %s = %d, want 1: the old handle's backoff held it", moved, got)
	}
	if o, _ := w.lastRing(""); o.Result != overwatch.RingAccepted {
		t.Fatalf("the ring to the rebound session = %+v, want accepted", o)
	}
	if a := w.ringAlerts(old.Terminal); len(a) != 1 || a[0].state != "closed" {
		t.Fatalf("alerts that name the old handle %s after an accepted ring = %+v, want one closed row", old.Terminal, a)
	}
}
