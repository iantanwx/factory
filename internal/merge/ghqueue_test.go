package merge_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/ghqueue"
)

func newQueue(t *testing.T) *env {
	return newEnv(t, func(e *env) merge.Mechanism { return ghqueue.New(e.svc) })
}

func group(n string, parent string) string { return "gh-readonly-queue/main/pr-" + n + "-" + parent }

func posts(e *env, sha string) int {
	return len(slices.DeleteFunc(e.world.Posted(), func(p string) bool { return p != sha+" loop/eligible success" }))
}

// GitHub checks required statuses on the merge-group commit. The server
// posts loop/eligible on a group commit only when every PR in the group
// is eligible at its queued head (KTD29).
func TestGitHubQueuePostsEligibleOnlyOnAGroupOfEligiblePRs(t *testing.T) {
	e := newQueue(t)
	a, prA := e.reviewed(sha('1'), true)
	b, prB := e.reviewed(sha('2'), true)
	e.tick()
	e.wantStatus(a, machine.Queued)
	e.wantStatus(b, machine.Queued)
	if q := e.world.Queue(); !slices.Equal(q, []int{prA.Number, prB.Number}) {
		t.Fatalf("queue = %v", q)
	}

	// One PR with no review pass, which a person queued by hand.
	c, res := e.m.Create(e.ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindTicket, Title: "c", Verb: "create",
		Actor: machine.Server("test")})
	e.mustOK(res)
	_, res = e.m.Claim(e.ctx, machine.ClaimRequest{Project: testProject, Item: c, Stage: machine.StageImplementer, RunID: "run-c"})
	e.mustOK(res)
	e.apply(c, "pr_open", map[string]string{"head": sha('3')})
	e.apply(c, machine.VerbSimplifyOff, nil) // the project runs no simplify stage
	e.world.Add(&gt.PR{Head: github.BranchFor(c), HeadSHA: sha('3')})
	d, _ := e.reviewed(sha('4'), true)
	e.tick()
	e.wantStatus(d, machine.Queued)

	// Groups chain: each group's branch names the commit it builds on.
	ga, gb, gc, gd := sha('a'), sha('c'), sha('d'), sha('e')
	e.world.AddGroup(group("1", gt.BaseSHA), ga)
	e.world.AddGroup(group("2", ga), gb)
	e.world.AddGroup(group("3", gb), gc)
	e.world.AddGroup(group("4", gc), gd)
	e.world.AddGroup("gh-readonly-queue/main/unreadable", sha('f'))
	e.tick()
	e.tick()
	if posts(e, ga) != 1 || posts(e, gb) != 1 {
		t.Fatalf("posts = %v; want loop/eligible once on each all-eligible group", e.world.Posted())
	}
	for _, s := range []string{gc, gd, sha('f')} {
		if posts(e, s) != 0 {
			t.Fatalf("loop/eligible posted on %s, a group that holds an ineligible or unknown PR: %v", s, e.world.Posted())
		}
	}
}

// TestC29AMergeGroupCommitGetsANeutralLoopQABeforeLoopEligible: no QA run
// reads a merge-group commit, so the sweep posts a neutral loop/qa there
// before loop/eligible. A group whose loop/qa GitHub refuses gets no
// loop/eligible.
func TestC29AMergeGroupCommitGetsANeutralLoopQABeforeLoopEligible(t *testing.T) {
	e := newQueue(t)
	e.reviewed(sha('1'), true)
	e.tick()
	ga := sha('a')
	e.world.AddGroup(group("1", gt.BaseSHA), ga)
	before := len(e.api.Calls())
	e.tick()
	calls := e.api.Calls()[before:]
	qa := slices.Index(calls, "POST /repos/"+gt.Repo+"/check-runs")
	eligible := slices.Index(calls, "POST /repos/"+gt.Repo+"/statuses/"+ga)
	if qa < 0 || eligible < 0 || qa > eligible {
		t.Fatalf("calls %q; want the check run post before the loop/eligible post on %s", calls, ga)
	}
	if runs := e.world.CheckRunsAt(ga); len(runs) != 1 || runs[0]["name"] != github.QAContext || runs[0]["conclusion"] != "neutral" {
		t.Fatalf("check runs at %s = %v, want one neutral loop/qa", ga, runs)
	}

	refused := newQueue(t)
	refused.reviewed(sha('1'), true)
	refused.tick()
	refused.world.AddGroup(group("1", gt.BaseSHA), ga)
	refused.api.Route("POST /repos/"+gt.Repo+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		gt.WriteJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by integration"})
	})
	_ = refused.job.TickProject(refused.ctx, testProject)
	if posts(refused, ga) != 0 {
		t.Fatalf("loop/eligible posted on %s while GitHub refuses check runs: %v", ga, refused.world.Posted())
	}
}

// A group whose base commit is neither the base branch nor another
// group's commit has an unknown composition, and never gets the status.
func TestGitHubQueueSkipsAGroupOfUnknownComposition(t *testing.T) {
	e := newQueue(t)
	e.reviewed(sha('1'), true)
	e.tick()
	e.world.AddGroup(group("1", sha('9')), sha('a'))
	e.tick()
	if posts(e, sha('a')) != 0 {
		t.Fatalf("posted on a group of unknown composition: %v", e.world.Posted())
	}
}

// `project add` refuses github-queue when the default branch has no
// merge-queue rule, and the message names the missing rule.
func TestGitHubQueueProjectAddNeedsAMergeQueueRule(t *testing.T) {
	e := newQueue(t)
	err := e.mech.Check(e.ctx, e.project)
	if err == nil || !strings.Contains(err.Error(), "merge_queue") || !strings.Contains(err.Error(), "main") {
		t.Fatalf("add with no merge-queue rule = %v; want a refusal that names the rule", err)
	}
	e.api.SetRules(append(gt.GoodRules(), map[string]any{"type": "merge_queue", "parameters": map[string]any{"merge_method": "MERGE"}}))
	if err := e.mech.Check(e.ctx, e.project); err != nil {
		t.Fatalf("add with the rule = %v", err)
	}
}

// GitHub's merge queue merges once the ruleset's required checks pass,
// and the loop cannot add a check to that list. So `project add` refuses
// github-queue when the ruleset leaves out a check the loop requires: the
// one project config names, or gates when nothing names one.
func TestGitHubQueueProjectAddNeedsTheRulesetToRequireEachRequiredCheck(t *testing.T) {
	queue := map[string]any{"type": "merge_queue", "parameters": map[string]any{"merge_method": "MERGE"}}
	e := newQueue(t)
	e.api.SetRules([]any{queue})
	err := e.mech.Check(e.ctx, e.project)
	if err == nil || !strings.Contains(err.Error(), "required_status_checks") || !strings.Contains(err.Error(), "gates") {
		t.Fatalf("add with a ruleset that requires no check = %v; want a refusal that names gates", err)
	}
	e.api.SetRules(append(gt.GoodRules(), queue))
	e.baseConfig("merge:\n  mechanism: github-queue\n  required_checks: [acceptance]\n")
	err = e.mech.Check(e.ctx, e.project)
	if err == nil || !strings.Contains(err.Error(), "acceptance") || strings.Contains(err.Error(), "ci,") {
		t.Fatalf("add with a ruleset that leaves out acceptance = %v; want a refusal that names acceptance only", err)
	}
}
