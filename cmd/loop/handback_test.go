package main

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/store"
)

// C39: the rollback's hand-back reads the instance's registered projects
// and database, and leaves the first project's coordinator as the one
// coordinator row that the earlier release reads.
func TestC39RollbackHandsTheFirstProjectsCoordinatorBack(t *testing.T) {
	ctx := context.Background()
	l := release.Layout{Dir: t.TempDir()}
	cfg := config.DefaultHost(l.Dir)
	cfg.Projects = []config.Project{
		{ID: "alpha", Repo: "iantanwx/alpha", Path: t.TempDir()},
		{ID: "beta", Repo: "iantanwx/beta", Path: t.TempDir()},
	}
	if err := config.SaveHost(l.HostConfig(), cfg); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, l.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *store.Tx) error {
		for _, p := range []string{"beta", "alpha"} {
			if _, err := tx.Exec(`INSERT INTO schedule (project, kind, payload, created_at, updated_at)
				VALUES (?, 'home_coordinator', json_object('project', ?, 'run_id', 'coordinator-' || ?), 0, 0)`, p, p, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if err := handBack(ctx, l); err != nil {
		t.Fatal(err)
	}

	st, err = store.Open(ctx, l.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var run string
	var homes int
	if err := st.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT payload ->> 'run_id' FROM schedule WHERE kind = 'coordinator' AND project = ''`).Scan(&run); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT count(*) FROM schedule WHERE kind = 'home_coordinator'`).Scan(&homes)
	}); err != nil {
		t.Fatal(err)
	}
	if run != "coordinator-alpha" || homes != 1 {
		t.Fatalf("coordinator row run %q, home rows %d; want alpha's run and beta's row", run, homes)
	}
}

// An instance with no database has no coordinator to hand back.
func TestHandBackWithNoDatabase(t *testing.T) {
	l := release.Layout{Dir: t.TempDir()}
	if err := handBack(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.Dir, "loop.db")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the hand-back made a database")
	}
}
