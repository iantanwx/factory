package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
)

// Snapshot writes a consistent copy of the database to dst with VACUUM INTO
// (KTD5). It runs on its own connection inside one read transaction, so the
// writer keeps writing, and the copy holds every row committed before the
// snapshot began. It refuses a dst that exists. The copy is synced to disk
// before Snapshot returns.
func (s *Store) Snapshot(ctx context.Context, dst string) (err error) {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("store: snapshot target %s exists", dst)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store: snapshot target %s: %w", dst, err)
	}
	dir := filepath.Dir(dst)
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(dst)+".tmp-"+hex.EncodeToString(suffix))
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	// The readers are query_only, and SQLite refuses VACUUM INTO there.
	q := url.Values{}
	basePragmas(q)
	db, err := sql.Open(driverName, s.path+"?"+q.Encode())
	if err != nil {
		return fmt.Errorf("store: open snapshot connection: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return fmt.Errorf("store: vacuum into %s: %w", tmp, err)
	}

	if err := syncFile(tmp); err != nil {
		return err
	}
	// Link, then remove: unlike a rename, a link never replaces a dst that
	// appeared after the check above.
	if err := os.Link(tmp, dst); err != nil {
		return fmt.Errorf("store: place snapshot %s: %w", dst, err)
	}
	if err := os.Remove(tmp); err != nil {
		return fmt.Errorf("store: remove %s: %w", tmp, err)
	}
	return syncFile(dir)
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("store: open %s for sync: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("store: sync %s: %w", path, err)
	}
	return nil
}
