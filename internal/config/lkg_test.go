package config

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/store"
)

type alertRow struct {
	kind, state string
	count       int
}

func readAlerts(t *testing.T, st *store.Store, project string) []alertRow {
	t.Helper()
	var rows []alertRow
	err := st.Read(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.Query(`SELECT kind, state, count FROM alerts WHERE project = ? ORDER BY id`, project)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		for r.Next() {
			var a alertRow
			if err := r.Scan(&a.kind, &a.state, &a.count); err != nil {
				return err
			}
			rows = append(rows, a)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// fakeOrigin serves one file set per project as origin/main.
type fakeOrigin map[string]string

func (f fakeOrigin) read(_ context.Context, ref, path string) ([]byte, error) {
	if ref != "origin/main" {
		return nil, fs.ErrNotExist
	}
	if b, ok := f[path]; ok {
		return []byte(b), nil
	}
	return nil, fs.ErrNotExist
}

func TestInvalidConfigKeepsLastKnownGoodAndRaisesAlert(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	for _, p := range []Project{
		{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"},
		{ID: "beta", Repo: "iantanwx/beta", Path: "/srv/b"},
	} {
		if err := StoreProjectRow(st)(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	l := &Loader{LKG: StoreLKG(st), Alerts: StoreAlerter(st)}
	origin := fakeOrigin{ProjectConfigPath: validProjectConfig}

	got, err := l.Load(ctx, "alpha", origin.read, "main")
	if err != nil || got.Stale || got.Config.Merge.Mechanism != MergeMergify {
		t.Fatalf("first load = %+v, %v", got, err)
	}

	// The default branch now holds an invalid file.
	origin[ProjectConfigPath] = strings.Replace(validProjectConfig, "mechanism: mergify", "mechanism: bors", 1)
	for i := 0; i < 2; i++ {
		got, err = l.Load(ctx, "alpha", origin.read, "main")
		if err != nil {
			t.Fatalf("load with LKG: %v", err)
		}
		if !got.Stale || !errors.Is(got.Problem, ErrInvalid) || got.Config.Merge.Mechanism != MergeMergify {
			t.Fatalf("load %d = %+v, want the last known good config", i, got)
		}
	}
	if a := readAlerts(t, st, "alpha"); len(a) != 1 || a[0].kind != AlertProjectConfigInvalid || a[0].state != "open" || a[0].count != 2 {
		t.Fatalf("alerts = %+v, want one open alert seen twice", a)
	}

	// A project with no last known good config refuses, and still alerts.
	_, err = l.Load(ctx, "beta", origin.read, "main")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("beta load = %v, want ErrInvalid", err)
	}
	if a := readAlerts(t, st, "beta"); len(a) != 1 || a[0].state != "open" {
		t.Fatalf("beta alerts = %+v", a)
	}

	// A repaired file loads, becomes the new LKG, and closes the alert.
	origin[ProjectConfigPath] = strings.Replace(validProjectConfig, "mechanism: mergify", "mechanism: direct", 1)
	got, err = l.Load(ctx, "alpha", origin.read, "main")
	if err != nil || got.Stale || got.Config.Merge.Mechanism != MergeDirect {
		t.Fatalf("repaired load = %+v, %v", got, err)
	}
	if a := readAlerts(t, st, "alpha"); a[0].state != "closed" {
		t.Fatalf("alert after repair = %+v", a)
	}

	// Each project keeps its own last known good config.
	origin[ProjectConfigPath] = validProjectConfig
	if got, err := l.Load(ctx, "beta", origin.read, "main"); err != nil || got.Config.Merge.Mechanism != MergeMergify {
		t.Fatalf("beta = %+v, %v", got, err)
	}
	delete(origin, ProjectConfigPath)
	got, err = l.Load(ctx, "alpha", origin.read, "main")
	if err != nil || !got.Stale || got.Config.Merge.Mechanism != MergeDirect {
		t.Fatalf("alpha LKG after a missing file = %+v, %v", got, err)
	}
}

// A project that never had config is not set up yet: the load refuses with
// ErrConfigMissing and raises no alert.
func TestMissingConfigWithNoLastKnownGoodRaisesNoAlert(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := StoreProjectRow(st)(ctx, Project{ID: "bare", Repo: "iantanwx/bare", Path: "/srv/bare"}); err != nil {
		t.Fatal(err)
	}
	l := &Loader{LKG: StoreLKG(st), Alerts: StoreAlerter(st)}
	_, err := l.Load(ctx, "bare", fakeOrigin{}.read, "main")
	if !errors.Is(err, ErrConfigMissing) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("load = %v, want ErrConfigMissing", err)
	}
	if a := readAlerts(t, st, "bare"); len(a) != 0 {
		t.Fatalf("alerts = %+v, want none", a)
	}
}
