package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// Migration 0014 records the checkout name of each room opened before it:
// loop-<room id>, the name that room's checkout was given. So the cleanup
// pass still keeps the checkout of a room opened before the upgrade
// (factorio-pwpi).
func TestMigrationRecordsTheCheckoutNameOfAnEarlierRoom(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loop.db")
	upTo13 := fstest.MapFS{}
	ms, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms[:13] {
		upTo13[m.name] = file(m.sql)
	}
	s, err := openWith(ctx, path, upTo13)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	db := rawDB(t, path)
	if _, err := db.Exec(`INSERT INTO rooms (id, project, state, opened_at, kind) VALUES ('room-1a2b3c4d', 'alpha', 'opening', 0, 'intake')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var name string
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT checkout_name FROM rooms WHERE id = 'room-1a2b3c4d'`).Scan(&name)
	}); err != nil {
		t.Fatal(err)
	}
	if name != "loop-room-1a2b3c4d" {
		t.Errorf("checkout name of the earlier room = %q, want loop-room-1a2b3c4d", name)
	}
}
