package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/rpc"
)

// C15: `loop settings show` from the operator's shell lists the
// implementer cap as 8 from host. After a commit on the remote default
// branch that sets it to 3, it fetches that branch and lists 3 from
// project with that commit's sha.
func TestC15SettingsShowFromTheHost(t *testing.T) {
	dir := instanceDir(t)
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
	repo := projectRepo(t, "stages:\n  implementer: {}\nmerge:\n  mechanism: direct\n")
	var srv *Server
	start(t, dir, Options{Setup: []func(*Server) error{func(s *Server) error { srv = s; return nil }}}, true)
	if err := srv.Projects.Add(context.Background(), config.Project{ID: "alpha", Repo: "iantanwx/alpha", Path: repo}); err != nil {
		t.Fatal(err)
	}
	capRow := func() map[string]any {
		t.Helper()
		env, code, _ := call(t, dir, 10*time.Second, nil, "settings", "show", "alpha")
		if code != 0 {
			t.Fatalf("settings show = %d %v", code, env)
		}
		data := env["data"].(map[string]any)
		for _, r := range data["settings"].([]any) {
			if row := r.(map[string]any); row["key"] == "stages.implementer.cap" {
				return row
			}
		}
		t.Fatalf("settings show lists no stages.implementer.cap: %v", data)
		return nil
	}
	if row := capRow(); row["value"] != "8" || row["source"] != "host" || row["commit"] != nil {
		t.Fatalf("before the commit: %v", row)
	}

	origin := filepath.Join(filepath.Dir(repo), "origin.git")
	other := filepath.Join(t.TempDir(), "other")
	gitT(t, filepath.Dir(other), "clone", "-q", origin, other)
	if err := os.WriteFile(filepath.Join(other, config.ProjectConfigPath),
		[]byte("stages:\n  implementer: {cap: 3}\nmerge:\n  mechanism: direct\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, other, "commit", "-q", "-am", "set the implementer cap")
	gitT(t, other, "push", "-q", "origin", "main")
	sha := revParse(t, origin, "main")
	if row := capRow(); row["value"] != "3" || row["source"] != "project" || row["commit"] != sha {
		t.Fatalf("after commit %s: %v", sha, row)
	}

	if env, _, _ := call(t, dir, 10*time.Second, nil, "settings", "show", "nope"); env["rule"] != "project_not_found" {
		t.Fatalf("settings show nope = %v", env)
	}
}
