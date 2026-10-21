package release

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
)

// Restore runs `loop restore [<snapshot>]` (KTD26) with no manual step. It
// needs the host-admin credential file, and it never waits on a server
// that is down.
type Restore struct {
	Host
}

// RestoreReport is the JSON of `loop restore`.
type RestoreReport struct {
	Snapshot string      `json:"snapshot"`
	Drain    DrainReport `json:"drain"`
	// SecretsSource is "current" when the restored database holds the
	// replaced database's secret table, and "snapshot" when the replaced
	// database could not be read.
	SecretsSource string `json:"secrets_source"`
	SecretsNote   string `json:"secrets_note,omitempty"`
	// Kept is the replaced database.
	Kept    string `json:"kept,omitempty"`
	Started bool   `json:"started"`
}

// Secrets sources.
const (
	SecretsFromCurrent  = "current"
	SecretsFromSnapshot = "snapshot"
)

// Run restores the named snapshot, or the newest one when name is empty.
// It drains a reachable server, stops the service, carries the current
// secret table, lease epochs and token revocations into the snapshot's
// copy, keeps the replaced database as loop.db.before-restore, swaps the
// files and starts the service. The server's start-up reconcile then
// raises every epoch to the bead's (KTD6).
func (r *Restore) Run(ctx context.Context, name string) (RestoreReport, error) {
	var rep RestoreReport
	if _, err := cli.ReadAdminCredential(r.Layout.Dir); err != nil {
		return rep, refuse(cli.CodeCapabilityDenied, "admin_credential", "loop restore needs the host-admin credential file %s: %v",
			cli.AdminCredentialPath(r.Layout.Dir), err)
	}
	snap, err := r.pick(name)
	if err != nil {
		return rep, err
	}
	rep.Snapshot = snap
	db := r.Layout.DB()
	tmp := db + ".restore"
	removeDB(tmp)
	if err := copyFile(snap, tmp); err != nil {
		return rep, err
	}
	if err := checkDatabase(ctx, tmp); err != nil {
		removeDB(tmp)
		return rep, refuse(cli.CodeNotFound, "snapshot_unreadable", "the snapshot %s is not a readable database: %v", snap, err)
	}

	if rep.Drain, err = r.drain(ctx); err != nil {
		removeDB(tmp)
		return rep, err
	}
	lock, err := r.stop(ctx)
	if err != nil {
		removeDB(tmp)
		return rep, err
	}
	if why := checkpoint(ctx, db); why != "" {
		rep.SecretsSource = SecretsFromSnapshot
		rep.SecretsNote = "the replaced database could not be read (" + why + "), so the restored database holds the snapshot's secret table; check every secret rotated after the snapshot"
	} else {
		if err := merge(ctx, tmp, db, r.now().UnixMilli()); err != nil {
			removeDB(tmp)
			return rep, r.abort(ctx, lock, err)
		}
		rep.SecretsSource = SecretsFromCurrent
	}
	if rep.Kept, err = r.swap(db, tmp); err != nil {
		return rep, r.abort(ctx, lock, err)
	}
	if err := r.start(ctx, lock); err != nil {
		return rep, err
	}
	rep.Started = true
	return rep, nil
}

// pick finds the snapshot: a name in snapshots/, an absolute path, or the
// newest snapshot when name is empty.
func (r *Restore) pick(name string) (string, error) {
	dir := r.Layout.Snapshots()
	if name != "" {
		path := name
		if !filepath.IsAbs(name) {
			if strings.ContainsRune(name, filepath.Separator) {
				return "", refuse(cli.CodeUsage, "bad_snapshot", "name a snapshot in %s, or give an absolute path", dir)
			}
			path = filepath.Join(dir, name)
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return "", refuse(cli.CodeNotFound, "no_snapshot", "no snapshot %s", path)
		}
		return path, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	type cand struct {
		path string
		mod  int64
	}
	var cs []cand
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		cs = append(cs, cand{filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
	}
	if len(cs) == 0 {
		return "", refuse(cli.CodeNotFound, "no_snapshot", "%s holds no snapshot", dir)
	}
	newest := slices.MaxFunc(cs, func(a, b cand) int {
		if a.mod != b.mod {
			return compareInt(a.mod, b.mod)
		}
		return strings.Compare(a.path, b.path)
	})
	return newest.path, nil
}

func compareInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// openOne opens one connection to a database file.
func openOne(ctx context.Context, path string) (*sql.DB, *sql.Conn, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, conn, nil
}

func closeOne(db *sql.DB, conn *sql.Conn) error {
	return errors.Join(conn.Close(), db.Close())
}

// checkDatabase runs SQLite's quick check.
func checkDatabase(ctx context.Context, path string) error {
	db, conn, err := openOne(ctx, path)
	if err != nil {
		return err
	}
	var res string
	err = conn.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&res)
	if err == nil && res != "ok" {
		err = errors.New(res)
	}
	if err == nil {
		var n int
		err = conn.QueryRowContext(ctx, `SELECT count(*) FROM secrets`).Scan(&n)
	}
	return errors.Join(err, closeOne(db, conn))
}

// checkpoint moves the WAL of the replaced database into its main file and
// proves it readable. It returns why the database cannot be read, or "".
func checkpoint(ctx context.Context, path string) string {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "it is missing"
	}
	db, conn, err := openOne(ctx, path)
	if err != nil {
		return err.Error()
	}
	var busy, logged, done, n int
	err = conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logged, &done)
	if err == nil {
		err = conn.QueryRowContext(ctx, `SELECT count(*) FROM secrets`).Scan(&n)
	}
	if err = errors.Join(err, closeOne(db, conn)); err != nil {
		return err.Error()
	}
	return ""
}

