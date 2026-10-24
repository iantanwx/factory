package server

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

func failed(lines []github.CheckLine) []string {
	var out []string
	for _, l := range lines {
		if !l.OK {
			out = append(out, l.Check)
		}
	}
	return out
}

// TestDoctorChecksGitHubOnTheServer runs the network checks of loop doctor
// against synthetic GitHub: they pass on a good setup, and each fault
// fails its own line.
func TestDoctorChecksGitHubOnTheServer(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	p := config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}
	if err := w.srv.Projects.Add(ctx, p); err != nil {
		t.Fatal(err)
	}

	// No project config on the default branch yet.
	got, err := w.gh.Doctor(ctx, w.srv.Projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Project != "alpha" {
		t.Fatalf("doctor = %+v", got)
	}
	if f := failed(got[0].Lines); len(f) != 1 || f[0] != "project_config" {
		t.Fatalf("failed = %q", f)
	}

	w.api.World.SetFile("main", config.ProjectConfigPath, "merge: direct\n")
	got, _ = w.gh.Doctor(ctx, w.srv.Projects)
	if f := failed(got[0].Lines); len(f) != 0 {
		t.Fatalf("a good setup failed %q", f)
	}

	// The App lacks workflows and holds administration. The installation
	// selects every repository, which doctor allows.
	w.api.SetInstall("all", map[string]string{"contents": "write", "pull_requests": "write", "statuses": "write",
		"checks": "read", "actions": "read", "metadata": "read", "administration": "write"})
	w.api.SetRules([]any{map[string]any{"type": "pull_request"}})
	got, _ = w.gh.Doctor(ctx, w.srv.Projects)
	f := strings.Join(failed(got[0].Lines), " ")
	for _, want := range []string{"installation_permissions", "installation_required_permissions",
		"ci_required", "eligible_required"} {
		if !strings.Contains(f, want) {
			t.Errorf("failed %q lacks %s", f, want)
		}
	}
	for _, l := range got[0].Lines {
		if l.Check == "installation_required_permissions" && !strings.Contains(l.Detail, "workflows:write") {
			t.Errorf("detail = %q", l.Detail)
		}
	}

	// The owner suspended the installation (parity 10.2).
	w.api.SetInstallField("suspended_at", "2026-10-16T13:00:00Z")
	got, _ = w.gh.Doctor(ctx, w.srv.Projects)
	if f := failed(got[0].Lines); !slices.Contains(f, "installation_active") {
		t.Errorf("a suspended installation passes: failed %q", f)
	}
}

