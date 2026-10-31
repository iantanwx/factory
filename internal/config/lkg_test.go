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

// A snapshot names the branch it was read from, and so does the last
// known good config that stands in for an invalid read: a run starts from
// that branch.
func TestSnapshotNamesItsBranch(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := StoreProjectRow(st)(ctx, Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"}); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{ProjectConfigPath: validProjectConfig}
	read := func(_ context.Context, ref, path string) ([]byte, error) {
		if b, ok := files[path]; ok && ref == "origin/trunk" {
			return []byte(b), nil
		}
		return nil, fs.ErrNotExist
	}
	snap, err := ReadProjectSnapshot(ctx, read, "trunk")
	if err != nil || snap.Branch != "trunk" {
		t.Fatalf("snapshot = %+v, %v; want branch trunk", snap, err)
	}
	l := &Loader{LKG: StoreLKG(st), Alerts: StoreAlerter(st)}
	if got, err := l.Load(ctx, "alpha", read, "trunk"); err != nil || got.Branch != "trunk" {
		t.Fatalf("load = %+v, %v", got, err)
	}
	files[ProjectConfigPath] = "merge: {mechanism: bors}\n"
	got, err := l.Load(ctx, "alpha", read, "trunk")
	if err != nil || !got.Stale || got.Branch != "trunk" {
		t.Fatalf("stale load = %+v, %v; want the last known good config on trunk", got, err)
	}
	if (&ProjectSnapshot{}).BaseBranch() != "main" || snap.BaseBranch() != "trunk" {
		t.Fatalf("BaseBranch = %q, %q", (&ProjectSnapshot{}).BaseBranch(), snap.BaseBranch())
	}
}

// loopPinConfig is validProjectConfig with one pin in the loop- namespace.
const loopPinConfig = validProjectConfig + `skills:
  - name: loop-x
    repo: owner/repo
    commit: 0123456789abcdef0123456789abcdef01234567
    path: ""
    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`

// TestC1ALoopPinDoesNotLoad proves C1: a userspace pin named loop-x fails
// the parse. The load keeps the last good config and raises one
// project_config_invalid alert that names the pin. A project with no last
// good config refuses.
func TestC1ALoopPinDoesNotLoad(t *testing.T) {
	if _, err := ParseProjectConfig([]byte(loopPinConfig)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `"loop-x"`) {
		t.Fatalf("parse = %v, want ErrInvalid that names loop-x", err)
	}
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
	if _, err := l.Load(ctx, "alpha", origin.read, "main"); err != nil {
		t.Fatal(err)
	}

	origin[ProjectConfigPath] = loopPinConfig
	got, err := l.Load(ctx, "alpha", origin.read, "main")
	if err != nil || !got.Stale || got.Config.Merge.Mechanism != MergeMergify {
		t.Fatalf("load = %+v, %v, want the last good config", got, err)
	}
	var bodies []string
	err = st.Read(ctx, func(tx *sql.Tx) error {
		r, err := tx.Query(`SELECT body FROM alerts WHERE project = 'alpha' AND kind = ? AND state = 'open'`, AlertProjectConfigInvalid)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		for r.Next() {
			var b string
			if err := r.Scan(&b); err != nil {
				return err
			}
			bodies = append(bodies, b)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"loop-x"`) {
		t.Fatalf("open alert bodies = %q, want one that names loop-x", bodies)
	}

	if _, err := l.Load(ctx, "beta", origin.read, "main"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `"loop-x"`) {
		t.Fatalf("beta load = %v, want ErrInvalid that names loop-x", err)
	}
}

// neonPinConfig is validProjectConfig with one pin named neon.
const neonPinConfig = validProjectConfig + `skills:
  - name: neon
    repo: owner/repo
    commit: 0123456789abcdef0123456789abcdef01234567
    path: ""
    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`

// fakeTree serves the tracked file paths of origin/main as a TreeReader.
type fakeTree []string

func (f fakeTree) list(_ context.Context, ref, dir string) ([]TreeEntry, error) {
	if ref != "origin/main" {
		return nil, nil
	}
	var out []TreeEntry
	seen := map[string]bool{}
	for _, p := range f {
		rest, ok := strings.CutPrefix(p, dir+"/")
		if !ok {
			continue
		}
		name, _, sub := strings.Cut(rest, "/")
		if !seen[name] {
			out = append(out, TreeEntry{Name: name, Dir: sub})
			seen[name] = true
		}
	}
	return out, nil
}

// TestC4APinWhoseDirectoryTheDefaultBranchTracksDoesNotLoad proves C4: a
// pin neon on a default branch that tracks a file in .claude/skills/neon,
// in .agents/skills/neon, or at .claude/skills/neon itself keeps the last
// good config, and the open project_config_invalid alert names neon and
// that path. A tracked .claude/skills/neon-postgres loads the pin.
func TestC4APinWhoseDirectoryTheDefaultBranchTracksDoesNotLoad(t *testing.T) {
	for _, c := range []struct {
		tracked, path string
	}{
		{".claude/skills/neon/SKILL.md", ".claude/skills/neon"},
		{".agents/skills/neon/README.md", ".agents/skills/neon"},
		{".claude/skills/neon", ".claude/skills/neon"},
		{".claude/skills/neon-postgres/SKILL.md", ""},
	} {
		t.Run(c.tracked, func(t *testing.T) {
			ctx := context.Background()
			st := openStore(t)
			if err := StoreProjectRow(st)(ctx, Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"}); err != nil {
				t.Fatal(err)
			}
			tree := fakeTree{"README.md", c.tracked}
			l := &Loader{LKG: StoreLKG(st), Alerts: StoreAlerter(st), Tree: tree.list}
			origin := fakeOrigin{ProjectConfigPath: validProjectConfig}
			if _, err := l.Load(ctx, "alpha", origin.read, "main"); err != nil {
				t.Fatal(err)
			}

			origin[ProjectConfigPath] = neonPinConfig
			got, err := l.Load(ctx, "alpha", origin.read, "main")
			if err != nil {
				t.Fatal(err)
			}
			var bodies []string
			err = st.Read(ctx, func(tx *sql.Tx) error {
				r, err := tx.Query(`SELECT body FROM alerts WHERE project = 'alpha' AND kind = ? AND state = 'open'`, AlertProjectConfigInvalid)
				if err != nil {
					return err
				}
				defer func() { _ = r.Close() }()
				for r.Next() {
					var b string
					if err := r.Scan(&b); err != nil {
						return err
					}
					bodies = append(bodies, b)
				}
				return r.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			if c.path == "" {
				if got.Stale || len(got.Config.Skills) != 1 || len(bodies) != 0 {
					t.Fatalf("load = %+v, alerts %q, want the pin to load", got, bodies)
				}
				return
			}
			if !got.Stale || len(got.Config.Skills) != 0 {
				t.Fatalf("load = %+v, want the last good config", got)
			}
			if len(bodies) != 1 || !strings.Contains(bodies[0], `"neon"`) || !strings.Contains(bodies[0], c.path) {
				t.Fatalf("open alert bodies = %q, want one that names neon and %s", bodies, c.path)
			}
		})
	}
}
