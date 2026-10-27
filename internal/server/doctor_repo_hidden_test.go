package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// hiddenRepoConfigLine runs doctor while the App's token reads the whole
// repository as not found, though project config is on main, and returns
// the project_config line.
func hiddenRepoConfigLine(t *testing.T) github.CheckLine {
	t.Helper()
	w := newGHWorld(t)
	ctx := context.Background()
	p := config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}
	if err := w.srv.Projects.Add(ctx, p); err != nil {
		t.Fatal(err)
	}
	w.api.World.SetFile("main", config.ProjectConfigPath, "merge: direct\n")
	hidden := func(rw http.ResponseWriter, _ *http.Request) {
		gt.WriteJSON(rw, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
	w.api.Route("GET /repos/"+gt.Repo, hidden)
	w.api.Route("GET /repos/"+gt.Repo+"/contents/"+config.ProjectConfigPath, hidden)

	got, err := w.gh.Doctor(ctx, w.srv.Projects)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range got[0].Lines {
		if l.Check == "project_config" {
			return l
		}
	}
	t.Fatalf("doctor holds no project_config line: %+v", got[0].Lines)
	return github.CheckLine{}
}

// TestDoctorTellsAHiddenRepositoryFromAMissingConfig is F-39: the App's
// token reads the whole repository as not found, though project config is
// on main. The project_config line must not send the operator to merge a
// file that exists. It names the status and that the token cannot reach
// the repository.
func TestDoctorTellsAHiddenRepositoryFromAMissingConfig(t *testing.T) {
	l := hiddenRepoConfigLine(t)
	if l.OK {
		t.Fatalf("project_config passed for a repository the token cannot read: %+v", l)
	}
	if strings.Contains(l.Detail, "merge "+config.ProjectConfigPath) {
		t.Errorf("project_config tells the operator to merge a file that is on main: %q", l.Detail)
	}
	if !strings.Contains(l.Detail, "404") || !strings.Contains(l.Detail, gt.Repo) {
		t.Errorf("project_config detail names no status and no repository: %q", l.Detail)
	}
}

// TestDoctorNamesTheTokenGrantForAHiddenRepository is F-39: the failed
// detail names the repositories and permissions that GitHub granted the
// token at its last mint, and never the token itself.
func TestDoctorNamesTheTokenGrantForAHiddenRepository(t *testing.T) {
	l := hiddenRepoConfigLine(t)
	want := "repositories " + gt.Repo + " and permissions actions:read, checks:read, contents:read, metadata:read, pull_requests:read, statuses:read"
	if !strings.Contains(l.Detail, want) {
		t.Errorf("project_config detail = %q, want it to hold %q", l.Detail, want)
	}
	if strings.Contains(l.Detail, "ghs_") {
		t.Errorf("project_config detail holds a token: %q", l.Detail)
	}
}
