package overwatch

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// The ring record of each destination is its own coordinator_ring row,
// and a new ring replaces it. On a host with a loop project, LastRings
// leaves out the row of the destination "", which an earlier release
// wrote.
func TestRecordRingKeepsOneRowForEachDestination(t *testing.T) {
	w := newFakeWorld(t)
	ctx := context.Background()
	if got, err := LastRings(ctx, w.st, ""); len(got) != 0 || err != nil {
		t.Fatalf("an empty store's rings = %v %v", got, err)
	}
	at := time.Date(2026, 10, 24, 9, 3, 0, 0, time.UTC)
	old := RingOutcome{At: at, Source: SourceOverwatch, Result: RingRefused, Detail: "notify refused: no receipt", Refusals: 1}
	both := RingOutcome{At: at.Add(5 * time.Minute), Source: SourceInbox, Result: RingRefused, Detail: "closed", Handle: "h/t/i", Refusals: 1}
	a := RingOutcome{At: at.Add(10 * time.Minute), Source: SourceInbox, Result: RingAccepted, Handle: "h/t/i"}
	for _, r := range []struct {
		dests []string
		o     RingOutcome
	}{{[]string{""}, old}, {[]string{"a", "b"}, both}, {[]string{"a"}, a}} {
		if err := RecordRing(ctx, w.st, r.dests, r.o); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LastRings(ctx, w.st, "")
	if err != nil || len(got) != 3 || !got[""].At.Equal(old.At) || got[""].Detail != old.Detail ||
		got["a"].Result != RingAccepted || !got["a"].At.Equal(a.At) || got["b"].Result != RingRefused || got["b"].Handle != "h/t/i" {
		t.Fatalf("LastRings = %+v %v", got, err)
	}
	if got, err := LastRings(ctx, w.st, "loop"); err != nil || len(got) != 2 || got["a"].Result != RingAccepted {
		t.Fatalf("LastRings on a host with a loop project = %+v %v, want a and b only", got, err)
	}
	var n int
	if err := w.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedule WHERE kind = ?`, RingKind).Scan(&n)
	}); err != nil || n != 3 {
		t.Fatalf("%d coordinator_ring rows, %v, want one for each destination", n, err)
	}
	// Rings reads the rows oldest write first, also when two writes share
	// one time.
	if err := RecordRing(ctx, w.st, []string{"b"}, RingOutcome{At: a.At, Source: SourceInbox, Result: RingAccepted}); err != nil {
		t.Fatal(err)
	}
	rs, err := Rings(ctx, w.st)
	if err != nil || len(rs) != 3 || rs[0].Dest != "" || rs[1].Dest != "a" || rs[2].Dest != "b" || rs[2].Outcome.Result != RingAccepted {
		t.Fatalf("Rings = %+v %v, want \"\", a, b", rs, err)
	}
	if err := RecordRing(ctx, w.st, []string{""}, RingOutcome{At: at, Source: "operator", Result: RingAccepted}); err == nil {
		t.Fatal("RecordRing took a third source")
	}
	if err := RecordRing(ctx, w.st, []string{""}, RingOutcome{At: at, Source: SourceInbox, Result: RingAccepted, Detail: "x"}); err == nil {
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