// staleEnvWorld is one checkout of each kind the loop makes, each with a
// .env: a live run, a settled run, a crashed run kept for a person, a live
// room, an idle room and the coordinator. A second settled run holds only
// the .env of a manifest dir (parity 9.3), named in its record. It returns
// the server's instance and the checkout paths by kind.
func staleEnvWorld(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := instanceDir(t)
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
	var srv *Server
	start(t, dir, Options{Setup: []func(*Server) error{func(s *Server) error { srv = s; return nil }}}, true)
	if env, code, _ := call(t, dir, time.Second, nil, "project", "add", "alpha", "--repo", "iantanwx/alpha", "--path", "/srv/alpha"); code != 0 {
		t.Fatalf("project add: %v", env)
	}
	root := t.TempDir()
	paths := map[string]string{}
	for _, kind := range []string{"live_run", "settled_run", "kept_run", "live_room", "idle_room", "coordinator", "nested_run"} {
		paths[kind] = filepath.Join(root, kind)
		if err := os.Mkdir(paths[kind], 0o700); err != nil {
			t.Fatal(err)
		}
		if kind == "nested_run" {
			continue
		}
		if err := os.WriteFile(filepath.Join(paths[kind], brief.EnvFile), []byte("API_KEY=v\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"web", ".loop"} {
		if err := os.Mkdir(filepath.Join(paths["nested_run"], d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(paths["nested_run"], "web", brief.EnvFile), []byte("API_KEY=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths["nested_run"], brief.EnvRecord), []byte("web/.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := srv.Store.Write(context.Background(), func(tx *store.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO runs (run_id, project, stage, state, started_at, ended_at) VALUES
				('r-live', 'alpha', 'implementer', 'running', 1, NULL),
				('r-settled', 'alpha', 'implementer', 'finished', 1, 2),
				('r-kept', 'alpha', 'implementer', 'crashed', 1, 2),
				('r-nested', 'alpha', 'implementer', 'finished', 1, 2)`, nil},
			{`INSERT INTO checkouts (id, project, path, run_id, state, created_at, updated_at) VALUES
				('r-live', 'alpha', ?, 'r-live', 'active', 1, 1),
				('r-settled', 'alpha', ?, 'r-settled', 'settled', 1, 1),
				('r-kept', 'alpha', ?, 'r-kept', 'kept', 1, 1),
				('m-live', 'alpha', ?, 'm-live', 'active', 1, 1),
				('m-idle', 'alpha', ?, 'm-idle', 'active', 1, 1),
				('r-nested', 'alpha', ?, 'r-nested', 'settled', 1, 1)`,
				[]any{paths["live_run"], paths["settled_run"], paths["kept_run"], paths["live_room"], paths["idle_room"], paths["nested_run"]}},
			{`INSERT INTO rooms (id, project, checkout_id, state, opened_at) VALUES
				('m-live', 'alpha', 'm-live', 'live', 1), ('m-idle', 'alpha', 'm-idle', 'idle', 1)`, nil},
			{`INSERT INTO run_tokens (token_hash, run_id, kind, checkout, issued_at) VALUES ('h', 'coordinator-1', 'coordinator', ?, 1)`,
				[]any{paths["coordinator"]}},
		} {
			if _, err := tx.Exec(q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir, paths
}

// loop doctor lists each .env in a checkout with no live run (KTD17): a
// settled run's, a kept crashed run's, an idle room's and the
// coordinator's, and the .env of a manifest dir its record names. A live
// run's and a live room's are no fault. --fix removes each stale one with
// its record and leaves the live ones.
func TestDoctorListsEachStaleEnvAndFixRemovesIt(t *testing.T) {
	dir, paths := staleEnvWorld(t)
	stale := map[string]string{}
	for _, kind := range []string{"settled_run", "kept_run", "idle_room", "coordinator"} {
		stale[kind] = filepath.Join(paths[kind], brief.EnvFile)
	}
	stale["nested_run"] = filepath.Join(paths["nested_run"], "web", brief.EnvFile)

	env, code, out := call(t, dir, time.Second, nil, "doctor-env")
	if code != 0 {
		t.Fatalf("doctor-env = %d %s", code, out)
	}
	lines := env["data"].([]any)
	var listed []string
	for _, l := range lines {
		l := l.(map[string]any)
		if l["check"] != "stale_env" || l["ok"] != false || !strings.Contains(l["detail"].(string), "loop doctor --fix") {
			t.Fatalf("line = %v", l)
		}
		listed = append(listed, l["detail"].(string))
	}
	if len(listed) != len(stale) {
		t.Fatalf("listed %q, want the .env of %q", listed, stale)
	}
	for kind, p := range stale {
		if !slices.ContainsFunc(listed, func(d string) bool { return strings.HasPrefix(d, p+":") }) {
			t.Errorf("the %s .env is not listed: %q", kind, listed)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the listing removed the %s .env: %v", kind, err)
		}
	}

	env, code, out = call(t, dir, time.Second, nil, "doctor-env", "--fix")
	if code != 0 {
		t.Fatalf("doctor-env --fix = %d %s", code, out)
	}
	for _, l := range env["data"].([]any) {
		if l := l.(map[string]any); l["check"] != "stale_env" || l["ok"] != true || !strings.HasPrefix(l["detail"].(string), "removed ") {
			t.Fatalf("fixed line = %v", l)
		}
	}
	for kind, p := range stale {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("--fix left the %s .env: %v", kind, err)
		}
	}
	if _, err := os.Stat(filepath.Join(paths["nested_run"], brief.EnvRecord)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("--fix left the nested_run record: %v", err)
	}
	for _, kind := range []string{"live_run", "live_room"} {
		if _, err := os.Stat(filepath.Join(paths[kind], brief.EnvFile)); err != nil {
			t.Errorf("--fix removed the %s .env: %v", kind, err)
		}
	}

	env, code, _ = call(t, dir, time.Second, nil, "doctor-env")
	if lines := env["data"].([]any); code != 0 || len(lines) != 1 || lines[0].(map[string]any)["ok"] != true {
		t.Fatalf("after --fix: %d %v", code, env)
	}
}
