package merge_test

import (
	"errors"
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
