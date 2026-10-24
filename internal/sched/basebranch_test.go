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

// A stage run starts from the branch its project config was read from,
// and its brief names that branch: a project whose default branch is not
// main runs against its own base.
func TestStageRunStartsFromTheProjectBranch(t *testing.T) {
	w := newWorld(t)
	rec := &baseRecorder{CheckoutHost: w.co}
	w.cfg.Checkouts = func(string) CheckoutHost { return rec }
	snap := snapshot(t, "stages:\n  implementer: {}\n  code-reviewer: {}\nmerge:\n  mechanism: direct\n", "")
	snap.Branch = "trunk"
	w.project.set(snap)
	w.rebuild()
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	if got := rec.last(); got != "trunk" {
		t.Fatalf("checkout base = %q, want trunk", got)
	}
	if b := readBrief(t, st.Checkout); b.BaseBranch != "trunk" {
		t.Fatalf("brief base = %q, want trunk", b.BaseBranch)
	}
}

// The coordinator's checkout starts from the recorded default branch of
// the project that holds it, and its brief names that branch.
func TestCoordinatorStartsFromTheRegisteredBranch(t *testing.T) {
	w := newWorld(t)
	rec := &baseRecorder{CheckoutHost: w.co}
	w.cfg.Checkouts = func(string) CheckoutHost { return rec }
	w.cfg.Projects = func() ([]config.Project, error) {
		return []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo, Branch: "trunk"}}, nil
	}
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	if err := w.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.tick()
	cs := w.s.Coordinator()
	if cs.Session == nil {
		t.Fatalf("no coordinator: %+v", cs)
	}
	if got := rec.last(); got != "trunk" {
		t.Fatalf("coordinator checkout base = %q, want trunk", got)
	}
	if b := readBrief(t, cs.Checkout); b.BaseBranch != "trunk" {
		t.Fatalf("coordinator brief base = %q, want trunk", b.BaseBranch)
	}
}
