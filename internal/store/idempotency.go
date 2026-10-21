package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Fingerprint is what a stored idempotency key must match (KTD13). The same
// key with any different field is a conflict.
type Fingerprint struct {
	RunID       string
	Verb        string
	ArgsHash    string // hash of the canonical arguments
	ContentHash string // hash of the written content; empty when the verb writes none
}

// IdemRecord is one stored idempotency key.
type IdemRecord struct {
	Key string
	Fingerprint
	IntentID  int64           // the key's intent; 0 when the write records none
	Result    json.RawMessage // nil while the key's intent is in flight
	CreatedAt time.Time
	ExpiresAt time.Time // zero until the key's run settles
}

// Pending reports whether the key's write has no result yet. A retry of a
// pending key joins its in-flight intent (KTD6).
func (r IdemRecord) Pending() bool { return r.Result == nil }

// LookupKey returns the stored record for key. found is false when the key
// is not stored. A stored key with a different fingerprint returns an error
// that matches ErrKeyConflict.
func (t *Tx) LookupKey(key string, fp Fingerprint) (rec IdemRecord, found bool, err error) {
	rec, err = t.getKey(key)
	if errors.Is(err, ErrNotFound) {
		return IdemRecord{}, false, nil
	}
	if err != nil {
		return IdemRecord{}, false, err
	}
	if rec.Fingerprint != fp {
		return IdemRecord{}, false, fmt.Errorf("%w: key %s", ErrKeyConflict, key)
	}
	return rec, true, nil
}

// StoreResult stores rec under rec.Key. A nil Result reserves the key for an
// in-flight intent. When the key is already stored with the same
// fingerprint, StoreResult fills a missing result or intent id and never
// replaces a stored result. A different fingerprint returns an error that
// matches ErrKeyConflict.
func (t *Tx) StoreResult(rec IdemRecord) error {
	if rec.Key == "" || rec.RunID == "" || rec.Verb == "" {
		return fmt.Errorf("store: idempotency record needs a key, a run id and a verb")
	}
	res, err := t.Exec(`INSERT INTO idempotency
		(key, run_id, verb, args_hash, content_hash, intent_id, result, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key) DO NOTHING`,
		rec.Key, rec.RunID, rec.Verb, rec.ArgsHash, rec.ContentHash,
		nullID(rec.IntentID), nullJSON(rec.Result), t.now.UnixMilli(), millis(rec.ExpiresAt))
	if err != nil {
		return fmt.Errorf("store: store key %s: %w", rec.Key, err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	stored, err := t.getKey(rec.Key)
	if err != nil {
		return err
	}
	if stored.Fingerprint != rec.Fingerprint {
		return fmt.Errorf("%w: key %s", ErrKeyConflict, rec.Key)
	}
	if _, err := t.Exec(`UPDATE idempotency
		SET intent_id = coalesce(intent_id, ?), result = coalesce(result, ?)
		WHERE key = ?`, nullID(rec.IntentID), nullJSON(rec.Result), rec.Key); err != nil {
		return fmt.Errorf("store: update key %s: %w", rec.Key, err)
	}
	return nil
}

// ExpireRunKeys sets the expiry of every key of runID that has none. The
// caller calls it when the run settles, with the settle time plus the CLI
// wait budget (KTD13).
func (t *Tx) ExpireRunKeys(runID string, at time.Time) (int64, error) {
	res, err := t.Exec(`UPDATE idempotency SET expires_at = ? WHERE run_id = ? AND expires_at IS NULL`, at.UnixMilli(), runID)
	if err != nil {
		return 0, fmt.Errorf("store: expire keys of run %s: %w", runID, err)
	}
	return res.RowsAffected()
}

// PurgeExpiredKeys deletes every key that expired at or before now and holds
// a result. A key whose intent is still in flight stays.
func (t *Tx) PurgeExpiredKeys(now time.Time) (int64, error) {
	res, err := t.Exec(`DELETE FROM idempotency WHERE expires_at <= ? AND result IS NOT NULL`, now.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: purge expired keys: %w", err)
	}
	return res.RowsAffected()
}

func (t *Tx) getKey(key string) (IdemRecord, error) {
	var (
		rec       = IdemRecord{Key: key}
		intentID  sql.NullInt64
		result    sql.NullString
		createdAt int64
		expiresAt sql.NullInt64
	)
	err := t.QueryRow(`SELECT run_id, verb, args_hash, content_hash, intent_id, result, created_at, expires_at
		FROM idempotency WHERE key = ?`, key).
		Scan(&rec.RunID, &rec.Verb, &rec.ArgsHash, &rec.ContentHash, &intentID, &result, &createdAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return IdemRecord{}, fmt.Errorf("%w: key %s", ErrNotFound, key)
	}
	if err != nil {
		return IdemRecord{}, fmt.Errorf("store: read key %s: %w", key, err)
	}
	rec.IntentID = intentID.Int64
	rec.Result = rawJSON(result)
	rec.CreatedAt = time.UnixMilli(createdAt)
	rec.ExpiresAt = fromMillis(expiresAt)
	return rec, nil
}

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id != 0} }

// nullJSON stores nil as NULL and anything else as TEXT.
func nullJSON(v json.RawMessage) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(v), Valid: true}
}

// jsonOrEmpty stores nil or empty as an empty JSON object.
func jsonOrEmpty(v json.RawMessage) string {
	if len(v) == 0 {
		return "{}"
	}
	return string(v)
}

func rawJSON(v sql.NullString) json.RawMessage {
	if !v.Valid {
		return nil
	}
	return json.RawMessage(v.String)
}
