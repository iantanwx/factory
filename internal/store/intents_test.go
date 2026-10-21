package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func record(t *testing.T, s *Store, in Intent, ob []OutboxRow, c *CursorAdvance) Intent {
	t.Helper()
	var out Intent
	err := s.Write(context.Background(), func(tx *Tx) error {
		var err error
		out, err = tx.RecordIntent(in, ob, c)
		return err
	})
	if err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	return out
}

func apply(t *testing.T, s *Store, id int64) {
	t.Helper()
	err := s.Write(context.Background(), func(tx *Tx) error {
		return tx.MarkApplied(id, json.RawMessage(`{}`))
	})
	if err != nil {
		t.Fatalf("MarkApplied(%d): %v", id, err)
	}
}

func TestIntentSequenceIncreasesPerItemAndIndependently(t *testing.T) {
	s := openTemp(t)
	var a, b []int64
	for i := 0; i < 3; i++ {
		ia := record(t, s, Intent{Project: "p", Item: "bd-a", Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil)
		apply(t, s, ia.ID)
		a = append(a, ia.Seq)
		ib := record(t, s, Intent{Project: "p", Item: "bd-b", Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil)
		apply(t, s, ib.ID)
		b = append(b, ib.Seq)
	}
	other := record(t, s, Intent{Project: "q", Item: "bd-a", Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil)
	if a[0] != 1 || a[1] != 2 || a[2] != 3 || b[0] != 1 || b[1] != 2 || b[2] != 3 || other.Seq != 1 {
		t.Fatalf("sequences: a %v, b %v, other project %d; want 1,2,3 each and 1", a, b, other.Seq)
	}
}

func TestRolledBackWriteConsumesNoSequence(t *testing.T) {
	s := openTemp(t)
	first := record(t, s, Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil)
	apply(t, s, first.ID)
	boom := errors.New("boom")
	err := s.Write(context.Background(), func(tx *Tx) error {
		if _, err := tx.RecordIntent(Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1},
			[]OutboxRow{{EffectKind: "comment", NaturalKey: "m-1"}}, &CursorAdvance{Project: "p", Stream: "pulls", Cursor: "c9"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Write err = %v", err)
	}
	if n := countRows(t, s, "outbox"); n != 0 {
		t.Fatalf("outbox = %d after rollback", n)
	}
	if n := countRows(t, s, "github_cursors"); n != 0 {
		t.Fatalf("github_cursors = %d after rollback", n)
	}
	next := record(t, s, Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil)
	if next.Seq != 2 {
		t.Fatalf("sequence after a rolled-back intent = %d, want 2", next.Seq)
	}
}

func TestRecordIntentStoresIntentOutboxAndCursorTogether(t *testing.T) {
	s := openTemp(t)
	in := record(t, s, Intent{
		Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", IdemKey: "k1",
		ExpectedStatus: "in_progress", TargetStatus: "review", Facts: json.RawMessage(`{"pr":7}`),
		Epoch: 4, RunID: "run-1", Actor: "implementer",
	}, []OutboxRow{
		{EffectKind: "status", NaturalKey: "abc123/loop/review", Payload: json.RawMessage(`{"state":"pending"}`)},
		{EffectKind: "comment", NaturalKey: "marker-1"},
	}, &CursorAdvance{Project: "p", Stream: "pulls", Cursor: "etag-1"})

	if in.ID == 0 || in.Seq != 1 || in.State != IntentPending || in.CreatedAt.IsZero() {
		t.Fatalf("recorded intent = %+v", in)
	}
	err := s.Write(context.Background(), func(tx *Tx) error {
		got, err := tx.GetIntent(in.ID)
		if err != nil {
			return err
		}
		if got.IdemKey != "k1" || got.ExpectedStatus != "in_progress" || got.TargetStatus != "review" ||
			string(got.Facts) != `{"pr":7}` || got.Epoch != 4 || got.RunID != "run-1" || got.Actor != "implementer" {
			t.Fatalf("GetIntent = %+v", got)
		}
		rows, err := tx.OutboxFor(in.ID)
		if err != nil {
			return err
		}
		if len(rows) != 2 || rows[0].State != OutboxHeld || string(rows[0].Payload) != `{"state":"pending"}` || string(rows[1].Payload) != `{}` {
			t.Fatalf("OutboxFor = %+v", rows)
		}
		var cursor string
		if err := tx.QueryRow(`SELECT cursor FROM github_cursors WHERE project = 'p' AND stream = 'pulls'`).Scan(&cursor); err != nil {
			return err
		}
		if cursor != "etag-1" {
			t.Fatalf("cursor = %q", cursor)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOutboxBecomesRunnableOnlyAfterApplied(t *testing.T) {
	s := openTemp(t)
	in := record(t, s, Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1},
		[]OutboxRow{{EffectKind: "pr", NaturalKey: "branch-1"}, {EffectKind: "comment", NaturalKey: "m-1"}}, nil)
	runnable := func() []OutboxRow {
		var rows []OutboxRow
		err := s.Write(context.Background(), func(tx *Tx) error {
			var err error
			rows, err = tx.RunnableOutbox(10)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if rows := runnable(); len(rows) != 0 {
		t.Fatalf("runnable before apply = %+v", rows)
	}
	apply(t, s, in.ID)
	rows := runnable()
	if len(rows) != 2 || rows[0].NaturalKey != "branch-1" || rows[1].NaturalKey != "m-1" || rows[0].State != OutboxRunnable {
		t.Fatalf("runnable after apply = %+v", rows)
	}
	err := s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.MarkEffectDone(rows[0].ID); err != nil {
			return err
		}
		return tx.MarkEffectRetry(rows[1].ID, tx.Now().Add(1e12), "rate limited")
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows := runnable(); len(rows) != 0 {
		t.Fatalf("runnable after done and a deferred retry = %+v", rows)
	}
}

func TestRefusedIntentCancelsItsOutboxAndStoresTheRefusal(t *testing.T) {
	s := openTemp(t)
	fp := Fingerprint{RunID: "run-1", Verb: "move", ArgsHash: "a"}
	var in Intent
	err := s.Write(context.Background(), func(tx *Tx) error {
		var err error
		in, err = tx.RecordIntent(Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", IdemKey: "k1", Epoch: 1},
			[]OutboxRow{{EffectKind: "comment", NaturalKey: "m-1"}}, nil)
		if err != nil {
			return err
		}
		return tx.StoreResult(IdemRecord{Key: "k1", Fingerprint: fp, IntentID: in.ID})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.MarkRefused(in.ID, json.RawMessage(`{"code":"LEASE_NOT_HELD"}`)); err != nil {
			return err
		}
		rows, err := tx.OutboxFor(in.ID)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].State != OutboxCancelled {
			t.Fatalf("outbox after refusal = %+v", rows)
		}
		rec, _, err := tx.LookupKey("k1", fp)
		if err != nil || string(rec.Result) != `{"code":"LEASE_NOT_HELD"}` {
			t.Fatalf("key after refusal = %+v, err %v", rec, err)
		}
		got, err := tx.GetIntent(in.ID)
		if err != nil || got.State != IntentRefused || got.SettledAt.IsZero() {
			t.Fatalf("intent after refusal = %+v, err %v", got, err)
		}
		// A cancelled effect frees its natural key.
		_, err = tx.RecordIntent(Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1},
			[]OutboxRow{{EffectKind: "comment", NaturalKey: "m-1"}}, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSettlingTwiceOrUnknownIntentFails(t *testing.T) {
	s := openTemp(t)
	in := record(t, s, Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil)
	apply(t, s, in.ID)
	err := s.Write(context.Background(), func(tx *Tx) error { return tx.MarkApplied(in.ID, nil) })
	if !errors.Is(err, ErrIntentSettled) {
		t.Fatalf("second MarkApplied err = %v, want ErrIntentSettled", err)
	}
	err = s.Write(context.Background(), func(tx *Tx) error { return tx.MarkRefused(in.ID, nil) })
	if !errors.Is(err, ErrIntentSettled) {
		t.Fatalf("MarkRefused after apply err = %v, want ErrIntentSettled", err)
	}
	err = s.Write(context.Background(), func(tx *Tx) error { return tx.MarkApplied(9999, nil) })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkApplied(unknown) err = %v, want ErrNotFound", err)
	}
	err = s.Write(context.Background(), func(tx *Tx) error { _, err := tx.GetIntent(9999); return err })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetIntent(unknown) err = %v, want ErrNotFound", err)
	}
}

func TestSecondUnappliedIntentForAnItemIsRefused(t *testing.T) {
	s := openTemp(t)
	record(t, s, Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil)
	err := s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.RecordIntent(Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil)
		return err
	})
	if !errors.Is(err, ErrIntentInFlight) {
		t.Fatalf("second pending intent err = %v, want ErrIntentInFlight", err)
	}
}

func TestDuplicateLiveEffectIsRefused(t *testing.T) {
	s := openTemp(t)
	record(t, s, Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1},
		[]OutboxRow{{EffectKind: "pr", NaturalKey: "branch-1"}}, nil)
	err := s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.RecordIntent(Intent{Project: "p", Item: "bd-2", Kind: "ticket", Verb: "move", Epoch: 1},
			[]OutboxRow{{EffectKind: "pr", NaturalKey: "branch-1"}}, nil)
		return err
	})
	if !errors.Is(err, ErrEffectExists) {
		t.Fatalf("duplicate effect err = %v, want ErrEffectExists", err)
	}
}

func TestUnappliedIntentsReturnsIntentOrder(t *testing.T) {
	s := openTemp(t)
	done := record(t, s, Intent{Project: "p", Item: "bd-0", Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil)
	apply(t, s, done.ID)
	var want []int64
	for _, item := range []string{"bd-3", "bd-1", "bd-2"} {
		want = append(want, record(t, s, Intent{Project: "p", Item: item, Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil).ID)
	}
	var got []Intent
	err := s.Write(context.Background(), func(tx *Tx) error {
		var err error
		got, err = tx.UnappliedIntents()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != want[0] || got[1].ID != want[1] || got[2].ID != want[2] || got[0].Item != "bd-3" {
		t.Fatalf("UnappliedIntents = %+v, want ids %v", got, want)
	}
}

func TestRaiseItemSeqOnlyRaises(t *testing.T) {
	s := openTemp(t)
	err := s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.RaiseItemSeq("p", "bd-1", 7); err != nil {
			return err
		}
		if err := tx.RaiseItemSeq("p", "bd-1", 3); err != nil {
			return err
		}
		n, err := tx.ItemSeq("p", "bd-1")
		if err != nil || n != 7 {
			t.Fatalf("ItemSeq = %d, err %v; want 7", n, err)
		}
		in, err := tx.RecordIntent(Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1}, nil, nil)
		if err != nil || in.Seq != 8 {
			t.Fatalf("seq after raise = %d, err %v; want 8", in.Seq, err)
		}
		n, err = tx.ItemSeq("p", "never")
		if err != nil || n != 0 {
			t.Fatalf("ItemSeq(unknown) = %d, err %v; want 0", n, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetriedEffectBecomesDueAtItsNextAttempt(t *testing.T) {
	s := openTemp(t)
	base := time.UnixMilli(1_800_000_000_000)
	s.now = func() time.Time { return base }
	in := record(t, s, Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", Epoch: 1},
		[]OutboxRow{{EffectKind: "comment", NaturalKey: "m-1"}}, nil)
	apply(t, s, in.ID)
	var id int64
	err := s.Write(context.Background(), func(tx *Tx) error {
		rows, err := tx.RunnableOutbox(1)
		if err != nil || len(rows) != 1 {
			t.Fatalf("RunnableOutbox = %+v, %v", rows, err)
		}
		id = rows[0].ID
		return tx.MarkEffectRetry(id, base.Add(time.Minute), "502")
	})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return base.Add(time.Minute) }
	err = s.Write(context.Background(), func(tx *Tx) error {
		rows, err := tx.RunnableOutbox(5)
		if err != nil || len(rows) != 1 || rows[0].Attempts != 1 || rows[0].LastError != "502" || !rows[0].NextAttemptAt.Equal(base.Add(time.Minute)) {
			t.Fatalf("due retry = %+v, %v", rows, err)
		}
		if err := tx.MarkEffectFailed(id, "gave up"); err != nil {
			return err
		}
		if err := tx.MarkEffectDone(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("MarkEffectDone on a failed row err = %v, want ErrNotFound", err)
		}
		rows, err = tx.OutboxFor(in.ID)
		if err != nil || rows[0].State != OutboxFailed || rows[0].Attempts != 2 {
			t.Fatalf("failed row = %+v, %v", rows, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIntentKeyHeldByAnotherIntentIsAConflict(t *testing.T) {
	s := openTemp(t)
	first := record(t, s, Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", IdemKey: "k1", Epoch: 1}, nil, nil)
	apply(t, s, first.ID)
	err := s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.RecordIntent(Intent{Project: "p", Item: "bd-2", Kind: "ticket", Verb: "move", IdemKey: "k1", Epoch: 1}, nil, nil)
		return err
	})
	if !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("err = %v, want ErrKeyConflict", err)
	}
}
