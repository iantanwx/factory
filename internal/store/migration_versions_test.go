package store

import (
	"context"
	"path/filepath"
	"testing"
)

// The migrations this binary embeds run 1..N with one file per version,
// so a fresh database opens.
func TestEmbeddedMigrationsOpenAFreshDatabase(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ms, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.SchemaVersion(), len(ms); got != want {
		t.Fatalf("SchemaVersion = %d, want %d (one per embedded migration)", got, want)
	}
}
