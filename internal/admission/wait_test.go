package admission

import (
	"context"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// C13: the wait of an item names the run that holds its lease, the room
// that holds it, every counted failure with its stage, class and inputs,
// the limit, and the open alerts of its project that name it. A pending or
// blocked failure, a lease with no owner, another item's rows, a closed alert and another
// project's alert are absent.
func TestWaitOfExplainsWhyAnItemWaits(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO leases (project, item, epoch, owner_run_id, granted_at, renewed_at, ttl_ms) VALUES ('alpha', 'tk-1', 7, 'r-live', 1000, 2000, 60000)`,
		`INSERT INTO leases (project, item, epoch, owner_run_id, granted_at, renewed_at, ttl_ms) VALUES ('beta', 'tk-1', 3, 'r-beta', 1000, 2000, 60000)`,
		`INSERT INTO leases (project, item, epoch) VALUES ('alpha', 'tk-9', 4)`,
		`INSERT INTO holds (project, item, holder, kind, created_at) VALUES ('alpha', 'tk-1', 'room-1', 'room', 1500)`,
		`INSERT INTO holds (project, item, holder, kind, created_at) VALUES ('alpha', 'tk-2', 'room-2', 'room', 1500)`,
		`INSERT INTO failures (run_id, project, item, stage, inputs_hash, class, note, recorded_at) VALUES
			('r-2', 'alpha', 'tk-1', 'implementer', 'h1', 'stalled', 'no call for 20m', 200),
			('r-1', 'alpha', 'tk-1', 'implementer', 'h1', 'crashed', 'session died', 100),
			('r-3', 'alpha', 'tk-1', 'code-reviewer', 'h2', 'failed', 'red', 300),
			('r-4', 'alpha', 'tk-1', 'implementer', 'h1', 'pending', '', 400),
			('r-5', 'alpha', 'tk-1', 'implementer', 'h1', 'blocked', '', 500),
			('r-6', 'alpha', 'tk-2', 'implementer', 'h1', 'failed', '', 600),
			('r-7', 'beta', 'tk-1', 'implementer', 'h1', 'failed', '', 700)`,
		`INSERT INTO alerts (project, kind, subject, dedupe_key, state, first_at, last_at) VALUES
			('alpha', 'retry_limit', 'implementer stopped taking tk-1 after 2 failed attempts', 'retry_limit:alpha/tk-1/implementer/h1', 'open', 300, 300),
			('beta', 'retry_limit', 'implementer stopped taking tk-1 after 2 failed attempts', 'retry_limit:beta/tk-1/implementer/h1', 'open', 300, 300),
			('alpha', 'run_blocked', 'implementer run r-0 on tk-1 finished blocked', 'run_blocked:alpha/tk-1/implementer/h0', 'closed', 50, 50),
			('alpha', 'retry_limit', 'implementer stopped taking tk-12 after 2 failed attempts', 'retry_limit:alpha/tk-12/implementer/h1', 'open', 300, 300)`,
	} {
		if err := w.st.Write(ctx, func(tx *store.Tx) error { _, err := tx.Exec(q); return err }); err != nil {
			t.Fatal(err)
		}
	}

	got, err := WaitOf(w.st)(ctx, "alpha", "tk-1")
	if err != nil {
		t.Fatal(err)
	}
	if l := got.Lease; l == nil || l.Run != "r-live" || l.Epoch != 7 || !l.Granted.Equal(time.UnixMilli(1000)) || !l.Renewed.Equal(time.UnixMilli(2000)) {
		t.Fatalf("lease = %+v", got.Lease)
	}
	if len(got.Holds) != 1 || got.Holds[0] != (machine.WaitHold{Holder: "room-1", Kind: "room", Since: time.UnixMilli(1500)}) {
		t.Fatalf("holds = %+v", got.Holds)
	}
	want := []machine.WaitFailure{
		{Run: "r-1", Stage: "implementer", Class: "crashed", Inputs: "h1", Note: "session died", At: time.UnixMilli(100)},
		{Run: "r-2", Stage: "implementer", Class: "stalled", Inputs: "h1", Note: "no call for 20m", At: time.UnixMilli(200)},
		{Run: "r-3", Stage: "code-reviewer", Class: "failed", Inputs: "h2", Note: "red", At: time.UnixMilli(300)},
	}
	if len(got.Failures) != len(want) {
		t.Fatalf("failures = %+v", got.Failures)
	}
	for i := range want {
		if !got.Failures[i].At.Equal(want[i].At) {
			t.Fatalf("failure %d = %+v, want %+v", i, got.Failures[i], want[i])
		}
		got.Failures[i].At = want[i].At
		if got.Failures[i] != want[i] {
			t.Fatalf("failure %d = %+v, want %+v", i, got.Failures[i], want[i])
		}
	}
	if got.Limit != 2 {
		t.Fatalf("failure_limit = %d", got.Limit)
	}
	if len(got.Alerts) != 1 || got.Alerts[0] != (machine.WaitAlert{Entry: "alert/1", Kind: "retry_limit",
		Subject: "implementer stopped taking tk-1 after 2 failed attempts"}) {
		t.Fatalf("alerts = %+v", got.Alerts)
	}

	empty, err := WaitOf(w.st)(ctx, "alpha", "tk-9")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Lease != nil || empty.Holds == nil || len(empty.Holds) != 0 || empty.Failures == nil || len(empty.Failures) != 0 ||
		empty.Alerts == nil || len(empty.Alerts) != 0 || empty.Limit != 2 {
		t.Fatalf("the wait of an item with none = %+v", empty)
	}
}
