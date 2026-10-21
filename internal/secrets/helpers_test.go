package secrets

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

// testEnv is a store with two registered projects and a host key file.
type testEnv struct {
	db      *store.Store
	secrets *Store
	keys    *FileKeyProvider
	keyPath string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "loop.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, p := range []config.Project{
		{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/alpha"},
		{ID: "beta", Repo: "iantanwx/beta", Path: "/srv/beta"},
	} {
		if err := config.StoreProjectRow(db)(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	keyPath := filepath.Join(dir, "keys", "kek")
	if err := EnsureKeyFile(keyPath); err != nil {
		t.Fatalf("EnsureKeyFile: %v", err)
	}
	keys, err := NewFileKeyProvider(keyPath)
	if err != nil {
		t.Fatalf("NewFileKeyProvider: %v", err)
	}
	return &testEnv{db: db, secrets: New(db, keys), keys: keys, keyPath: keyPath}
}

func (e *testEnv) set(t *testing.T, project, name, value string) {
	t.Helper()
	if err := e.secrets.Set(context.Background(), project, name, strings.NewReader(value)); err != nil {
		t.Fatalf("Set %s/%s: %v", project, name, err)
	}
}

func (e *testEnv) get(t *testing.T, project, name string) string {
	t.Helper()
	v, err := e.secrets.Get(context.Background(), project, name)
	if err != nil {
		t.Fatalf("Get %s/%s: %v", project, name, err)
	}
	return string(v)
}

func (e *testEnv) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	err := e.db.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	})
	if err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
