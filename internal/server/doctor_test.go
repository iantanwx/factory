package server

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
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
}
