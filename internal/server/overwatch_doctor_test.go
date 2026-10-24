package server

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/overwatch"
)

// driftLine is a failed install drift check.
var driftLine = github.CheckLine{Check: "install", Detail: "run loop install: the service unit /home/op/.config/systemd/user/loop.service is not installed"}

// serverPartOf returns the server part of the shipped part list.
func serverPartOf(t *testing.T) overwatch.Part {
	t.Helper()
	for _, p := range overwatch.Parts() {
		if p.Name == "server" {
			return p
		}
	}
	t.Fatal("overwatch.Parts() holds no server part")
	return overwatch.Part{}
}

// C19, the production World: Doctor returns the install drift check, then
// each project's GitHub doctor lines with the check named by its project.
// A failed install line makes the server part hold work.
func TestC19TheWorldDoctorIsInstallThenGitHub(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	w.api.World.SetFile("main", config.ProjectConfigPath, "merge: direct\n")
	w.srv.SetOverwatchDoctor(func(context.Context) github.CheckLine { return driftLine }, w.gh)

	got, err := w.srv.overwatch.world.Doctor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := w.gh.Doctor(ctx, w.srv.Projects)
	if err != nil || len(docs) != 1 {
		t.Fatalf("GitHub doctor = %v %v", docs, err)
	}
	want := []github.CheckLine{driftLine}
	for _, l := range docs[0].Lines {
		l.Check = "alpha/" + l.Check
		want = append(want, l)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Doctor =\n%+v\nwant\n%+v", got, want)
	}
	if !slices.ContainsFunc(got, func(l github.CheckLine) bool { return l.Check == "alpha/project_config" && l.OK }) {
		t.Fatalf("Doctor holds no passed alpha/project_config line: %+v", got)
	}

	r := overwatch.Sweep(ctx, w.srv.overwatch.world, []overwatch.Part{serverPartOf(t)})
	if r.Parts[0].Code != overwatch.Work || len(r.Parts[0].Lines) != 1 || r.Parts[0].Lines[0] !=
		"install failed: run loop install: the service unit /home/op/.config/systemd/user/loop.service is not installed (overwatch:server:install)" {
		t.Fatalf("the server part = %+v", r.Parts[0])
	}
}

// C19: a failed GitHub check makes the server part hold work, keyed by the
// project and the check.
func TestC19AFailedGitHubCheckIsWork(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	w.srv.SetOverwatchDoctor(func(context.Context) github.CheckLine {
		return github.CheckLine{Check: "install", OK: true, Detail: "the service, binaries, host config, key file and credential match loop install"}
	}, w.gh)
	r := overwatch.Sweep(ctx, w.srv.overwatch.world, []overwatch.Part{serverPartOf(t)})
	want := "alpha/project_config failed: merge .loop/config.yaml to main; loop project add proposes it (overwatch:server:alpha/project_config)"
	if r.Parts[0].Code != overwatch.Work || !slices.Equal(r.Parts[0].Lines, []string{want}) {
		t.Fatalf("the server part = %+v", r.Parts[0])
	}
}

// A server started with no install check fails the install line, and one
// with projects and no GitHub actor fails a github line. Neither passes in
// silence.
func TestTheWorldDoctorFailsAMissingCheck(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	got, err := w.srv.overwatch.world.Doctor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []github.CheckLine{
		{Check: "install", Detail: "this server was started with no install check; run it with loop serve"},
		{Check: "github", Detail: "the server holds no GitHub actor; set credentials.github_app in host config"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Doctor =\n%+v\nwant\n%+v", got, want)
	}
}
