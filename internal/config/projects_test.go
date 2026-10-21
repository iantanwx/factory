package config

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/store"
)

func newProjects(t *testing.T, hooks ...AddHook) *Projects {
	t.Helper()
	path := filepath.Join(t.TempDir(), HostConfigFile)
	if err := SaveHost(path, DefaultHost("/var/lib/loop")); err != nil {
		t.Fatal(err)
	}
	return &Projects{HostPath: path, AddHooks: hooks}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestProjectsAddListShowRemove(t *testing.T) {
	ctx := context.Background()
	var hooked []string
	ps := newProjects(t, func(_ context.Context, p Project) error {
		hooked = append(hooked, p.ID)
		return nil
	})
	a := Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/loop/alpha"}
	b := Project{ID: "beta", Repo: "iantanwx/beta", Path: "/srv/loop/beta"}
	for _, p := range []Project{a, b} {
		if err := ps.Add(ctx, p); err != nil {
			t.Fatalf("Add %s: %v", p.ID, err)
		}
	}
	if len(hooked) != 2 {
		t.Fatalf("hooks ran for %v, want both projects", hooked)
	}
	list, err := ps.List()
	if err != nil || len(list) != 2 || list[0] != a || list[1] != b {
		t.Fatalf("List = %v, %v", list, err)
	}
	got, err := ps.Show("beta")
	if err != nil || got != b {
		t.Fatalf("Show = %v, %v", got, err)
	}
	if _, err := ps.Show("gamma"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("Show missing: %v", err)
	}

	live := true
	isLive := func(context.Context, string) (bool, error) { return live, nil }
	if err := ps.Remove(ctx, "alpha", isLive); !errors.Is(err, ErrProjectHasLiveRun) {
		t.Fatalf("Remove with a live run: %v, want ErrProjectHasLiveRun", err)
	}
	if err := ps.Remove(ctx, "alpha", nil); err == nil {
		t.Fatal("Remove ran with no live-run check")
	}
	live = false
	if err := ps.Remove(ctx, "alpha", isLive); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if list, _ := ps.List(); len(list) != 1 || list[0] != b {
		t.Fatalf("List after remove = %v", list)
	}
}

func TestProjectsRefuseSecondAddOfSameRepository(t *testing.T) {
	ctx := context.Background()
	ps := newProjects(t)
	if err := ps.Add(ctx, Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]Project{
		"same repo":        {ID: "alpha2", Repo: "iantanwx/alpha", Path: "/srv/a2"},
		"same repo case":   {ID: "alpha3", Repo: "IanTanWX/Alpha", Path: "/srv/a3"},
		"same id":          {ID: "alpha", Repo: "iantanwx/other", Path: "/srv/o"},
		"same checkout":    {ID: "other", Repo: "iantanwx/other", Path: "/srv/a"},
		"organisation url": {ID: "org", Repo: "github.com/acme/x", Path: "/srv/x"},
		"relative path":    {ID: "rel", Repo: "iantanwx/rel", Path: "srv/rel"},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ps.Add(ctx, p); err == nil {
				t.Fatalf("Add(%v) succeeded", p)
			}
		})
	}
	if list, _ := ps.List(); len(list) != 1 {
		t.Fatalf("a refused add changed the registry: %v", list)
	}
}

func TestProjectsHookFailureRegistersNothing(t *testing.T) {
	boom := errors.New("mirror clone failed")
	ps := newProjects(t, func(context.Context, Project) error { return boom })
	err := ps.Add(context.Background(), Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"})
	if !errors.Is(err, boom) {
		t.Fatalf("Add = %v, want the hook error", err)
	}
	if list, _ := ps.List(); len(list) != 0 {
		t.Fatalf("a failed hook left a registration: %v", list)
	}
}

func TestStoreProjectRowHook(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	hook := StoreProjectRow(st)
	p := Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"}
	if err := hook(ctx, p); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if err := hook(ctx, p); err != nil {
		t.Fatalf("hook is not idempotent: %v", err)
	}
	// A reused id that now names another repository would inherit the old
	// project's secrets and history, so the hook refuses it.
	if err := hook(ctx, Project{ID: "alpha", Repo: "iantanwx/other", Path: "/srv/o"}); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("reused id: %v, want ErrProjectExists", err)
	}
	var repo string
	err := st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT repo FROM projects WHERE id = 'alpha'`).Scan(&repo)
	})
	if err != nil || repo != "iantanwx/alpha" {
		t.Fatalf("projects row = %q, %v", repo, err)
	}
}
