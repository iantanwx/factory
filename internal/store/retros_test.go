package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// C4: the migration that adds retros creates the table empty, so an epic
// that reached done and a bug that closed before it get no retro.
func TestRetrosMigrationQueuesNoRetroForFinishedItems(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loop.db")
	before := fstest.MapFS{}
	ms, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms[:14] {
		before[m.name] = file(m.sql)
	}
	s, err := openWith(ctx, path, before)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	db := rawDB(t, path)
	if _, err := db.Exec(`INSERT INTO transitions (intent_id, project, item, seq, kind, verb, from_status, to_status, epoch, at) VALUES
		(1, 'alpha', 'tk-1', 1, 'epic', 'complete', 'execution', 'done', 1, 0),
		(2, 'alpha', 'tk-2', 1, 'bug', 'cancel', 'reproducing', 'canceled', 1, 0),
		(3, 'alpha', 'tk-3', 1, 'bug', 'shape', 'reproducing', 'shaped', 1, 0),
		(4, 'alpha', 'tk-4', 1, 'ticket', 'merged', 'in_review', 'done', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var n int
	if err := s.Read(ctx, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT count(*) FROM retros`).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("retros holds %d rows after the migration, want none", n)
	}
}
