package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// IntentState is the life of one intent (KTD6).
type IntentState string

const (
	IntentPending IntentState = "pending" // recorded, not yet applied to beads
	IntentApplied IntentState = "applied" // the bead holds the write
	IntentRefused IntentState = "refused" // beads refused the write, for example a stale epoch
)

// Intent is one recorded write to one item (KTD6 step 1).
type Intent struct {
	ID             int64 // global intent order
	Project        string
	Item           string
	Seq            int64 // the item's intent sequence; set by RecordIntent
	Kind           string
	Verb           string
	IdemKey        string // empty when the write has no idempotency key
	ExpectedStatus string
	TargetStatus   string
	Facts          json.RawMessage
	Epoch          int64
	State          IntentState
	Result         json.RawMessage
	RunID          string
	Actor          string
	CreatedAt      time.Time
	SettledAt      time.Time
}

// OutboxState is the life of one outbox effect.
type OutboxState string

const (
	OutboxHeld      OutboxState = "held"      // waits for its intent to be applied
	OutboxRunnable  OutboxState = "runnable"  // may run at or after NextAttemptAt
	OutboxDone      OutboxState = "done"      // the effect happened
	OutboxFailed    OutboxState = "failed"    // the effect gave up
	OutboxCancelled OutboxState = "cancelled" // its intent was refused; frees the natural key
)

