//go:build acceptance

package scenarios

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gates"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// The F1 fixture project. Its test gate fails until greeting.txt holds
// the greeting, so the code-reviewer can prove the check red at the merge
// base. Every project runs every stack gate, so the others run true. The
// job runs the compiled binary outside this directory, so the files are
// inline. Each stage fires once a year, so a tick on the real clock starts
// no run that the scenario does not dispatch. Simplify is on by default,
// and F1 runs it.
const (
	f1Check  = "#!/bin/sh\ntest \"$(cat greeting.txt 2>/dev/null)\" = \"hello, world\"\n"
	f1Config = `merge:
  mechanism: direct
gates:
  - name: format
    run: "true"
  - name: lint
    run: "true"
  - name: audit
    run: "true"
  - name: typecheck
    run: "true"
  - name: test
    run: sh check.sh
  - name: build
    run: "true"
stages:
  implementer:
    schedule: "0 0 1 1 *"
  simplify:
    schedule: "0 0 1 1 *"
  code-reviewer:
    schedule: "0 0 1 1 *"
  qa:
    enabled: false
`
	f1Title = "Add a greeting"
	f1Body  = "Create `greeting.txt` holding the one line `hello, world`, so that `sh check.sh` passes."
)

// The waits of F1 on real orca. A scripted run ends before its dispatch
// returns, so F1 with the fake host waits 0.
const (
	f1Poll = 15 * time.Second
	f1Wait = 30 * time.Minute
)

// The real implementer of F1 runs loop gates before it pushes, and a stack
// gate that the fixture config names no command for refuses the push. So
// the fixture passes every stack gate once greeting.txt holds the greeting.
// This runs with no orca, so CI proves the fixture the lane runs.
func TestF1FixtureConfigPassesEveryStackGate(t *testing.T) {
	dir := t.TempDir()
	gt.Git(t, dir, "init", "-q", "-b", "main")
	base := gt.Commit(t, dir, "seed the fixture project", map[string]string{config.ProjectConfigPath: f1Config, "check.sh": f1Check})
	gt.Commit(t, dir, "add the greeting", map[string]string{"greeting.txt": "hello, world\n"})
	cfg, err := config.ParseProjectConfig([]byte(f1Config))
	if err != nil {
		t.Fatal(err)
	}
	var stack []gates.Gate
	for _, g := range gates.List(cfg) {
		if g.Stack {
			stack = append(stack, g)
		}
	}
	var out strings.Builder
	results := gates.Run(context.Background(), &gates.Repo{Root: dir, Base: base, Config: cfg, Out: &out}, stack)
	var passed []string
	for _, r := range results {
		if r.OK {
			passed = append(passed, r.Gate)
		}
	}
	if want := []string{"format", "lint", "audit", "typecheck", "test", "build"}; !slices.Equal(passed, want) {
		t.Fatalf("the F1 fixture passed the stack gates %q, want %q\n%s", passed, want, out.String())
	}
}

// f1Path takes item from claim to merge under f1Config, in the steps of F1
// on real orca. It dispatches each stage, and act does the run's work: a
// real agent's session, or a scripted run. It returns the reviewed head.
func f1Path(w *acceptance.World, item string, wait time.Duration, act func(acceptance.Run)) string {
	w.T.Helper()
	step := func(stage, want, from string) {
		w.T.Helper()
		run := w.Dispatch(stage)
		if run.Item != item {
			w.T.Fatalf("the %s took %s, want %s", stage, run.Item, item)
		}
		act(run)
		waitFor(w, item, want, from, wait)
	}
	step(machine.StageImplementer, machine.InSimplify, machine.InProgress)
	step(machine.StageSimplify, machine.InReview, machine.InSimplify)
	head := w.Show(item).Fact(machine.FactHead)
	if head == "" || w.Git.RemoteSHA("loop/"+item) != head {
		w.T.Fatalf("the head %q after simplify is not the remote branch's %q", head, w.Git.RemoteSHA("loop/"+item))
	}

	w.CI(head, "success")
	w.React()
	step(machine.StageCodeReviewer, machine.ReadyToMerge, machine.InReview)
	if got := w.Show(item).Fact(machine.FactReviewedHead); got != head {
		w.T.Fatalf("the code-reviewer passed %s, want the head %s after simplify", got, head)
	}

	w.React()
	if !slices.Contains(w.API.World.Posted(), head+" "+gt.Eligible+" success") {
		w.T.Fatalf("no eligibility at the reviewed head: %v", w.API.World.Posted())
	}
	w.HandOff()
	w.HandOff()
	w.React()
	wantStatus(w, item, machine.Done)
	if merges := w.API.World.Merges(); len(merges) != 1 || merges[0].SHA != head || merges[0].Status != 200 {
		w.T.Fatalf("merges = %+v, want one at the reviewed head %s", merges, head)
	}
	return head
}

// waitFor ticks the scheduler until item reaches want, each f1Poll for up
// to wait. A status other than want or from fails at once, because a
// reject or a red build would need a session that F1 does not declare.
func waitFor(w *acceptance.World, item, want, from string, wait time.Duration) {
	w.T.Helper()
	for end := time.Now().Add(wait); ; time.Sleep(f1Poll) {
		w.Tick()
		switch got := w.Status(item); got {
		case want:
			return
		case from:
		default:
			w.T.Fatalf("%s is %s, want %s", item, got, want)
		}
		if !time.Now().Before(end) {
			w.T.Fatalf("%s did not reach %s in %s: it is %s", item, want, wait, w.Status(item))
		}
	}
}

// C-bug-1: F1's path with the fake host and scripted runs, so CI proves
// the path that the orca lane runs. A stage that f1Config turns on and F1
// does not dispatch leaves the ticket short of review, and fails this.
func TestF1PathRunsEveryStageOfItsFixtureConfig(t *testing.T) {
	w := newWorld(t, acceptance.Options{Config: f1Config, Files: map[string]string{"check.sh": f1Check}})
	id := createTicket(w, f1Title, f1Body)
	var last string
	head := f1Path(w, id, 0, func(run acceptance.Run) {
		switch run.Stage {
		case machine.StageImplementer:
			last = w.Commit(run.Checkout, "implement "+id, map[string]string{"greeting.txt": "hello, world\n\n"})
			w.Must(w.In(run.Checkout, "branch", "push"))
			w.Must(w.In(run.Checkout, "pr", "open", "--title", f1Title, "--body", "Implements "+id+"."))
		case machine.StageSimplify:
			syncItem(w, run.Checkout)
			last = w.Commit(run.Checkout, "simplify "+id, map[string]string{"greeting.txt": "hello, world\n"})
			w.Must(w.In(run.Checkout, "branch", "push"))
			w.Must(w.In(run.Checkout, "pr", "open", "--title", f1Title))
		case machine.StageCodeReviewer:
			w.Must(w.In(run.Checkout, "review", "submit", id, "--verdict", "pass", "--head", last))
		default:
			t.Fatalf("F1 dispatched the %s stage", run.Stage)
		}
		w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	})
	if head != last {
		t.Fatalf("the merged head is %s, want the simplify head %s", head, last)
	}
}
