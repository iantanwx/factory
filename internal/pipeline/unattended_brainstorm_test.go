package pipeline_test

import (
	"strconv"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// drafting creates a PRD epic from the coordinator's request, and claims
// it for the unattended-brainstorm run id. It returns the epic and the
// run's token.
func (w *world) drafting(id string) (string, rpc.Principal) {
	w.t.Helper()
	epic := ok(w.t, w.call(w.coordinator(), "", "epic create", prdEpic(nil)))["epic"].(string)
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageUnattendedBrainstorm, RunID: id}); res.Refusal != nil {
		w.t.Fatalf("unattended-brainstorm claim: %+v", *res.Refusal)
	}
	return epic, w.run(id, epic, machine.StageUnattendedBrainstorm)
}

// runSubmit runs prd submit as pr at the checkout's head.
func (w *world) runSubmit(pr rpc.Principal) cli.Result {
	w.t.Helper()
	head := gt.Git(w.t, w.checkout, "rev-parse", "HEAD")
	return w.call(pr, "", "prd submit", headFlags(head, map[string]string{"path": prdPath, "title": "Watchlists"}))
}

// C10: an unattended-brainstorm run submits the PRD of the epic it holds.
// HEAD lands on loop/<epic>, one PR opens, and the epic moves to
// prd_review with prd_path, doc_pr and no owner. The project holds one epic before and
// after. A replay answers the same, and a first reject lands on
// prd_amending, as for a room's PRD.
func TestC10ARunSubmitsThePRDOfTheEpicItHolds(t *testing.T) {
	w := newWorld(t)
	epic, run := w.drafting("run-pa")
	if n := len(w.items(machine.KindEpic)); n != 1 {
		t.Fatalf("%d epics before the submit", n)
	}
	head := w.commitPRD(prdText)

	data := ok(t, w.runSubmit(run))
	n := int(data["pr"].(map[string]any)["number"].(float64))
	if data["epic"] != epic || data["status"] != machine.PRDReview {
		t.Fatalf("submit answered %v", data)
	}
	it := w.show(epic)
	if it.Status != machine.PRDReview || it.Owner != "" || it.Fact(machine.FactDocPR) != strconv.Itoa(n) ||
		it.Fact(machine.FactPRDPath) != prdPath {
		t.Fatalf("epic after submit: %+v", it)
	}
	if got := w.git.RemoteSHA(github.BranchFor(epic)); got != head {
		t.Fatalf("the PRD branch is at %q, want %s", got, head)
	}
	if w.pr(n).Head != github.BranchFor(epic) || w.pr(n).Title != "Watchlists" {
		t.Fatalf("PR = %+v", w.pr(n))
	}
	if stories, _ := data["stories"].([]any); len(stories) != 2 {
		t.Fatalf("stories = %v", data["stories"])
	}

	again := ok(t, w.runSubmit(run))
	if again["epic"] != epic || again["status"] != machine.PRDReview ||
		int(again["pr"].(map[string]any)["number"].(float64)) != n {
		t.Fatalf("a replay answered %v", again)
	}
	if got := len(w.items(machine.KindEpic)); got != 1 {
		t.Fatalf("%d epics after the submit", got)
	}
	if got := len(w.api.World.PRs()); got != 1 {
		t.Fatalf("%d PRs", got)
	}

	w.review(epic, "reject")
	if it := w.show(epic); it.Status != machine.PRDAmending || it.Fact(machine.FactPRDRejects) != "1" {
		t.Fatalf("after the first reject: %+v", it)
	}
}

// C11: prd submit refuses a run that may not submit, before any push. A
// run of another stage, a held epic not in prd_drafting, a stale epoch and
// a PRD with no story label each get their code and rule. The PRD branch
// stays empty, no PR opens, and the epic keeps its state.
func TestC11PRDSubmitRefusesARunThatMayNotSubmit(t *testing.T) {
	w := newWorld(t)
	epic, run := w.drafting("run-pa")
	w.commitPRD(prdText)

	other := run
	other.Stage = machine.StagePlanner
	stale := run
	stale.Epoch--
	needed, _ := w.drafting("run-pb")
	w.mustApply(machine.Request{Item: needed, Verb: "release", Actor: machine.Server("test")})
	notDrafting := w.run("run-pb", needed, machine.StageUnattendedBrainstorm)

	for _, c := range []struct {
		name       string
		pr         rpc.Principal
		code, rule string
	}{
		{"a planner run", other, cli.CodeCapabilityDenied, "prd_author_only"},
		{"an epic not in prd_drafting", notDrafting, cli.CodeTransitionNotAllowed, "not_drafting"},
		{"a stale epoch", stale, cli.CodeLeaseNotHeld, "stale_epoch"},
	} {
		if r := wantCode(t, w.runSubmit(c.pr), c.code); r.Rule != c.rule {
			t.Errorf("%s: rule %q, want %q", c.name, r.Rule, c.rule)
		}
	}
	w.commitPRD("# Watchlists\n\nNo stories here.\n")
	if r := wantCode(t, w.runSubmit(run), cli.CodeUsage); r.Rule != "no_story_labels" {
		t.Errorf("a PRD with no story label: rule %q, want no_story_labels", r.Rule)
	}

	for id, status := range map[string]string{epic: machine.PRDDrafting, needed: machine.PRDNeeded} {
		if got := w.git.RemoteSHA(github.BranchFor(id)); got != "" {
			t.Errorf("a refused submit pushed %s to %s", got, github.BranchFor(id))
		}
		if got := w.show(id).Status; got != status {
			t.Errorf("%s is %s after the refusals, want %s", id, got, status)
		}
	}
	if n := len(w.api.World.PRs()); n != 0 {
		t.Fatalf("a refused submit opened %d PRs", n)
	}
}
