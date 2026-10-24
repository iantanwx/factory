package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// baseRecorder records the base branch of each checkout it creates.
type baseRecorder struct {
	CheckoutHost
	mu    sync.Mutex
	bases []string
}

func (r *baseRecorder) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	r.mu.Lock()
	r.bases = append(r.bases, spec.BaseBranch)
	r.mu.Unlock()
	return r.CheckoutHost.CreateCheckout(ctx, spec)
}

func (r *baseRecorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bases) == 0 {
		return ""
	}
	return r.bases[len(r.bases)-1]
}

func readBrief(t *testing.T, checkout string) brief.Brief {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// pushTrunk pushes a branch trunk to the origin from another clone, one
// commit ahead of main, and returns its head. A checkout that starts from
// trunk starts at that head (C-bug-1), and main's head is not it.
func (w *world) pushTrunk() string {
	t := w.t
	t.Helper()
	other := filepath.Join(w.dir, "trunk")
	gitIn(t, w.dir, "clone", "-q", w.origin, other)
	if err := os.WriteFile(filepath.Join(other, "trunk.txt"), []byte("trunk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", "-A")
	gitIn(t, other, "commit", "-q", "-m", "trunk")
	gitIn(t, other, "push", "-q", "origin", "HEAD:refs/heads/trunk")
	head := gitIn(t, w.origin, "rev-parse", "trunk")
	if head == gitIn(t, w.origin, "rev-parse", "main") {
		t.Fatal("trunk is main")
	}
	return head
}

// A stage run starts from the branch its project config was read from,
// and its brief names that branch: a project whose default branch is not
// main runs against its own base. The checkout starts at the head the
// remote holds for that branch (C-bug-1).
func TestStageRunStartsFromTheProjectBranch(t *testing.T) {
	w := newWorld(t)
	rec := &baseRecorder{CheckoutHost: w.co}
	w.cfg.Checkouts = func(string) CheckoutHost { return rec }
	snap := snapshot(t, "stages:\n  implementer: {}\n  code-reviewer: {}\nmerge:\n  mechanism: direct\n", "")
	snap.Branch = "trunk"
	w.project.set(snap)
	w.rebuild()
	trunk := w.pushTrunk()
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	if got := rec.last(); got != trunk {
		t.Fatalf("checkout base = %q, want the head of trunk %s", got, trunk)
	}
	if b := readBrief(t, st.Checkout); b.BaseBranch != "trunk" {
		t.Fatalf("brief base = %q, want trunk", b.BaseBranch)
	}
}

// The coordinator's checkout starts from the recorded default branch of
// the project that holds it, at the head the remote holds for it, and its
// brief names that branch.
func TestCoordinatorStartsFromTheRegisteredBranch(t *testing.T) {
	w := newWorld(t)
	rec := &baseRecorder{CheckoutHost: w.co}
	w.cfg.Checkouts = func(string) CheckoutHost { return rec }
	w.cfg.Projects = func() ([]config.Project, error) {
		return []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo, Branch: "trunk"}}, nil
	}
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	trunk := w.pushTrunk()
	if err := w.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.tick()
	cs := w.s.Coordinator()
	if cs.Session == nil {
		t.Fatalf("no coordinator: %+v", cs)
	}
	if got := rec.last(); got != trunk {
		t.Fatalf("coordinator checkout base = %q, want the head of trunk %s", got, trunk)
	}
	if b := readBrief(t, cs.Checkout); b.BaseBranch != "trunk" {
		t.Fatalf("coordinator brief base = %q, want trunk", b.BaseBranch)
	}
}
