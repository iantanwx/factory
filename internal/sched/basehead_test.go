package sched

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/machine"
)

// baseCheckouts makes each checkout a real git worktree of the project
// clone at spec.BaseBranch, resolved in the clone, as `orca worktree create
// --base-branch` does. gitCheckouts starts every checkout at origin/main
// and so never reads the spec's base.
type baseCheckouts struct{ *gitCheckouts }

func (b baseCheckouts) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	co, err := b.host.CreateCheckout(ctx, spec)
	if err != nil {
		return co, err
	}
	gitIn(b.t, b.repo, "worktree", "add", "-q", "-B", spec.Name, co.Path, spec.BaseBranch)
	return co, nil
}

// C-bug-1 (F-14): a run checkout starts at the head the remote holds for
// the base branch when the server creates it. The host clone's local main
// moves only when a person moves it, so it is never the base.
func TestRunCheckoutStartsAtTheRemoteBaseHead(t *testing.T) {
	w := newWorld(t)
	w.cfg.Checkouts = func(string) CheckoutHost { return baseCheckouts{w.co} }
	w.rebuild()
	stale, head := w.mergeElsewhere()

	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	if got := gitIn(t, st.Checkout, "rev-parse", "HEAD"); got != head {
		t.Fatalf("checkout HEAD = %s, want the remote base head %s (the clone's local main is %s)", got, head, stale)
	}
}

// mergeElsewhere merges a change into the origin from another clone, so
// the remote base moves on and the project clone's local main stays where
// it was. It returns both heads.
func (w *world) mergeElsewhere() (stale, head string) {
	t := w.t
	t.Helper()
	stale = gitIn(t, w.repo, "rev-parse", "main")
	other := filepath.Join(w.dir, "other")
	gitIn(t, w.dir, "clone", "-q", w.origin, other)
	if err := os.WriteFile(filepath.Join(other, "merged.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", "-A")
	gitIn(t, other, "commit", "-q", "-m", "merged")
	gitIn(t, other, "push", "-q", "origin", "main")
	head = gitIn(t, w.origin, "rev-parse", "main")
	if head == stale {
		t.Fatal("the remote base did not move")
	}
	return stale, head
}

// C-bug-1: the coordinator's checkout starts at the remote base head too.
func TestCoordinatorCheckoutStartsAtTheRemoteBaseHead(t *testing.T) {
	w := newWorld(t)
	w.cfg.Checkouts = func(string) CheckoutHost { return baseCheckouts{w.co} }
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	stale, head := w.mergeElsewhere()

	if err := w.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.tick()
	cs := w.s.Coordinator()
	if cs.Session == nil {
		t.Fatalf("no coordinator: %+v; %d coordinator alerts", cs, w.alerts(AlertCoordinator))
	}
	if got := gitIn(t, cs.Checkout, "rev-parse", "HEAD"); got != head {
		t.Fatalf("coordinator checkout HEAD = %s, want the remote base head %s (the clone's local main is %s)", got, head, stale)
	}
}

// C-bug-1: when the server cannot read the remote base head, the run does
// not start, an alert names it, and no checkout starts from the clone's
// local main.
func TestRunWithNoReadableBaseHeadFailsWithAnAlert(t *testing.T) {
	w := newWorld(t)
	w.cfg.Checkouts = func(string) CheckoutHost { return baseCheckouts{w.co} }
	w.rebuild()
	if err := os.Rename(w.origin, w.origin+".gone"); err != nil {
		t.Fatal(err)
	}

	item := w.ticket(machine.Todo)
	st, ref := w.s.Dispatch(context.Background(), prj, machine.StageImplementer)
	if ref == nil {
		t.Fatalf("a run started with no readable base head: %+v", st)
	}
	if n := w.alerts(AlertStartFailed); n != 1 {
		t.Fatalf("%d %s alerts, want 1", n, AlertStartFailed)
	}
	if n := w.count(`SELECT count(*) FROM checkouts`); n != 0 {
		t.Fatalf("%d checkouts, want none", n)
	}
	if got := w.status(item); got != machine.Todo {
		t.Fatalf("item status = %s, want %s", got, machine.Todo)
	}
}

// C-bug-1: when the server cannot read the remote base head, the
// coordinator does not start, and an alert names it.
func TestCoordinatorWithNoReadableBaseHeadFailsWithAnAlert(t *testing.T) {
	w := newWorld(t)
	w.cfg.Checkouts = func(string) CheckoutHost { return baseCheckouts{w.co} }
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	if err := os.Rename(w.origin, w.origin+".gone"); err != nil {
		t.Fatal(err)
	}

	if err := w.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.tick()
	if cs := w.s.Coordinator(); cs.Session != nil || cs.Checkout != "" {
		t.Fatalf("a coordinator started with no readable base head: %+v", cs)
	}
	if n := w.alerts(AlertCoordinator); n != 1 {
		t.Fatalf("%d %s alerts, want 1", n, AlertCoordinator)
	}
}
