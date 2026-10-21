package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// embeddedMigrations holds the forward-only migrations. The binary's schema
// version is the highest one.
//
//go:embed migrations/*.sql
var embeddedFS embed.FS

var embeddedMigrations = mustSub(embeddedFS, "migrations")

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

type migration struct {
	version   int
	name      string
	minReader int
	sql       string
}

var (
	migrationName   = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)
	minReaderHeader = regexp.MustCompile(`^--\s*min_reader_schema:\s*(\d+)\s*$`)
)

// loadMigrations reads NNNN_name.sql files from the root of fsys. Versions
// must run 1..N with no gap. Each file's first line must be
// "-- min_reader_schema: M" with 1 <= M <= its own version.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var ms []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("store: migration file %q is not named NNNN_name.sql", e.Name())
		}
		version, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, path.Clean(e.Name()))
		if err != nil {
			return nil, fmt.Errorf("store: read migration %s: %w", e.Name(), err)
		}
		first, _, _ := strings.Cut(string(body), "\n")
		h := minReaderHeader.FindStringSubmatch(strings.TrimSpace(first))
		if h == nil {
			return nil, fmt.Errorf("store: migration %s must start with \"-- min_reader_schema: N\"", e.Name())
		}
		minReader, _ := strconv.Atoi(h[1])
		if minReader < 1 || minReader > version {
			return nil, fmt.Errorf("store: migration %s has min_reader_schema %d; want 1..%d", e.Name(), minReader, version)
		}
		ms = append(ms, migration{version: version, name: e.Name(), minReader: minReader, sql: string(body)})
	}
	if len(ms) == 0 {
		return nil, fmt.Errorf("store: no migrations")
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migration %s has version %d; want %d (no gap, no duplicate)", m.name, m.version, i+1)
		}
	}
	return ms, nil
}

const schemaMetaDDL = `CREATE TABLE IF NOT EXISTS schema_meta (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    min_reader_schema INTEGER NOT NULL
)`

// migrate applies each pending migration in its own BEGIN IMMEDIATE
// transaction and returns the database's user_version afterwards. It
// refuses a database whose minimum reader schema is above the binary's.
func migrate(ctx context.Context, db *sql.DB, dbPath string, ms []migration) (int, error) {
	binary := ms[len(ms)-1].version
	for {
		applied, done, err := migrateOne(ctx, db, dbPath, ms, binary)
		if err != nil || done {
			return applied, err
		}
	}
}

// migrateOne checks the database inside one write transaction and applies
// the next migration, if any. The check runs under the write lock, so a
// second process that opens the same file at the same time sees the result.
func migrateOne(ctx context.Context, db *sql.DB, dbPath string, ms []migration, binary int) (version int, done bool, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("store: begin migration: %w", err)
	}
	defer func() {
		if err != nil || done {
			_ = tx.Rollback()
		}
	}()

	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, false, fmt.Errorf("store: read user_version: %w", err)
	}
	minReader, err := readMinReader(ctx, tx)
	if err != nil {
		return 0, false, err
	}
	if minReader > binary {
		return version, false, &SchemaTooNewError{Path: dbPath, DBSchema: version, MinReaderSchema: minReader, BinarySchema: binary}
	}
	if version >= binary {
		return version, true, nil
	}

	m := ms[version] // versions run 1..N, so ms[version] is version+1
	if _, err := tx.ExecContext(ctx, schemaMetaDDL); err != nil {
		return 0, false, fmt.Errorf("store: create schema_meta: %w", err)
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return 0, false, fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_meta (id, min_reader_schema) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET min_reader_schema = max(min_reader_schema, excluded.min_reader_schema)`, m.minReader); err != nil {
		return 0, false, fmt.Errorf("store: migration %s: record min reader schema: %w", m.name, err)
	}
	// PRAGMA takes no bound parameter. m.version is an int.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return 0, false, fmt.Errorf("store: migration %s: set user_version: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("store: commit migration %s: %w", m.name, err)
	}
	return m.version, false, nil
}

func readMinReader(ctx context.Context, tx *sql.Tx) (int, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_meta'`).Scan(&exists); err != nil {
		return 0, fmt.Errorf("store: look for schema_meta: %w", err)
	}
	if exists == 0 {
		return 0, nil
	}
	var minReader int
	err := tx.QueryRowContext(ctx, `SELECT min_reader_schema FROM schema_meta WHERE id = 1`).Scan(&minReader)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: read schema_meta: %w", err)
	}
	return minReader, nil
}
