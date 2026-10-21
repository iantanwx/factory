package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
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
