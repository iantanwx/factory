package server

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/overwatch"
)

// partReport is the report of the part named name.
func partReport(t *testing.T, r overwatch.Report, name string) overwatch.PartReport {
	t.Helper()
	i := slices.IndexFunc(r.Parts, func(p overwatch.PartReport) bool { return p.Name == name })
	if i < 0 {
		t.Fatalf("the report holds no part %s: %+v", name, r)
	}
	return r.Parts[i]
}

// The production World serves the GitHub parts' reads through the GitHub
// service: open pull requests, the workflow runs of the base branch, and
// the jobs of each attempt of a rerun.
func TestTheProductionWorldServesTheGitHubReads(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	w.api.World.Add(&gt.PR{Head: "loop/alpha-x1", Base: "main"})
	w.api.World.Add(&gt.PR{Head: "loop/alpha-x1", Base: "release"})
	head := strings.Repeat("d", 40)
	// The flaky part reads a window back from the server's clock, so the
	// runs carry times near it. The rerun is at an older head.
	now := time.Now()
	w.api.World.AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: strings.Repeat("e", 40), Attempt: 2, Conclusion: "success",
		CreatedAt: now.Add(-time.Hour), Jobs: [][]gt.Job{{{Name: "test", Conclusion: "failure"}}, {{Name: "test", Conclusion: "success"}}}})
	w.api.World.AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: head, Conclusion: "failure", CreatedAt: now.Add(-time.Minute)})

	r := overwatch.Sweep(ctx, w.srv.overwatch.world, overwatch.Parts())
	pulls, main := partReport(t, r, "pulls"), partReport(t, r, "main")
	if pulls.Code != overwatch.Work || !slices.ContainsFunc(pulls.Lines, func(l string) bool {
		return strings.Contains(l, "overwatch:pulls:alpha:alpha-x1:work")
	}) {
		t.Fatalf("pulls = %+v", pulls)
	}
	if main.Code != overwatch.Work || !slices.ContainsFunc(main.Lines, func(l string) bool {
		return strings.Contains(l, "overwatch:main:alpha:ci-yml@"+head[:12])
	}) {
		t.Fatalf("main = %+v", main)
	}
	if flaky := partReport(t, r, "flaky"); flaky.Code != overwatch.Work || !slices.ContainsFunc(flaky.Lines, func(l string) bool {
		return strings.Contains(l, "overwatch:flaky:alpha:ci-yml/test")
	}) {
		t.Fatalf("flaky = %+v", flaky)
	}
}

// A server with no GitHub App reads no repository: its GitHub parts hold
// one note each, and no work.
func TestAServerWithNoGitHubAppReadsNoRepository(t *testing.T) {
	w := newHumanWorld(t, "")
	if gh := w.srv.overwatch.world.GitHub(); gh != nil {
		t.Fatalf("World.GitHub() = %v on a server with no GitHub setup", gh)
	}
	r := overwatch.Sweep(context.Background(), w.srv.overwatch.world, overwatch.Parts())
	for _, name := range []string{"pulls", "main", "ci", "flaky", "audit"} {
		pr := partReport(t, r, name)
		if pr.Code != overwatch.Quiet || len(pr.Lines) != 1 || !strings.Contains(pr.Lines[0], "no GitHub App") {
			t.Fatalf("%s = %+v", name, pr)
		}
	}
}
