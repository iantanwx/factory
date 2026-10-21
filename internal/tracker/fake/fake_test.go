package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/tracker"
	"github.com/iantanwx/loop/internal/tracker/fake"
)

func ev(seq int64, item, status string) tracker.Event {
	return tracker.Event{Seq: seq, Op: "update", Item: item, State: &tracker.Item{ID: item, Status: status}}
}

func seqs(evs []tracker.Event) []int64 {
	out := make([]int64, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Seq)
	}
	return out
}

func TestReplayedBatchAppliesOnce(t *testing.T) {
	ctx := context.Background()
	a := fake.New()
	batch := []tracker.Event{ev(1, "lp-1", "open"), ev(2, "lp-1", "in_progress")}
	for range 2 {
		if err := a.Apply(ctx, "p", batch); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	if got := seqs(a.Applied("p")); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("applied = %v, want [1 2]", got)
	}
	if a.Calls() != 2 {
		t.Fatalf("calls = %d, want 2", a.Calls())
	}
	if st := a.State("p")["lp-1"].Status; st != "in_progress" {
		t.Fatalf("state = %q, want in_progress", st)
	}
}

func TestDedupeIsPerProject(t *testing.T) {
	ctx := context.Background()
	a := fake.New()
	_ = a.Apply(ctx, "p1", []tracker.Event{ev(1, "lp-1", "open")})
	_ = a.Apply(ctx, "p2", []tracker.Event{ev(1, "lp-1", "open")})
	if len(a.Applied("p1")) != 1 || len(a.Applied("p2")) != 1 {
		t.Fatalf("applied p1=%d p2=%d, want 1 and 1", len(a.Applied("p1")), len(a.Applied("p2")))
	}
}

func TestFailNextAppliesNothing(t *testing.T) {
	ctx := context.Background()
	a := fake.New()
	boom := errors.New("boom")
	a.FailNext(1, boom)
	if err := a.Apply(ctx, "p", []tracker.Event{ev(1, "lp-1", "open")}); !errors.Is(err, boom) {
		t.Fatalf("Apply = %v, want boom", err)
	}
	if len(a.Applied("p")) != 0 {
		t.Fatalf("applied %d events after a failure", len(a.Applied("p")))
	}
	if err := a.Apply(ctx, "p", []tracker.Event{ev(1, "lp-1", "open")}); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
}

func TestLoseResultAppliesAndFails(t *testing.T) {
	ctx := context.Background()
	a := fake.New()
	a.LoseResultNext(1)
	if err := a.Apply(ctx, "p", []tracker.Event{ev(1, "lp-1", "open")}); err == nil {
		t.Fatal("Apply = nil, want an unknown result")
	}
	if len(a.Applied("p")) != 1 {
		t.Fatalf("applied = %d, want 1", len(a.Applied("p")))
	}
}

func TestRateLimitNext(t *testing.T) {
	a := fake.New()
	a.RateLimitNext(7 * time.Second)
	err := a.Reconcile(context.Background(), "p", nil)
	var rl *tracker.RateLimitError
	if !errors.Is(err, tracker.ErrRateLimited) || !errors.As(err, &rl) || rl.RetryAfter != 7*time.Second {
		t.Fatalf("Reconcile = %v, want a 7s rate limit", err)
	}
	if a.Reconciles("p") != 0 {
		t.Fatal("a rate-limited reconcile counted")
	}
}

func TestHangWaitsForContext(t *testing.T) {
	a := fake.New()
	a.SetHang(true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := a.Apply(ctx, "p", []tracker.Event{ev(1, "lp-1", "open")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Apply = %v, want deadline exceeded", err)
	}
	if len(a.Applied("p")) != 0 {
		t.Fatal("a hung call applied events")
	}
}

func TestReconcileReplacesState(t *testing.T) {
	ctx := context.Background()
	a := fake.New()
	_ = a.Apply(ctx, "p", []tracker.Event{ev(1, "lp-1", "open"), ev(2, "lp-2", "open")})
	items := []tracker.Item{{ID: "lp-2", Status: "closed"}, {ID: "lp-3", Status: "open"}}
	if err := a.Reconcile(ctx, "p", items); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := a.State("p")
	if _, ok := st["lp-1"]; ok || st["lp-2"].Status != "closed" || st["lp-3"].Status != "open" || len(st) != 2 {
		t.Fatalf("state = %+v", st)
	}
	if a.Reconciles("p") != 1 {
		t.Fatalf("reconciles = %d, want 1", a.Reconciles("p"))
	}
}

func TestDeleteEventRemovesItem(t *testing.T) {
	ctx := context.Background()
	a := fake.New()
	_ = a.Apply(ctx, "p", []tracker.Event{ev(1, "lp-1", "open"), {Seq: 2, Op: "delete", Item: "lp-1"}})
	if _, ok := a.State("p")["lp-1"]; ok {
		t.Fatal("deleted item still in state")
	}
}
