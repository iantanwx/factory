package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSameKeySameBodyReturnsStoredResult(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	fp := Fingerprint{RunID: "run-1", Verb: "note add", ArgsHash: "args-a", ContentHash: "body-a"}

	err := s.Write(ctx, func(tx *Tx) error {
		if _, found, err := tx.LookupKey("k1", fp); err != nil || found {
			t.Fatalf("first lookup: found %v, err %v; want a miss", found, err)
		}
		return tx.StoreResult(IdemRecord{Key: "k1", Fingerprint: fp, Result: json.RawMessage(`{"ok":true}`)})
	})
	if err != nil {
		t.Fatal(err)
	}

	err = s.Write(ctx, func(tx *Tx) error {
		rec, found, err := tx.LookupKey("k1", fp)
		if err != nil || !found {
			t.Fatalf("replay lookup: found %v, err %v", found, err)
		}
		if string(rec.Result) != `{"ok":true}` || rec.Pending() || rec.Fingerprint != fp {
			t.Fatalf("replay record = %+v", rec)
		}
		// Storing the same key and fingerprint again keeps the first result.
		return tx.StoreResult(IdemRecord{Key: "k1", Fingerprint: fp, Result: json.RawMessage(`{"ok":false}`)})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *Tx) error {
		rec, _, err := tx.LookupKey("k1", fp)
		if err == nil && string(rec.Result) != `{"ok":true}` {
			t.Fatalf("stored result changed to %s", rec.Result)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSameKeyDifferentBodyIsRefused(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	fp := Fingerprint{RunID: "run-1", Verb: "note add", ArgsHash: "args-a", ContentHash: "body-a"}
	err := s.Write(ctx, func(tx *Tx) error {
		return tx.StoreResult(IdemRecord{Key: "k1", Fingerprint: fp, Result: json.RawMessage(`{}`)})
	})
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string]Fingerprint{
		"body": {RunID: "run-1", Verb: "note add", ArgsHash: "args-a", ContentHash: "body-b"},
		"args": {RunID: "run-1", Verb: "note add", ArgsHash: "args-b", ContentHash: "body-a"},
		"verb": {RunID: "run-1", Verb: "reply", ArgsHash: "args-a", ContentHash: "body-a"},
		"run":  {RunID: "run-2", Verb: "note add", ArgsHash: "args-a", ContentHash: "body-a"},
	}
	for name, other := range variants {
		t.Run(name, func(t *testing.T) {
			err := s.Write(ctx, func(tx *Tx) error {
				_, _, err := tx.LookupKey("k1", other)
				return err
			})
			if !errors.Is(err, ErrKeyConflict) {
				t.Fatalf("LookupKey err = %v, want ErrKeyConflict", err)
			}
			err = s.Write(ctx, func(tx *Tx) error {
				return tx.StoreResult(IdemRecord{Key: "k1", Fingerprint: other, Result: json.RawMessage(`{}`)})
			})
			if !errors.Is(err, ErrKeyConflict) {
				t.Fatalf("StoreResult err = %v, want ErrKeyConflict", err)
			}
		})
	}
}

func TestKeyOfAnInFlightIntentIsPendingUntilApplied(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	fp := Fingerprint{RunID: "run-1", Verb: "ticket move", ArgsHash: "a"}
	var intentID int64
	err := s.Write(ctx, func(tx *Tx) error {
		in, err := tx.RecordIntent(Intent{Project: "p", Item: "bd-1", Kind: "ticket", Verb: "move", IdemKey: "k1", Epoch: 1}, nil, nil)
		if err != nil {
			return err
		}
		intentID = in.ID
		return tx.StoreResult(IdemRecord{Key: "k1", Fingerprint: fp, IntentID: in.ID})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *Tx) error {
		rec, found, err := tx.LookupKey("k1", fp)
		if err != nil || !found || !rec.Pending() || rec.IntentID != intentID {
			t.Fatalf("in-flight lookup = %+v, found %v, err %v", rec, found, err)
		}
		return tx.MarkApplied(intentID, json.RawMessage(`{"status":"review"}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *Tx) error {
		rec, found, err := tx.LookupKey("k1", fp)
		if err != nil || !found || rec.Pending() || string(rec.Result) != `{"status":"review"}` {
			t.Fatalf("applied lookup = %+v, found %v, err %v", rec, found, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestKeysExpireOnlyAfterTheirRunSettles(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	fp1 := Fingerprint{RunID: "run-1", Verb: "note add", ArgsHash: "a"}
	fp2 := Fingerprint{RunID: "run-2", Verb: "note add", ArgsHash: "a"}
	err := s.Write(ctx, func(tx *Tx) error {
		if err := tx.StoreResult(IdemRecord{Key: "k1", Fingerprint: fp1, Result: json.RawMessage(`{}`)}); err != nil {
			return err
		}
		if err := tx.StoreResult(IdemRecord{Key: "k2", Fingerprint: fp2, Result: json.RawMessage(`{}`)}); err != nil {
			return err
		}
		n, err := tx.ExpireRunKeys("run-1", tx.Now().Add(100*time.Second))
		if err != nil || n != 1 {
			t.Fatalf("ExpireRunKeys = %d, %v; want 1", n, err)
		}
		if n, err := tx.PurgeExpiredKeys(tx.Now()); err != nil || n != 0 {
			t.Fatalf("early purge = %d, %v; want 0", n, err)
		}
		if n, err := tx.PurgeExpiredKeys(tx.Now().Add(100 * time.Second)); err != nil || n != 1 {
			t.Fatalf("purge = %d, %v; want 1", n, err)
		}
		if _, found, err := tx.LookupKey("k1", fp1); err != nil || found {
			t.Fatalf("k1 after purge: found %v, err %v", found, err)
		}
		if _, found, err := tx.LookupKey("k2", fp2); err != nil || !found {
			t.Fatalf("k2 of an unsettled run: found %v, err %v", found, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
