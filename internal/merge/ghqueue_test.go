package merge_test

import (
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
