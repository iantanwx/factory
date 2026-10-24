package merge_test

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/mergify"
)

func newMergify(t *testing.T) *env {
	return newEnv(t, func(e *env) merge.Mechanism { return mergify.New(e.svc) })
}

func queueComments(e *env, n int) int {
	c := 0
	for _, cm := range e.world.Comments(n) {
		if cm["body"] == mergify.QueueCommand {
			c++
		}
	}
	return c
}

// Another dequeue re-queues once for each head, then escalates. The
// re-queue removes dequeued and posts the queue command as the App,
// because a fresh label alone was dequeued again from Mergify's memory of
// the failed attempt (alpha #2850).
func TestMergifyRequeuesOncePerHeadThenEscalates(t *testing.T) {
	e := newMergify(t)
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	e.wantStatus(id, machine.Queued)

	dequeue := func() {
		e.world.Update(pr, func(p *gt.PR) {
			p.Labels = slices.DeleteFunc(p.Labels, func(l string) bool { return l == mergify.QueueLabel })
			p.Labels = append(p.Labels, mergify.DequeuedLabel)
		})
	}
	dequeue() // a flaky batch: no conflict, green CI at the head
	e.tick()
	e.wantStatus(id, machine.Queued)
	labels := e.world.Labels(pr.Number)
	if has(labels, mergify.DequeuedLabel) || !has(labels, mergify.QueueLabel) {
		t.Fatalf("labels after the re-queue = %v; want the queue label and no dequeued", labels)
	}
	if n := queueComments(e, pr.Number); n != 1 {
		t.Fatalf("%d queue commands; want 1", n)
	}
	e.tick() // nothing new: the PR waits in the queue
	if n := queueComments(e, pr.Number); n != 1 {
		t.Fatalf("%d queue commands after a quiet tick; want 1", n)
	}

	dequeue() // the same head fails again
	e.tick()
	it := e.wantStatus(id, machine.Escalated)
	if it.Fact(machine.FactResume) != machine.Todo {
		t.Fatalf("resume = %q", it.Fact(machine.FactResume))
	}
	if n := queueComments(e, pr.Number); n != 1 {
		t.Fatalf("%d queue commands; a second dequeue at one head must escalate, not re-queue", n)
	}
}

// A dequeued PR that is only behind its base keeps its review: Mergify
// tests it on the current base, so it re-queues as any other dequeue
// does (alpha 3668b32ae). While GitHub still computes the mergeable
// state, the job reads it again on the next tick before it re-queues.
func TestMergifyRequeuesABranchBehindTheBase(t *testing.T) {
	e := newMergify(t)
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	e.world.Update(pr, func(p *gt.PR) {
		p.Labels = append(p.Labels, mergify.DequeuedLabel)
		p.MergeableState = merge.MergeableUnknown
	})
	e.tick()
	e.wantStatus(id, machine.Queued)
	if n := queueComments(e, pr.Number); n != 0 {
		t.Fatalf("%d queue commands while the mergeable state is unknown; want none", n)
	}
	e.wantTracked(pr, "handed", "", 1)

	e.world.Update(pr, func(p *gt.PR) { p.MergeableState = merge.MergeableBehind })
	e.tick()
	e.wantStatus(id, machine.Queued)
	if n := queueComments(e, pr.Number); n != 1 {
		t.Fatalf("%d queue commands; want 1", n)
	}
	e.wantTracked(pr, "requeued", merge.Other, 2)
}

// `project add` on a repository with no Mergify config opens one template
// PR through the App, and a repeated add opens no second one.
func TestMergifyProjectAddOpensOneTemplatePR(t *testing.T) {
	e := newMergify(t)
	e.api.ServeApp("loop-fixture")
	for i := 0; i < 2; i++ {
		err := e.mech.Check(e.ctx, e.project)
		var setup *mergify.SetupError
		if !errors.As(err, &setup) || !strings.Contains(err.Error(), "#1") {
			t.Fatalf("add %d = %v; want a refusal that names the template PR", i, err)
		}
	}
	prs := e.world.PRs()
	if len(prs) != 1 || prs[0].Head != mergify.SetupBranch || prs[0].Base != "main" {
		t.Fatalf("PRs = %+v; want one template PR", prs)
	}
	if github.ItemOf(prs[0].Head) != "" {
		t.Fatalf("the setup branch %q reads as an item branch", prs[0].Head)
	}
	got, ok := e.world.File(mergify.SetupBranch, mergify.ConfigPath)
	want, err := mergify.Render(mergify.Template{Bot: "loop-fixture[bot]", Base: "main", Checks: []string{"ci"}})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != string(want) {
		t.Fatalf("the template PR holds %q; want the rendered template", got)
	}

	// Once the template is on the base, add passes and opens nothing.
	e.world.SetFile("main", mergify.ConfigPath, got)
	if err := e.mech.Check(e.ctx, e.project); err != nil {
		t.Fatalf("add with config = %v", err)
	}
	if n := len(e.world.PRs()); n != 1 {
		t.Fatalf("%d PRs; want 1", n)
	}
}

// On a plan with no rulesets, `project add` still opens the template PR,
// and the template requires the gates job: nothing else requires CI there.
func TestMergifyProjectAddOnAPlanWithNoRulesetsRequiresTheGatesJob(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		e := newMergify(t)
		e.api.ServeApp("loop-fixture")
		e.api.SetRules(nil)
		e.api.SetNoRulesStatus(status)
		err := e.mech.Check(e.ctx, e.project)
		var setup *mergify.SetupError
		if !errors.As(err, &setup) {
			t.Fatalf("%d: add = %v; want a refusal that names the template PR", status, err)
		}
		got, _ := e.world.File(mergify.SetupBranch, mergify.ConfigPath)
		want, err := mergify.Render(mergify.Template{Bot: "loop-fixture[bot]", Base: "main", Checks: []string{"gates"}})
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Fatalf("%d: the template PR holds %q; want the template that requires gates", status, got)
		}
	}
}

// The template requires each check that project config names, besides
// the ruleset's, so Mergify merges no head whose required check has not
// passed.
func TestMergifyProjectAddRequiresTheChecksProjectConfigNames(t *testing.T) {
	e := newMergify(t)
	e.api.ServeApp("loop-fixture")
	e.baseConfig("merge:\n  mechanism: mergify\n  required_checks: [acceptance]\n")
	var setup *mergify.SetupError
	if err := e.mech.Check(e.ctx, e.project); !errors.As(err, &setup) {
		t.Fatalf("add = %v; want a refusal that names the template PR", err)
	}
	got, _ := e.world.File(mergify.SetupBranch, mergify.ConfigPath)
	want, err := mergify.Render(mergify.Template{Bot: "loop-fixture[bot]", Base: "main", Checks: []string{"acceptance", "ci"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("the template PR holds %q; want the template that requires acceptance and ci", got)
	}
}

// Any of Mergify's config locations counts.
func TestMergifyProjectAddAcceptsEveryConfigLocation(t *testing.T) {
	for _, path := range []string{".mergify.yml", ".mergify/config.yml", ".github/mergify.yml"} {
		e := newMergify(t)
		e.world.SetFile("main", path, "queue_rules: []\n")
		if err := e.mech.Check(e.ctx, e.project); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if n := len(e.world.PRs()); n != 0 {
			t.Fatalf("%s: %d PRs opened", path, n)
		}
	}
}