// OutboxRow is one GitHub effect, idempotent by its natural key (KTD6).
type OutboxRow struct {
	ID            int64
	IntentID      int64
	EffectKind    string // for example "pr", "status" or "comment"
	NaturalKey    string // for example the head branch, sha and context, or the marker id
	Payload       json.RawMessage
	State         OutboxState
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CursorAdvance moves one reactor cursor in the same transaction as an
// intent (KTD6 step 1).
type CursorAdvance struct {
	Project string
	Stream  string
	Cursor  string
}

// RecordIntent records in, its outbox rows and an optional cursor advance in
// this transaction. It allocates the item's next intent sequence here, so a
// rolled-back write consumes none. It returns the stored intent. It refuses
// a second unapplied intent for one item with ErrIntentInFlight, an intent
// key that another intent holds with ErrKeyConflict, and a natural key that
// a live outbox row holds with ErrEffectExists.
func (t *Tx) RecordIntent(in Intent, outbox []OutboxRow, cursor *CursorAdvance) (Intent, error) {
	if in.Project == "" || in.Item == "" || in.Kind == "" || in.Verb == "" {
		return Intent{}, fmt.Errorf("store: intent needs a project, an item, a kind and a verb")
	}
	if in.Epoch < 0 {
		return Intent{}, fmt.Errorf("store: intent epoch %d is negative", in.Epoch)
	}
	if err := t.checkIntent(in, outbox); err != nil {
		return Intent{}, err
	}

	var seq int64
	err := t.QueryRow(`INSERT INTO item_seq (project, item, last_seq) VALUES (?, ?, 1)
		ON CONFLICT (project, item) DO UPDATE SET last_seq = last_seq + 1
		RETURNING last_seq`, in.Project, in.Item).Scan(&seq)
	if err != nil {
		return Intent{}, fmt.Errorf("store: allocate intent sequence for %s/%s: %w", in.Project, in.Item, err)
	}

	now := t.now.UnixMilli()
	res, err := t.Exec(`INSERT INTO intents
		(project, item, seq, kind, verb, idem_key, expected_status, target_status, facts, epoch, state, run_id, actor, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)`,
		in.Project, in.Item, seq, in.Kind, in.Verb, sql.NullString{String: in.IdemKey, Valid: in.IdemKey != ""},
		in.ExpectedStatus, in.TargetStatus, jsonOrEmpty(in.Facts), in.Epoch, in.RunID, in.Actor, now)
	if err != nil {
		return Intent{}, fmt.Errorf("store: record intent for %s/%s: %w", in.Project, in.Item, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Intent{}, err
	}

	for _, ob := range outbox {
		if _, err := t.Exec(`INSERT INTO outbox
			(intent_id, effect_kind, natural_key, payload, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'held', ?, ?)`,
			id, ob.EffectKind, ob.NaturalKey, jsonOrEmpty(ob.Payload), now, now); err != nil {
			return Intent{}, fmt.Errorf("store: record outbox %s %s: %w", ob.EffectKind, ob.NaturalKey, err)
		}
	}

	if cursor != nil {
		if cursor.Project == "" || cursor.Stream == "" {
			return Intent{}, fmt.Errorf("store: cursor advance needs a project and a stream")
		}
		if _, err := t.Exec(`INSERT INTO github_cursors (project, stream, cursor, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (project, stream) DO UPDATE SET cursor = excluded.cursor, updated_at = excluded.updated_at`,
			cursor.Project, cursor.Stream, cursor.Cursor, now); err != nil {
			return Intent{}, fmt.Errorf("store: advance cursor %s/%s: %w", cursor.Project, cursor.Stream, err)
		}
	}
	return t.GetIntent(id)
}

// checkIntent runs every refusal before RecordIntent writes, so a caller
// that handles the refusal and commits leaves no partial row.
func (t *Tx) checkIntent(in Intent, outbox []OutboxRow) error {
	var n int
	if err := t.QueryRow(`SELECT count(*) FROM intents WHERE project = ? AND item = ? AND state = 'pending'`,
		in.Project, in.Item).Scan(&n); err != nil {
		return fmt.Errorf("store: check pending intents: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("%w: %s/%s", ErrIntentInFlight, in.Project, in.Item)
	}
	if in.IdemKey != "" {
		if err := t.QueryRow(`SELECT count(*) FROM intents WHERE idem_key = ?`, in.IdemKey).Scan(&n); err != nil {
			return fmt.Errorf("store: check intent key: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("%w: another intent holds key %s", ErrKeyConflict, in.IdemKey)
		}
	}
	seen := make(map[[2]string]bool, len(outbox))
	for _, ob := range outbox {
		if ob.EffectKind == "" || ob.NaturalKey == "" {
			return fmt.Errorf("store: outbox row needs an effect kind and a natural key")
		}
		k := [2]string{ob.EffectKind, ob.NaturalKey}
		if seen[k] {
			return fmt.Errorf("%w: %s %s appears twice in one intent", ErrEffectExists, ob.EffectKind, ob.NaturalKey)
		}
		seen[k] = true
		if err := t.QueryRow(`SELECT count(*) FROM outbox WHERE effect_kind = ? AND natural_key = ? AND state <> 'cancelled'`,
			ob.EffectKind, ob.NaturalKey).Scan(&n); err != nil {
			return fmt.Errorf("store: check outbox key: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("%w: %s %s", ErrEffectExists, ob.EffectKind, ob.NaturalKey)
		}
	}
	return nil
}

// MarkApplied settles a pending intent as applied (KTD6 step 3). It stores
// result on the intent and under its idempotency key, and it makes the
// intent's held outbox rows runnable. A nil result is stored as {}.
func (t *Tx) MarkApplied(intentID int64, result json.RawMessage) error {
	return t.settle(intentID, IntentApplied, OutboxRunnable, result)
}

// MarkRefused settles a pending intent as refused. It stores result on the
// intent and under its idempotency key, so a replay returns the refusal, and
// it cancels the intent's held outbox rows. A nil result is stored as {}.
func (t *Tx) MarkRefused(intentID int64, result json.RawMessage) error {
	return t.settle(intentID, IntentRefused, OutboxCancelled, result)
}

func (t *Tx) settle(intentID int64, to IntentState, outboxTo OutboxState, result json.RawMessage) error {
	var state string
	err := t.QueryRow(`SELECT state FROM intents WHERE id = ?`, intentID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: intent %d", ErrNotFound, intentID)
	}
	if err != nil {
		return fmt.Errorf("store: read intent %d: %w", intentID, err)
	}
	if IntentState(state) != IntentPending {
		return fmt.Errorf("%w: intent %d is %s", ErrIntentSettled, intentID, state)
	}
	body, now := jsonOrEmpty(result), t.now.UnixMilli()
	if _, err := t.Exec(`UPDATE intents SET state = ?, result = ?, settled_at = ? WHERE id = ?`,
		string(to), body, now, intentID); err != nil {
		return fmt.Errorf("store: settle intent %d: %w", intentID, err)
	}
	if _, err := t.Exec(`UPDATE outbox SET state = ?, updated_at = ? WHERE intent_id = ? AND state = 'held'`,
		string(outboxTo), now, intentID); err != nil {
		return fmt.Errorf("store: release outbox of intent %d: %w", intentID, err)
	}
	if _, err := t.Exec(`UPDATE idempotency SET result = ? WHERE intent_id = ? AND result IS NULL`,
		body, intentID); err != nil {
		return fmt.Errorf("store: store result of intent %d: %w", intentID, err)
	}
	return nil
}

const intentColumns = `id, project, item, seq, kind, verb, idem_key, expected_status, target_status,
	facts, epoch, state, result, run_id, actor, created_at, settled_at`

// Scanner is one row to scan: a *sql.Row or a *sql.Rows.
type Scanner interface{ Scan(dest ...any) error }

// Querier runs a query that returns rows: a *sql.DB or a *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func scanIntent(r Scanner) (Intent, error) {
	var (
		in        Intent
		idemKey   sql.NullString
		facts     string
		state     string
		result    sql.NullString
		createdAt int64
		settledAt sql.NullInt64
	)
	err := r.Scan(&in.ID, &in.Project, &in.Item, &in.Seq, &in.Kind, &in.Verb, &idemKey, &in.ExpectedStatus,
		&in.TargetStatus, &facts, &in.Epoch, &state, &result, &in.RunID, &in.Actor, &createdAt, &settledAt)
	if err != nil {
		return Intent{}, err
	}
	in.IdemKey = idemKey.String
	in.Facts = json.RawMessage(facts)
	in.State = IntentState(state)
	in.Result = rawJSON(result)
	in.CreatedAt = time.UnixMilli(createdAt)
	in.SettledAt = fromMillis(settledAt)
	return in, nil
}

// GetIntent returns one intent, or an error that matches ErrNotFound.
func (t *Tx) GetIntent(id int64) (Intent, error) {
	in, err := scanIntent(t.QueryRow(`SELECT `+intentColumns+` FROM intents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Intent{}, fmt.Errorf("%w: intent %d", ErrNotFound, id)
	}
	if err != nil {
		return Intent{}, fmt.Errorf("store: read intent %d: %w", id, err)
	}
	return in, nil
}

// UnappliedIntents returns every pending intent in intent order, the order
// that reconcile replays them in (KTD6).
func (t *Tx) UnappliedIntents() ([]Intent, error) {
	rows, err := t.Query(`SELECT ` + intentColumns + ` FROM intents WHERE state = 'pending' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list unapplied intents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Intent
	for rows.Next() {
		in, err := scanIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan intent: %w", err)
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// ItemSeq returns the item's last intent sequence, or 0 when the item has
// none.
func (t *Tx) ItemSeq(project, item string) (int64, error) {
	var seq int64
	err := t.QueryRow(`SELECT last_seq FROM item_seq WHERE project = ? AND item = ?`, project, item).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: read intent sequence of %s/%s: %w", project, item, err)
	}
	return seq, nil
}

// RaiseItemSeq raises the item's last intent sequence to atLeast. It never
// lowers it. Reconcile uses it when a bead holds a higher sequence than
// SQLite.
func (t *Tx) RaiseItemSeq(project, item string, atLeast int64) error {
	if atLeast < 0 {
		return fmt.Errorf("store: intent sequence %d is negative", atLeast)
	}
	_, err := t.Exec(`INSERT INTO item_seq (project, item, last_seq) VALUES (?, ?, ?)
		ON CONFLICT (project, item) DO UPDATE SET last_seq = max(last_seq, excluded.last_seq)`, project, item, atLeast)
	if err != nil {
		return fmt.Errorf("store: raise intent sequence of %s/%s: %w", project, item, err)
	}
	return nil
}

const outboxColumns = `id, intent_id, effect_kind, natural_key, payload, state, attempts, next_attempt_at,
	last_error, created_at, updated_at`

func scanOutbox(r Scanner) (OutboxRow, error) {
	var (
		ob                        OutboxRow
		payload, state            string
		next, createdAt, updateAt int64
	)
	err := r.Scan(&ob.ID, &ob.IntentID, &ob.EffectKind, &ob.NaturalKey, &payload, &state, &ob.Attempts, &next,
		&ob.LastError, &createdAt, &updateAt)
	if err != nil {
		return OutboxRow{}, err
	}
	ob.Payload = json.RawMessage(payload)
	ob.State = OutboxState(state)
	if next != 0 {
		ob.NextAttemptAt = time.UnixMilli(next)
	}
	ob.CreatedAt = time.UnixMilli(createdAt)
	ob.UpdatedAt = time.UnixMilli(updateAt)
	return ob, nil
}

func (t *Tx) listOutbox(query string, args ...any) ([]OutboxRow, error) {
	rows, err := t.Query(`SELECT `+outboxColumns+` FROM outbox `+query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list outbox: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []OutboxRow
	for rows.Next() {
		ob, err := scanOutbox(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan outbox: %w", err)
		}
		out = append(out, ob)
	}
	return out, rows.Err()
}

// OutboxFor returns every outbox row of one intent in record order.
func (t *Tx) OutboxFor(intentID int64) ([]OutboxRow, error) {
	return t.listOutbox(`WHERE intent_id = ? ORDER BY id`, intentID)
}

// RunnableOutbox returns up to limit runnable rows whose next attempt is due
// at the transaction's time, oldest first.
func (t *Tx) RunnableOutbox(limit int) ([]OutboxRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	return t.listOutbox(`WHERE state = 'runnable' AND next_attempt_at <= ? ORDER BY id LIMIT ?`, t.now.UnixMilli(), limit)
}

// MarkEffectDone records that a runnable effect happened.
func (t *Tx) MarkEffectDone(id int64) error {
	return t.updateRunnable(id, `state = 'done', attempts = attempts + 1, last_error = ''`)
}

// MarkEffectRetry records a failed attempt and keeps the effect runnable
// from next.
func (t *Tx) MarkEffectRetry(id int64, next time.Time, errText string) error {
	return t.updateRunnable(id, `attempts = attempts + 1, next_attempt_at = ?, last_error = ?`, next.UnixMilli(), errText)
}

// MarkEffectFailed records a final failed attempt. The effect never runs
// again.
func (t *Tx) MarkEffectFailed(id int64, errText string) error {
	return t.updateRunnable(id, `state = 'failed', attempts = attempts + 1, last_error = ?`, errText)
}

// updateRunnable applies set to one runnable outbox row. Any other row
// returns an error that matches ErrNotFound.
func (t *Tx) updateRunnable(id int64, set string, args ...any) error {
	args = append(args, t.now.UnixMilli(), id)
	res, err := t.Exec(`UPDATE outbox SET `+set+`, updated_at = ? WHERE id = ? AND state = 'runnable'`, args...)
	if err != nil {
		return fmt.Errorf("store: update outbox %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%w: no runnable outbox row %d", ErrNotFound, id)
	}
	return nil
}
