package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestOpenEmptyDatabaseRunsMigrationsAndVerifiesSettings(t *testing.T) {
	s := openTemp(t)
	if s.SchemaVersion() < 1 {
		t.Fatalf("SchemaVersion = %d, want >= 1", s.SchemaVersion())
	}
	if s.DatabaseSchema() != s.SchemaVersion() {
		t.Fatalf("DatabaseSchema = %d, want %d", s.DatabaseSchema(), s.SchemaVersion())
	}

	err := s.Write(context.Background(), func(tx *Tx) error {
		var userVersion, minReader int
		if err := tx.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
			return err
		}
		if userVersion != s.SchemaVersion() {
			return fmt.Errorf("user_version = %d, want %d", userVersion, s.SchemaVersion())
		}
		if err := tx.QueryRow("SELECT min_reader_schema FROM schema_meta").Scan(&minReader); err != nil {
			return err
		}
		if minReader != 1 {
			return fmt.Errorf("min_reader_schema = %d, want 1", minReader)
		}
		var mode string
		var sync, fk, busy, limit int
		if err := tx.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
			return err
		}
		if err := tx.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
			return err
		}
		if err := tx.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
			return err
		}
		if err := tx.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
			return err
		}
		if err := tx.QueryRow("PRAGMA journal_size_limit").Scan(&limit); err != nil {
			return err
		}
		if mode != "wal" || sync != 2 || fk != 1 || busy != 5000 || limit != journalSizeLimit {
			return fmt.Errorf("writer settings: journal_mode=%s synchronous=%d foreign_keys=%d busy_timeout=%d journal_size_limit=%d",
				mode, sync, fk, busy, limit)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	tables := []string{
		"projects", "item_seq", "intents", "outbox", "idempotency", "leases", "runs", "failures",
		"stage_mutexes", "schedule", "checkouts", "rooms", "alerts", "audit", "github_cursors",
		"merge_tracking", "push_intents", "adapter_checkpoints", "liveness_ticks", "outages", "secrets",
	}
	for _, table := range tables {
		_ = countRows(t, s, table)
	}
}

func TestReopenKeepsSchemaAndRows(t *testing.T) {
	s := openTemp(t)
	addProject(t, s, "p1")
	path := s.path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if n := countRows(t, s2, "projects"); n != 1 {
		t.Fatalf("projects = %d, want 1", n)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	s := openTemp(t)
	err := s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO runs (run_id, project, stage, state, started_at) VALUES ('r1', 'missing', 'build', 'running', 0)`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("insert with a missing project: err = %v, want a FOREIGN KEY failure", err)
	}
}

func TestReaderCannotWrite(t *testing.T) {
	s := openTemp(t)
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO projects (id, repo, created_at, updated_at) VALUES ('p', 'r', 0, 0)`)
		return err
	})
	if err == nil {
		t.Fatal("a write through Read succeeded")
	}
	if n := countRows(t, s, "projects"); n != 0 {
		t.Fatalf("projects = %d after a refused reader write, want 0", n)
	}
	var queryOnly int
	err = s.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("PRAGMA query_only").Scan(&queryOnly)
	})
	if err != nil || queryOnly != 1 {
		t.Fatalf("reader query_only = %d, err = %v; want 1", queryOnly, err)
	}
}

func TestWriteErrorRollsBack(t *testing.T) {
	s := openTemp(t)
	boom := errors.New("boom")
	err := s.Write(context.Background(), func(tx *Tx) error {
		if _, err := tx.Exec(`INSERT INTO projects (id, repo, created_at, updated_at) VALUES ('p', 'r', 0, 0)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Write err = %v, want boom", err)
	}
	if n := countRows(t, s, "projects"); n != 0 {
		t.Fatalf("projects = %d after rollback, want 0", n)
	}
}

func TestWritePanicRollsBackAndRepanics(t *testing.T) {
	s := openTemp(t)
	func() {
		defer func() {
			if r := recover(); r != "kaboom" {
				t.Fatalf("recover = %v, want kaboom", r)
			}
		}()
		_ = s.Write(context.Background(), func(tx *Tx) error {
			if _, err := tx.Exec(`INSERT INTO projects (id, repo, created_at, updated_at) VALUES ('p', 'r', 0, 0)`); err != nil {
				return err
			}
			panic("kaboom")
		})
	}()
	if n := countRows(t, s, "projects"); n != 0 {
		t.Fatalf("projects = %d after a panic, want 0", n)
	}
	// The writer connection is free again.
	addProject(t, s, "after")
}

func TestConcurrentWritesSerialize(t *testing.T) {
	s := openTemp(t)
	const workers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*each+workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				err := s.Write(context.Background(), func(tx *Tx) error {
					// Read then write inside one transaction: a lost update
					// would show as a short count.
					var n int
					if err := tx.QueryRow(`SELECT coalesce(max(last_seq), 0) FROM item_seq WHERE project = 'p' AND item = 'counter'`).Scan(&n); err != nil {
						return err
					}
					_, err := tx.Exec(`INSERT INTO item_seq (project, item, last_seq) VALUES ('p', 'counter', ?)
						ON CONFLICT (project, item) DO UPDATE SET last_seq = excluded.last_seq`, n+1)
					return err
				})
				if err != nil {
					errs <- err
				}
			}
		}(w)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				err := s.Read(context.Background(), func(tx *sql.Tx) error {
					var n int
					return tx.QueryRow(`SELECT count(*) FROM item_seq`).Scan(&n)
				})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent call: %v", err)
	}
	var got int
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT last_seq FROM item_seq WHERE project = 'p' AND item = 'counter'`).Scan(&got)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != workers*each {
		t.Fatalf("counter = %d, want %d", got, workers*each)
	}
}
