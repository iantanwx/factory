package overwatch

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// The one coordinator_ring row holds the last outcome, updated in place.
func TestRecordRingKeepsOneRow(t *testing.T) {
	w := newFakeWorld(t)
	ctx := context.Background()
	if _, ok, err := LastRing(ctx, w.st); ok || err != nil {
		t.Fatalf("an empty store's ring = %v %v", ok, err)
	}
	at := time.Date(2026, 10, 24, 9, 3, 0, 0, time.UTC)
	first := RingOutcome{At: at, Source: SourceOverwatch, Result: RingRefused, Detail: "notify refused: no receipt"}
	second := RingOutcome{At: at.Add(10 * time.Minute), Source: SourceInbox, Result: RingAccepted}
	for _, o := range []RingOutcome{first, second} {
		if err := RecordRing(ctx, w.st, o); err != nil {
			t.Fatal(err)
		}
		got, ok, err := LastRing(ctx, w.st)
		if err != nil || !ok || !got.At.Equal(o.At) || got.Source != o.Source || got.Result != o.Result || got.Detail != o.Detail {
			t.Fatalf("LastRing = %+v %v %v, want %+v", got, ok, err, o)
		}
	}
	var n int
	if err := w.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedule WHERE kind = ? AND project = ''`, RingKind).Scan(&n)
	}); err != nil || n != 1 {
		t.Fatalf("%d coordinator_ring rows, %v", n, err)
	}
	if err := RecordRing(ctx, w.st, RingOutcome{At: at, Source: "operator", Result: RingAccepted}); err == nil {
		t.Fatal("RecordRing took a third source")
	}
	if err := RecordRing(ctx, w.st, RingOutcome{At: at, Source: SourceInbox, Result: RingAccepted, Detail: "x"}); err == nil {
		t.Fatal("RecordRing took text on an accepted ring")
	}
}

// C-bug-1: the backoff starts at the RefusalLimit-th refusal in a row to
// one handle, doubles with each refusal after it, and stops at HoldMax. A
// ring to another handle is never held.
func TestCBug1TheRingHoldDoublesToItsCap(t *testing.T) {
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		refusals int
		want     time.Duration
	}{{1, 0}, {2, 0}, {3, time.Minute}, {4, 2 * time.Minute}, {5, 4 * time.Minute}, {8, 30 * time.Minute}, {40, 30 * time.Minute}} {
		o := RingOutcome{At: at, Source: SourceInbox, Result: RingRefused, Handle: "h/t/i", Refusals: c.refusals}
		if got := o.Hold(); got != c.want {
			t.Errorf("the hold after %d refusals = %s, want %s", c.refusals, got, c.want)
		}
		if held := o.Held("h/t/i", at.Add(c.want-time.Second)); held != (c.want > 0) {
			t.Errorf("a ring to the same handle %s after %d refusals: held = %v", c.want-time.Second, c.refusals, held)
		}
		if o.Held("h/t/i", at.Add(c.want)) || o.Held("h/t2/i", at) {
			t.Errorf("after %d refusals, a passed hold or another handle is held", c.refusals)
		}
	}
	if (RingOutcome{At: at, Result: RingAccepted, Handle: "h/t/i"}).Held("h/t/i", at) {
		t.Error("an accepted ring holds the next one")
	}
}
