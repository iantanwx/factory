package github

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// A code pass names the full sha of the head, since loop/eligible is
// posted on that sha only. A repository whose plan has no rulesets, and
// whose config names no required check, requires the gates job: a head
// with no check, or with other checks only, is never green.
func TestPassCINeedsTheFullHeadAndTheGatesJobWithoutRulesets(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	want := func(r *cli.Refusal, code, rule string) {
		t.Helper()
		if r == nil || r.Code != code || r.Rule != rule {
			t.Fatalf("want %s %s, got %+v", code, rule, r)
		}
	}

	w.SetRuns(shaA, gt.CheckRun("ci", shaA, "completed", "success", at))
	want(e.svc.PassCI(ctx, testProject, "tk-1", shaA[:12]), cli.CodeUsage, "head_not_sha")

	e.api.SetRules(nil)
	e.api.SetNoRulesStatus(http.StatusNotFound)
	w.SetRuns(shaA)
	want(e.svc.PassCI(ctx, testProject, "tk-1", shaA), cli.CodeCINotGreen, "ci_pending")
	w.SetRuns(shaA, gt.CheckRun("lint", shaA, "completed", "success", at))
	want(e.svc.PassCI(ctx, testProject, "tk-1", shaA), cli.CodeCINotGreen, "ci_pending")
	w.SetRuns(shaA, gt.CheckRun("lint", shaA, "completed", "success", at), gt.CheckRun("gates", shaA, "completed", "success", at))
	if r := e.svc.PassCI(ctx, testProject, "tk-1", shaA); r != nil {
		t.Fatalf("green gates on a repository with no rulesets: %+v", r)
	}
	w.SetRuns(shaA, gt.CheckRun("gates", shaA, "completed", "success", at), gt.CheckRun("test", shaA, "completed", "failure", at))
	want(e.svc.PassCI(ctx, testProject, "tk-1", shaA), cli.CodeCINotGreen, "ci_failed")
}
