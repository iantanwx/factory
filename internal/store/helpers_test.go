package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// openTemp opens a fresh store in a test directory and closes it at cleanup.
func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// rawDB opens path with no store settings, so a test can forge state.
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT count(*) FROM " + table).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func addProject(t *testing.T, s *Store, id string) {
	t.Helper()
	err := s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO projects (id, repo, created_at, updated_at) VALUES (?, ?, 0, 0)`, id, "git@example.com:"+id)
		return err
	})
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
}

// outboxFor returns every outbox row of one intent in record order.
func outboxFor(tx *Tx, intentID int64) ([]OutboxRow, error) {
	return listOutbox(tx, `WHERE intent_id = ? ORDER BY id`, intentID)
}

// runnableOutbox returns up to limit runnable rows whose next attempt is
// due at the transaction's time, oldest first, as an effect runner reads
// them.
func runnableOutbox(tx *Tx, limit int) ([]OutboxRow, error) {
	return listOutbox(tx, `WHERE state = 'runnable' AND next_attempt_at <= ? ORDER BY id LIMIT ?`, tx.Now().UnixMilli(), limit)
}

func listOutbox(tx *Tx, where string, args ...any) ([]OutboxRow, error) {
	rows, err := tx.Query(`SELECT id, intent_id, effect_kind, natural_key, payload, state, attempts, next_attempt_at,
		last_error, created_at, updated_at FROM outbox `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OutboxRow
	for rows.Next() {
		var (
			ob                        OutboxRow
			payload, state            string
			next, createdAt, updateAt int64
		)
		if err := rows.Scan(&ob.ID, &ob.IntentID, &ob.EffectKind, &ob.NaturalKey, &payload, &state, &ob.Attempts, &next,
			&ob.LastError, &createdAt, &updateAt); err != nil {
			return nil, err
		}
		ob.Payload = json.RawMessage(payload)
		ob.State = OutboxState(state)
		if next != 0 {
			ob.NextAttemptAt = time.UnixMilli(next)
		}
		ob.CreatedAt = time.UnixMilli(createdAt)
		ob.UpdatedAt = time.UnixMilli(updateAt)
		out = append(out, ob)
	}
	return out, rows.Err()
}
