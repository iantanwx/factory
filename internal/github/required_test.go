package github

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// baseConfig commits .loop/config.yaml to main on the remote and syncs
// the project's mirror, as the reactor's tick does.
func (e *env) baseConfig(doc string) {
	e.t.Helper()
	gt.Git(e.t, e.checkout, "checkout", "-q", "main")
	gt.Commit(e.t, e.checkout, "config", map[string]string{config.ProjectConfigPath: doc})
	gt.Git(e.t, e.checkout, "push", "-q", e.git.RemoteDir(), "main")
	gt.Git(e.t, e.checkout, "checkout", "-q", "work")
	if _, _, err := e.svc.SyncBase(context.Background(), e.project); err != nil {
		e.t.Fatal(err)
	}
}

// A required check that has not reported holds the head pending, so a
// head with no check is never green. Absence is never evidence of
// success (alpha premerge.ts:99-110). Red on any check is red.
func TestRequiredHoldsAHeadWithNoCheckPending(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	run := func(name, status, conclusion string) CheckRun {
		r := CheckRun{Name: name, HeadSHA: shaA, Status: status, Conclusion: conclusion, StartedAt: &at}
		return r
	}
	for _, c := range []struct {
		name     string
		runs     []CheckRun
		required []string
		state    string
		waiting  []string
		red      []string
	}{
		{"no check", nil, []string{"gates"}, CIPending, []string{"gates (not reported)"}, nil},
		{"no check and no name", nil, nil, CIPending, []string{"no check has reported"}, nil},
		{"another check green", []CheckRun{run("lint", "completed", "success")}, []string{"gates"}, CIPending,
			[]string{"gates (not reported)"}, nil},
		{"required running", []CheckRun{run("gates", "in_progress", "")}, []string{"gates"}, CIPending, []string{"gates"}, nil},
		{"required green", []CheckRun{run("gates", "completed", "success")}, []string{"gates"}, CIGreen, nil, nil},
		{"another check red", []CheckRun{run("gates", "completed", "success"), run("lint", "completed", "failure")},
			[]string{"gates"}, CIRed, nil, []string{"lint"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := Classify(shaA, c.runs, nil).Against(c.required)
			if v.State != c.state || !slices.Equal(v.Waiting, c.waiting) || !slices.Equal(v.Red, c.red) {
				t.Fatalf("Against(%v) = %+v; want %s, waiting %v, red %v", c.required, v, c.state, c.waiting, c.red)
			}
		})
	}
}

// The required checks are the ones project config names, with the
// base's ruleset checks. A project whose config and ruleset name none
// requires the gates job, so no merge is ever unguarded.
func TestRequiredChecksDefaultToGates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	got := func() []string {
		t.Helper()
		names, err := e.svc.RequiredChecks(ctx, e.project)
		if err != nil {
			t.Fatal(err)
		}
		return names
	}

	e.api.SetRules(nil)
	e.api.SetNoRulesStatus(http.StatusNotFound)
	if names := got(); !slices.Equal(names, []string{"gates"}) {
		t.Fatalf("no ruleset, no config: %v; want [gates]", names)
	}
	e.api.SetRules(gt.GoodRules())
	if names := got(); !slices.Equal(names, []string{"ci"}) {
		t.Fatalf("ruleset requires ci: %v; want [ci]", names)
	}
	e.baseConfig("merge:\n  mechanism: direct\n  required_checks: [acceptance, ci]\n")
	if names := got(); !slices.Equal(names, []string{"acceptance", "ci"}) {
		t.Fatalf("config names acceptance and ci: %v; want [acceptance ci]", names)
	}
}

// A code pass needs every check that project config requires at the
// head, besides the ruleset's checks: one that has not reported holds
// the pass back.
func TestPassCINeedsTheChecksProjectConfigRequires(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	e.baseConfig("merge:\n  mechanism: direct\n  required_checks: [acceptance]\n")

	w.SetRuns(shaA, gt.CheckRun("ci", shaA, "completed", "success", at))
	r := e.svc.PassCI(ctx, testProject, "tk-1", shaA)
	if r == nil || r.Code != cli.CodeCINotGreen || r.Rule != "ci_pending" {
		t.Fatalf("pass with acceptance not reported = %+v; want ci_pending", r)
	}
	w.SetRuns(shaA, gt.CheckRun("ci", shaA, "completed", "success", at), gt.CheckRun("acceptance", shaA, "completed", "success", at))
	if r := e.svc.PassCI(ctx, testProject, "tk-1", shaA); r != nil {
		t.Fatalf("pass with every required check green: %+v", r)
	}
}
