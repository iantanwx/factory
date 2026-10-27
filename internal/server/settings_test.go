package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
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

// setWorld is a GitHub world with the merge setup, whose project alpha's
// main holds cfg, and whose fake GitHub serves that file at main's head.
func setWorld(t *testing.T, cfg string) (*ghWorld, string) {
	t.Helper()
	w := newGHWorld(t)
	if err := (&Merge{GitHub: w.gh}).Setup(w.srv); err != nil {
		t.Fatal(err)
	}
	gt.Git(t, w.checkout, "checkout", "-q", "main")
	base := gt.Commit(t, w.checkout, "config", map[string]string{config.ProjectConfigPath: cfg})
	gt.Git(t, w.checkout, "push", "-q", w.git.RemoteDir(), "main")
	gt.Git(t, w.checkout, "remote", "add", "origin", w.git.RemoteDir())
	w.api.World.SetFile(base, config.ProjectConfigPath, cfg)
	if err := w.srv.Projects.Add(context.Background(), config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	return w, base
}

// as runs one CLI call with the token issued to a principal of kind.
func (w *ghWorld) as(kind string, args ...string) (map[string]any, int) {
	w.t.Helper()
	ctx := context.Background()
	dir := w.t.TempDir()
	if _, err := w.srv.Tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: kind + "-1", Kind: kind, Project: "alpha",
		Checkout: dir}); err != nil {
		w.t.Fatal(err)
	}
	w.t.Chdir(dir)
	r := cli.Surface()
	(&cli.Client{Instance: w.dir, Budget: 10 * time.Second, Getwd: func() (string, error) { return dir, nil }}).Attach(r)
	var out bytes.Buffer
	code := r.Run(ctx, args, &out, io.Discard)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		w.t.Fatalf("loop %v printed no envelope: %v\n%s", args, err, out.String())
	}
	return env, code
}

func (w *ghWorld) changeRows() int {
	w.t.Helper()
	var n int
	if err := w.srv.Store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM settings_changes`).Scan(&n)
	}); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// The settings branch of the first change.
const firstSettingsBranch = "loop/settings/1"

// The ticket's done case: against a fake GitHub, the coordinator's
// command opens one pull request whose branch holds one commit over main's
// head, and that commit changes stages.implementer.cap only.
func TestSettingsSetOpensOnePullRequestWithOneCommit(t *testing.T) {
	cfg := "# the loop\nmerge:\n  mechanism: direct\nstages:\n  implementer: {} # builds tickets\n  code-reviewer: {}\n"
	w, base := setWorld(t, cfg)
	env, code := w.as(cli.PrincipalCoordinator, "settings", "set", "alpha", "stages.implementer.cap", "--value", "3",
		"--reason", "two projects share one quota")
	if code != 0 {
		t.Fatalf("settings set = %d %v", code, env)
	}
	data := env["data"].(map[string]any)
	if data["state"] != "open" || data["setting"] != "stages.implementer.cap" || data["branch"] != firstSettingsBranch ||
		data["old"] != "8" || data["old_source"] != "host" || data["new"] != "3" || data["actor"] != "coordinator" {
		t.Fatalf("change = %v", data)
	}

	prs := w.api.World.PRs()
	commits := w.api.World.Commits(firstSettingsBranch)
	if len(prs) != 1 || prs[0].Head != firstSettingsBranch || data["pr"] != float64(prs[0].Number) {
		t.Fatalf("pull requests = %+v; want one from %s", prs, firstSettingsBranch)
	}
	if len(commits) != 1 || commits[0].Parent != base || data["head"] != commits[0].SHA {
		t.Fatalf("commits on %s = %+v; want one over %s", firstSettingsBranch, commits, base)
	}
	want := strings.Replace(cfg, "implementer: {}", "implementer: {cap: 3}", 1)
	if commits[0].Content != want {
		t.Fatalf("the commit writes\n%s\nwant\n%s", commits[0].Content, want)
	}
	if msg := commits[0].Message; !strings.HasPrefix(msg, "chore(settings): set stages.implementer.cap to 3\n\ntwo projects share one quota\n\n") ||
		!strings.HasSuffix(msg, "Loop-Changed-By: coordinator\n") {
		t.Fatalf("commit message = %q", msg)
	}
}

// C19: a run's token and a room's token each get CAPABILITY_DENIED from
// settings set, and the store holds no change row.
func TestC19SettingsSetAdmitsTheCoordinatorOnly(t *testing.T) {
	w, _ := setWorld(t, "merge:\n  mechanism: direct\nstages:\n  implementer: {}\n")
	for _, kind := range []string{cli.PrincipalRun, cli.PrincipalRoom} {
		env, _ := w.as(kind, "settings", "set", "alpha", "stages.implementer.cap", "--value", "3", "--reason", "fewer runs")
		if env["code"] != cli.CodeCapabilityDenied {
			t.Fatalf("settings set as %s = %v; want %s", kind, env, cli.CodeCapabilityDenied)
		}
	}
	if n := w.changeRows(); n != 0 || len(w.api.World.PRs()) != 0 {
		t.Fatalf("a refused caller left %d change rows and %d pull requests", n, len(w.api.World.PRs()))
	}
}

// C20: an absent --reason gets USAGE from the CLI, and lands nothing.
func TestC20SettingsSetWithNoReasonIsRefused(t *testing.T) {
	w, _ := setWorld(t, "merge:\n  mechanism: direct\nstages:\n  implementer: {}\n")
	env, _ := w.as(cli.PrincipalCoordinator, "settings", "set", "alpha", "stages.implementer.cap", "--value", "3")
	if env["code"] != cli.CodeUsage || env["rule"] != "missing_flag" || !strings.Contains(env["hint"].(string), "--reason") {
		t.Fatalf("settings set with no reason = %v; want %s naming --reason", env, cli.CodeUsage)
	}
	if n := w.changeRows(); n != 0 || len(w.api.World.PRs()) != 0 || len(w.api.World.Commits(firstSettingsBranch)) != 0 {
		t.Fatalf("a refused change left %d change rows, a pull request or a commit", n)
	}
}