// merge carries state that must never go back into the snapshot's copy:
// the secret table, so a rotated secret keeps its new value under the
// current key; every lease epoch, so no epoch is reused; and every token
// revocation, so a settled run's token stays dead.
func merge(ctx context.Context, tmp, current string, now int64) (err error) {
	db, conn, err := openOne(ctx, tmp)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeOne(db, conn)) }()
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS cur`, current); err != nil {
		return fmt.Errorf("release: attach the replaced database: %w", err)
	}
	defer func() {
		if _, derr := conn.ExecContext(context.Background(), `DETACH DATABASE cur`); derr != nil {
			err = errors.Join(err, derr)
		}
	}()
	hasTokens, err := bothHave(ctx, conn, "run_tokens")
	if err != nil {
		return err
	}
	stmts := []string{
		`DELETE FROM main.secrets`,
		`INSERT INTO main.projects (id, repo, config, created_at, updated_at)
			SELECT id, repo, config, created_at, updated_at FROM cur.projects c
			WHERE c.id IN (SELECT project FROM cur.secrets) AND c.id NOT IN (SELECT id FROM main.projects)`,
		`INSERT INTO main.secrets (project, name, key_id, key_version, wrapped_key, nonce, ciphertext, created_at, updated_at)
			SELECT project, name, key_id, key_version, wrapped_key, nonce, ciphertext, created_at, updated_at FROM cur.secrets`,
		`UPDATE main.leases SET epoch = (SELECT c.epoch + 1 FROM cur.leases c WHERE c.project = leases.project AND c.item = leases.item)
			WHERE EXISTS (SELECT 1 FROM cur.leases c WHERE c.project = leases.project AND c.item = leases.item AND c.epoch > leases.epoch)`,
		`INSERT INTO main.leases (project, item, epoch)
			SELECT project, item, epoch + 1 FROM cur.leases c
			WHERE NOT EXISTS (SELECT 1 FROM main.leases m WHERE m.project = c.project AND m.item = c.item)`,
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("release: carry state into the restored database: %w", err)
		}
	}
	if hasTokens {
		if _, err := tx.ExecContext(ctx, `UPDATE main.run_tokens SET revoked_at = ? WHERE revoked_at IS NULL AND NOT EXISTS
			(SELECT 1 FROM cur.run_tokens c WHERE c.token_hash = run_tokens.token_hash AND c.revoked_at IS NULL)`, now); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("release: carry token revocations into the restored database: %w", err)
		}
	}
	return tx.Commit()
}

func bothHave(ctx context.Context, conn *sql.Conn, table string) (bool, error) {
	var n int
	err := conn.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM main.sqlite_master WHERE type = 'table' AND name = ?)
		+ (SELECT count(*) FROM cur.sqlite_master WHERE type = 'table' AND name = ?)`, table, table).Scan(&n)
	return n == 2, err
}

// swap keeps the replaced database, with its WAL files, under the
// before-restore name, then renames the restored copy over the database.
func (r *Restore) swap(db, tmp string) (string, error) {
	if _, err := os.Lstat(db); errors.Is(err, fs.ErrNotExist) {
		return "", errors.Join(os.Rename(tmp, db), syncDir(filepath.Dir(db)))
	}
	kept := db + beforeRestore
	if _, err := os.Lstat(kept); err == nil {
		older := fmt.Sprintf("%s.%d", kept, r.now().UnixMilli())
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := renameIfExists(kept+suffix, older+suffix); err != nil {
				return "", err
			}
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := renameIfExists(db+suffix, kept+suffix); err != nil {
			return "", err
		}
	}
	if err := os.Link(db, kept); err != nil {
		return "", fmt.Errorf("release: keep the replaced database: %w", err)
	}
	if err := os.Rename(tmp, db); err != nil {
		return "", err
	}
	return kept, syncDir(filepath.Dir(db))
}

func renameIfExists(from, to string) error {
	err := os.Rename(from, to)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// removeDB removes a database file and its journal files.
func removeDB(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
