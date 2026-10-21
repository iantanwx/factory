// Package store holds the server's durable state in one SQLite database
// (R11, R16, KTD5). It owns storage and transactions only. Business rules,
// such as table rows, compare-and-set and lease epochs, live in the callers.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; pure Go, so cgo stays off
)

const (
	driverName = "sqlite"
	// busyTimeoutMS bounds how long a connection waits on another
	// connection's lock before SQLITE_BUSY (KTD5).
	busyTimeoutMS = 5000
	// journalSizeLimit caps the WAL file that a checkpoint leaves behind.
	journalSizeLimit = 64 << 20
	// readerConns is the size of the reader pool.
	readerConns = 4
)

// Store is the server's SQLite database: one writer connection that runs
// every write in BEGIN IMMEDIATE, and a pool of query-only readers.
type Store struct {
	path     string
	writer   *sql.DB
	reader   *sql.DB
	schema   int // the highest migration this binary embeds
	dbSchema int // the database's user_version after Open
	now      func() time.Time
}

// Open opens or creates the database at path, applies every pending
// migration and verifies the connection settings. It refuses a database
// whose minimum reader schema is above this binary's schema with an error
// that matches ErrSchemaTooNew.
func Open(ctx context.Context, path string) (*Store, error) {
	return openWith(ctx, path, embeddedMigrations)
}

func openWith(ctx context.Context, path string, migrations fs.FS) (_ *Store, err error) {
	if path == "" || strings.ContainsAny(path, "?#") || strings.HasPrefix(path, "file:") || path == ":memory:" {
		return nil, fmt.Errorf("store: path %q must be a plain file path", path)
	}
	ms, err := loadMigrations(migrations)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, schema: ms[len(ms)-1].version, now: time.Now}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()

	s.writer, err = sql.Open(driverName, path+"?"+writerParams().Encode())
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	// One connection that never expires: every write queues for it, so two
	// writers in this process never meet SQLITE_BUSY.
	s.writer.SetMaxOpenConns(1)
	s.writer.SetMaxIdleConns(1)
	s.writer.SetConnMaxLifetime(0)
	s.writer.SetConnMaxIdleTime(0)
	if err := verifyWriter(ctx, s.writer); err != nil {
		return nil, err
	}
	if s.dbSchema, err = migrate(ctx, s.writer, path, ms); err != nil {
		return nil, err
	}

	// The reader opens after the writer set WAL, which is persistent.
	s.reader, err = sql.Open(driverName, path+"?"+readerParams().Encode())
	if err != nil {
		return nil, fmt.Errorf("store: open reader: %w", err)
	}
	s.reader.SetMaxOpenConns(readerConns)
	s.reader.SetMaxIdleConns(readerConns)
	if err := verifyReader(ctx, s.reader); err != nil {
		return nil, err
	}
	return s, nil
}

func basePragmas(q url.Values) {
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	q.Add("_pragma", "foreign_keys(ON)")
}

func writerParams() url.Values {
	q := url.Values{}
	basePragmas(q)
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", fmt.Sprintf("journal_size_limit(%d)", journalSizeLimit))
	// database/sql's BeginTx then issues BEGIN IMMEDIATE, so a write takes
	// the write lock at its start and never upgrades a read lock later.
	q.Set("_txlock", "immediate")
	return q
}

func readerParams() url.Values {
	q := url.Values{}
	basePragmas(q)
	q.Add("_pragma", "query_only(ON)")
	return q
}

type pragmaWant struct {
	name string
	want string
}

func checkPragmas(ctx context.Context, conn *sql.Conn, role string, wants []pragmaWant) error {
	for _, w := range wants {
		var got string
		if err := conn.QueryRowContext(ctx, "PRAGMA "+w.name).Scan(&got); err != nil {
			return fmt.Errorf("store: read %s pragma %s: %w", role, w.name, err)
		}
		if !strings.EqualFold(got, w.want) {
			return fmt.Errorf("store: %s pragma %s = %s, want %s", role, w.name, got, w.want)
		}
	}
	return nil
}

func verifyWriter(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: connect writer: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return checkPragmas(ctx, conn, "writer", []pragmaWant{
		{"journal_mode", "wal"},
		{"synchronous", "2"}, // FULL
		{"foreign_keys", "1"},
		{"busy_timeout", fmt.Sprint(busyTimeoutMS)},
		{"journal_size_limit", fmt.Sprint(journalSizeLimit)},
	})
}

func verifyReader(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: connect reader: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return checkPragmas(ctx, conn, "reader", []pragmaWant{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", fmt.Sprint(busyTimeoutMS)},
		{"query_only", "1"},
	})
}

// Close closes both connection pools.
func (s *Store) Close() error {
	var errs []error
	if s.reader != nil {
		errs = append(errs, s.reader.Close())
	}
	if s.writer != nil {
		errs = append(errs, s.writer.Close())
	}
	return errors.Join(errs...)
}

// SchemaVersion is the highest migration this binary embeds.
func (s *Store) SchemaVersion() int { return s.schema }

// DatabaseSchema is the database's user_version after Open. It is above
// SchemaVersion when a newer binary migrated the database and this binary
// meets that database's minimum reader schema.
func (s *Store) DatabaseSchema() int { return s.dbSchema }

// Path is the database file path.
func (s *Store) Path() string { return s.path }
