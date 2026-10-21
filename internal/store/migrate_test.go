package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func file(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }

func TestOpenRefusesDatabaseWhoseMinimumReaderIsAboveBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loop.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	binary := s.SchemaVersion()
	_ = s.Close()

	// Forge the state a future binary leaves: schema 9 that only schema 7
	// or later may read.
	db := rawDB(t, path)
	if _, err := db.Exec(`PRAGMA user_version = 9`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_meta SET min_reader_schema = 7`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	_, err = Open(context.Background(), path)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("Open err = %v, want ErrSchemaTooNew", err)
	}
	var tooNew *SchemaTooNewError
	if !errors.As(err, &tooNew) || tooNew.MinReaderSchema != 7 || tooNew.BinarySchema != binary || tooNew.DBSchema != 9 {
		t.Fatalf("Open err = %#v, want min reader 7, binary %d, database 9", err, binary)
	}
	msg := err.Error()
	for _, want := range []string{"reader schema 7", fmt.Sprintf("this binary has schema %d", binary), "database schema 9"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not name %q", msg, want)
		}
	}
}

func TestOpenAcceptsNewerDatabaseWhoseMinimumReaderTheBinaryMeets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loop.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	binary := s.SchemaVersion()
	addProject(t, s, "p1")
	_ = s.Close()

	db := rawDB(t, path)
	if _, err := db.Exec(`PRAGMA user_version = 9`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE future_additive (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open of an additive future schema: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if s2.DatabaseSchema() != 9 || s2.SchemaVersion() != binary {
		t.Fatalf("DatabaseSchema = %d, SchemaVersion = %d; want 9 and %d", s2.DatabaseSchema(), s2.SchemaVersion(), binary)
	}
	if n := countRows(t, s2, "projects"); n != 1 {
		t.Fatalf("projects = %d, want 1", n)
	}
	// The older binary still writes.
	addProject(t, s2, "p2")
}

func TestRollbackAfterAMigrationThatRaisesTheMinimumReader(t *testing.T) {
	v1 := fstest.MapFS{
		"0001_init.sql": file("-- min_reader_schema: 1\nCREATE TABLE a (x INTEGER);\n"),
	}
	v2 := fstest.MapFS{
		"0001_init.sql":  v1["0001_init.sql"],
		"0002_more.sql":  file("-- min_reader_schema: 1\nCREATE TABLE b (x INTEGER);\n"),
		"0003_break.sql": file("-- min_reader_schema: 3\nALTER TABLE a RENAME TO a_old;\n"),
	}
	path := filepath.Join(t.TempDir(), "loop.db")

	s, err := openWith(context.Background(), path, v1)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = openWith(context.Background(), path, v2)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if s.SchemaVersion() != 3 || s.DatabaseSchema() != 3 {
		t.Fatalf("after upgrade: binary %d, database %d; want 3 and 3", s.SchemaVersion(), s.DatabaseSchema())
	}
	_ = s.Close()

	_, err = openWith(context.Background(), path, v1)
	var tooNew *SchemaTooNewError
	if !errors.As(err, &tooNew) || tooNew.MinReaderSchema != 3 || tooNew.BinarySchema != 1 {
		t.Fatalf("rollback open err = %v, want min reader 3 above binary 1", err)
	}
}

func TestMinimumReaderNeverDecreases(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_a.sql": file("-- min_reader_schema: 1\nCREATE TABLE a (x INTEGER);\n"),
		"0002_b.sql": file("-- min_reader_schema: 2\nCREATE TABLE b (x INTEGER);\n"),
		"0003_c.sql": file("-- min_reader_schema: 1\nCREATE TABLE c (x INTEGER);\n"),
	}
	path := filepath.Join(t.TempDir(), "loop.db")
	s, err := openWith(context.Background(), path, fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var minReader int
	err = s.Write(context.Background(), func(tx *Tx) error {
		return tx.QueryRow(`SELECT min_reader_schema FROM schema_meta`).Scan(&minReader)
	})
	if err != nil || minReader != 2 {
		t.Fatalf("min_reader_schema = %d, err = %v; want 2", minReader, err)
	}
}

func TestFailedMigrationLeavesTheEarlierSchema(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_a.sql":   file("-- min_reader_schema: 1\nCREATE TABLE a (x INTEGER);\n"),
		"0002_bad.sql": file("-- min_reader_schema: 2\nCREATE TABLE b (x INTEGER);\nINSERT INTO nowhere VALUES (1);\n"),
	}
	path := filepath.Join(t.TempDir(), "loop.db")
	if _, err := openWith(context.Background(), path, fsys); err == nil || !strings.Contains(err.Error(), "0002") {
		t.Fatalf("open err = %v, want a failure that names migration 0002", err)
	}
	db := rawDB(t, path)
	var userVersion, minReader, tables int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT min_reader_schema FROM schema_meta`).Scan(&minReader); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name = 'b'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if userVersion != 1 || minReader != 1 || tables != 0 {
		t.Fatalf("after a failed 0002: user_version %d, min reader %d, table b %d; want 1, 1, 0", userVersion, minReader, tables)
	}
}

func TestLoadMigrationsRefusesBadSets(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"no header": {"0001_a.sql": file("CREATE TABLE a (x INTEGER);\n")},
		"gap": {
			"0001_a.sql": file("-- min_reader_schema: 1\nSELECT 1;\n"),
			"0003_c.sql": file("-- min_reader_schema: 1\nSELECT 1;\n"),
		},
		"duplicate": {
			"0001_a.sql": file("-- min_reader_schema: 1\nSELECT 1;\n"),
			"0001_b.sql": file("-- min_reader_schema: 1\nSELECT 1;\n"),
		},
		"reader above own version": {"0001_a.sql": file("-- min_reader_schema: 2\nSELECT 1;\n")},
		"bad name":                 {"init.sql": file("-- min_reader_schema: 1\nSELECT 1;\n")},
		"empty":                    {},
	}
	for name, fsys := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadMigrations(fsys); err == nil {
				t.Fatal("loadMigrations accepted a bad set")
			}
		})
	}
}

func TestEmbeddedMigrationsLoad(t *testing.T) {
	ms, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 || ms[0].version != 1 || ms[0].minReader != 1 {
		t.Fatalf("embedded migrations = %+v", ms)
	}
}
