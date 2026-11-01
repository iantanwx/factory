//go:build acceptance

package scenarios

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/sched"
)

// observed collects every transition each world applied, for the row
// catalogue.
var (
	observedMu sync.Mutex
	observed   []acceptance.Transition
)

// newWorld makes a world and records its transitions when the test ends.
func newWorld(t *testing.T, o acceptance.Options) *acceptance.World {
	t.Helper()
	w := acceptance.New(t, o)
	t.Cleanup(func() {
		if w.Srv == nil {
			return
		}
		ts := w.Transitions()
		observedMu.Lock()
		observed = append(observed, ts...)
		observedMu.Unlock()
	})
	return w
}

// passSimplify passes a ticket that waits in in_simplify on to review, as
// the dependency-advance job does in a project that runs no simplify
// stage. A ticket in any other state stays as it is.
func passSimplify(w *acceptance.World, item string) {
	w.T.Helper()
	if w.Status(item) != machine.InSimplify {
		return
	}
	res := w.Srv.Machine.Apply(context.Background(), machine.Request{Project: acceptance.Project, Item: item,
		Verb: machine.VerbSimplifyOff, Actor: machine.Server(sched.DependencyAdvanceJob)})
	if res.Refusal != nil {
		w.T.Fatalf("simplify_off %s: %+v", item, *res.Refusal)
	}
}

// createTicket makes a PRD-less ticket as the coordinator, on the
// operator's request, and returns its id.
func createTicket(w *acceptance.World, title, body string) string {
	w.T.Helper()
	return w.CreateTicket(title, body)
}

// implement runs the implementer on item by implementRun, then passes the
// ticket on to review by passSimplify. It returns the run and the pushed
// head.
func implement(w *acceptance.World, item string, files map[string]string) (acceptance.Run, string) {
	w.T.Helper()
	run, head := implementRun(w, item, files)
	passSimplify(w, item)
	return run, head
}

// implementRun starts an implementer run on the next ticket, commits files,
// pushes and opens the PR from the run's checkout, and finishes the run.
// It returns the run and the pushed head.
func implementRun(w *acceptance.World, item string, files map[string]string) (acceptance.Run, string) {
	w.T.Helper()
	run := w.Dispatch(machine.StageImplementer)
	if run.Item != item {
		w.T.Fatalf("the implementer took %s, want %s", run.Item, item)
	}
	head := w.Commit(run.Checkout, "implement "+item, files)
	w.Must(w.In(run.Checkout, "branch", "push"))
	w.Must(w.In(run.Checkout, "pr", "open", "--title", "Implement "+item, "--body", "Implements "+item+"."))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	return run, head
}

// review starts a code-reviewer run on item and submits verdict at the
// PR's current head, which the run reads with `loop pr show`. flags go to
// `review submit` as they are, such as a body that names each contract.
func review(w *acceptance.World, item, verdict string, flags ...string) acceptance.Run {
	w.T.Helper()
	run := w.Dispatch(machine.StageCodeReviewer)
	if run.Item != item {
		w.T.Fatalf("the code-reviewer took %s, want %s", run.Item, item)
	}
	head := w.Must(w.In(run.Checkout, "pr", "show")).Str("head_sha")
	w.Must(w.In(run.Checkout, append([]string{"review", "submit", item, "--verdict", verdict, "--head", head}, flags...)...))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	return run
}

// wantStatus fails the test unless item holds status.
func wantStatus(w *acceptance.World, item, status string) {
	w.T.Helper()
	if got := w.Status(item); got != status {
		w.T.Fatalf("%s is %s, want %s", item, got, status)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// readyToMerge takes a new ticket through implement, green CI and a review
// pass, and returns its id and the reviewed head.
func readyToMerge(w *acceptance.World, title, file string) (string, string) {
	w.T.Helper()
	id := createTicket(w, title, "Create `"+file+"` holding the line `"+title+"`.")
	_, head := implement(w, id, map[string]string{file: title + "\n"})
	w.CI(head, "success")
	review(w, id, "pass")
	wantStatus(w, id, machine.ReadyToMerge)
	return id, head
}

// merge runs the merge path until item is done: eligibility, the
// hand-off and the mechanism's poll, and the reactor.
func merge(w *acceptance.World, item string) {
	w.T.Helper()
	w.React()
	w.HandOff()
	w.HandOff()
	w.React()
	wantStatus(w, item, machine.Done)
	if n := w.DoneWriters(item); n != 1 {
		w.T.Fatalf("%d writers moved %s to done", n, item)
	}
}

// counted is the number of failures that count against the retry limit.
// A pending row is a run's reservation, not a failure.
func counted(w *acceptance.World) int {
	w.T.Helper()
	return w.Count(`SELECT count(*) FROM failures WHERE class IN ('failed', 'crashed', 'stalled')`)
}
