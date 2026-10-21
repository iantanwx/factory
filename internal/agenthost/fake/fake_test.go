package fake_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/fake"
)

func TestContract(t *testing.T) {
	agenthost.RunContract(t, func(t *testing.T) agenthost.ContractEnv {
		root := t.TempDir()
		h := fake.New(root)
		n := 0
		return agenthost.ContractEnv{
			Host: h,
			NewCheckout: func(testing.TB) string {
				n++
				co, err := h.CreateCheckout(context.Background(), agenthost.CheckoutSpec{Name: "co" + string(rune('a'+n))})
				if err != nil {
					t.Fatalf("CreateCheckout: %v", err)
				}
				return co.Path
			},
			Harness:     agenthost.Claude,
			Model:       "claude-sonnet-4-5",
			Effort:      "high",
			OutsideRoot: t.TempDir(),
			Driver:      h,
		}
	})
}

// env starts one live session in a fresh fake.
func env(t *testing.T, opts ...fake.Option) (*fake.Host, agenthost.Session) {
	t.Helper()
	root := t.TempDir()
	h := fake.New(root, opts...)
	co, err := h.CreateCheckout(context.Background(), agenthost.CheckoutSpec{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(t.TempDir(), "p.txt")
	if err := os.WriteFile(prompt, []byte("do the thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := h.Start(context.Background(), agenthost.StartSpec{
		Kind: agenthost.KindStage, Checkout: co.Path, Harness: agenthost.Codex,
		Model: "gpt-5-codex", Effort: "high", PromptFile: prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, s
}

func TestCodexEffortFlag(t *testing.T) {
	h, _ := env(t)
	argv := h.Launches()[0].Argv
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-c model_reasoning_effort=high") || strings.Contains(joined, "--effort") {
		t.Fatalf("codex argv = %q", argv)
	}
}

func TestWriteRestartsQuietPeriod(t *testing.T) {
	ctx := context.Background()
	h, s := env(t, fake.WithQuietPeriod(10*time.Minute))
	h.EndTurn(s.ID)
	h.Advance(9 * time.Minute)
	h.Write(s.ID)
	h.Advance(9 * time.Minute)
	if r, _ := h.Ended(ctx, s); r.Ended {
		t.Fatalf("a write inside the quiet period did not restart it: %+v", r)
	}
	h.Advance(time.Minute)
	if r, _ := h.Ended(ctx, s); !r.Ended || r.Cause != agenthost.EndQuiet {
		t.Fatalf("Ended = %+v; want ended by quiet", r)
	}
}

func TestExitEndsAndSettlesExited(t *testing.T) {
	ctx := context.Background()
	h, s := env(t)
	h.Exit(s.ID, 3)
	st, err := h.Settle(ctx, s.ID)
	if err != nil || st.Liveness != agenthost.Exited || st.ExitCode == nil || *st.ExitCode != 3 {
		t.Fatalf("Settle = %+v, %v", st, err)
	}
	r, _ := h.Ended(ctx, s)
	if !r.Ended || r.Cause != agenthost.EndExited {
		t.Fatalf("Ended = %+v; want exited", r)
	}
	inv, _ := h.Inventory(ctx)
	if got := inv.Lookup(s); got != agenthost.Absent {
		t.Fatalf("Lookup of an exited session = %s; want absent", got)
	}
}

func TestRestartKeepingHandlesReadopts(t *testing.T) {
	ctx := context.Background()
	h, s := env(t)
	h.Restart(true)
	inv, _ := h.Inventory(ctx)
	if inv.Epoch == s.Epoch {
		t.Fatal("restart kept the epoch")
	}
	if got := inv.Lookup(s); got != agenthost.Present {
		t.Fatalf("Lookup after orca-serve-style restart = %s; want present", got)
	}
	moved, ok := inv.Readopted(s)
	if !ok || moved.Epoch != inv.Epoch {
		t.Fatalf("Readopted = %+v, %v", moved, ok)
	}
	if st, err := h.Settle(ctx, s.ID); err != nil || st.Liveness != agenthost.Live {
		t.Fatalf("Settle after restart = %+v, %v", st, err)
	}
	// Once re-adopted, a later absence in the new epoch reads absent.
	if err := h.Close(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	inv, _ = h.Inventory(ctx)
	if got := inv.Lookup(moved); got != agenthost.Absent {
		t.Fatalf("Lookup after close in the re-adopted epoch = %s; want absent", got)
	}
	if got := inv.Lookup(s); got != agenthost.PresenceUnknown {
		t.Fatalf("Lookup with the old epoch = %s; want unknown", got)
	}
}

func TestRestartWithNewHandlesStaysUnknown(t *testing.T) {
	ctx := context.Background()
	h, s := env(t)
	h.Restart(false)
	st, err := h.Settle(ctx, s.ID)
	if st.Liveness != agenthost.Unknown || !errors.Is(err, agenthost.ErrStaleHandle) {
		t.Fatalf("Settle of the old handle = %+v, %v; want unknown, stale", st, err)
	}
	inv, _ := h.Inventory(ctx)
	moved := s
	moved.Epoch = inv.Epoch
	if got := inv.Lookup(moved); got != agenthost.PresenceUnknown {
		t.Fatalf("Lookup with a same-incarnation trace = %s; want unknown", got)
	}
	if len(inv.Traces(s)) != 1 {
		t.Fatalf("Traces = %+v; want the new handle", inv.Traces(s))
	}
	if err := h.Notify(ctx, s.ID, "x"); !errors.Is(err, agenthost.ErrNotifyRefused) {
		t.Fatalf("Notify of the old handle = %v; want refused", err)
	}
}

// TestDaemonDeathNewIncarnationIsNotTheRun: after DaemonDeath each open
// session's handle and PTY name a fresh shell of another incarnation. That
// row is never the run.
func TestDaemonDeathNewIncarnationIsNotTheRun(t *testing.T) {
	ctx := context.Background()
	h, s := env(t)
	h.DaemonDeath()
	cur, ok := h.CurrentID(s.ID)
	if !ok || cur.Terminal != s.ID.Terminal || cur.Incarnation == s.ID.Incarnation {
		t.Fatalf("CurrentID = %+v, %v; want the same handle under another incarnation", cur, ok)
	}
	st, err := h.Settle(ctx, s.ID)
	if st.Liveness != agenthost.Unknown || !errors.Is(err, agenthost.ErrStaleHandle) {
		t.Fatalf("Settle = %+v, %v; want unknown, stale", st, err)
	}
	if r, err := h.Ended(ctx, s); r.Ended || err == nil {
		t.Fatalf("Ended = %+v, %v", r, err)
	}
	inv, _ := h.Inventory(ctx)
	if inv.Epoch != s.Epoch {
		t.Fatalf("a daemon death moved the epoch: %s -> %s", s.Epoch, inv.Epoch)
	}
	if got := inv.Lookup(s); got != agenthost.PresenceUnknown {
		t.Fatalf("Lookup = %s; want unknown from the PTY trace", got)
	}
	if _, ok := inv.Readopted(s); ok {
		t.Fatal("Readopted took the fresh shell as the run")
	}
	if err := h.Notify(ctx, s.ID, "x"); !errors.Is(err, agenthost.ErrNotifyRefused) {
		t.Fatalf("Notify = %v; want refused", err)
	}
	if st, err := h.Stop(ctx, s.ID); st.Liveness == agenthost.Exited || !errors.Is(err, agenthost.ErrExitUnproven) {
		t.Fatalf("Stop = %+v, %v", st, err)
	}
}

func TestDownHostIsUnknownNeverExited(t *testing.T) {
	ctx := context.Background()
	h, s := env(t)
	h.SetDown(true)
	if st, err := h.Settle(ctx, s.ID); st.Liveness != agenthost.Unknown || !errors.Is(err, agenthost.ErrHostUnavailable) {
		t.Fatalf("Settle = %+v, %v", st, err)
	}
	inv, err := h.Inventory(ctx)
	if err == nil || inv.Complete || inv.Lookup(s) == agenthost.Absent {
		t.Fatalf("Inventory = %+v, %v", inv, err)
	}
	if st, err := h.Stop(ctx, s.ID); st.Liveness == agenthost.Exited || !errors.Is(err, agenthost.ErrExitUnproven) {
		t.Fatalf("Stop = %+v, %v", st, err)
	}
	if r, err := h.Ended(ctx, s); r.Ended || err == nil {
		t.Fatalf("Ended = %+v, %v", r, err)
	}
}

func TestRemoveCheckoutRefusesBusyCheckout(t *testing.T) {
	ctx := context.Background()
	h, s := env(t)
	if err := h.RemoveCheckout(ctx, s.Checkout); !errors.Is(err, agenthost.ErrCheckoutBusy) {
		t.Fatalf("RemoveCheckout with a live session = %v", err)
	}
	if err := h.Close(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	h.SetInventoryIncomplete(true)
	if err := h.RemoveCheckout(ctx, s.Checkout); !errors.Is(err, agenthost.ErrCheckoutBusy) {
		t.Fatalf("RemoveCheckout on an incomplete inventory = %v", err)
	}
	h.SetInventoryIncomplete(false)
	if err := h.RemoveCheckout(ctx, s.Checkout); err != nil || h.CheckoutExists(s.Checkout) {
		t.Fatalf("RemoveCheckout = %v", err)
	}
}

func TestQuota(t *testing.T) {
	ctx := context.Background()
	h, _ := env(t)
	if _, err := h.Quota(ctx, agenthost.Claude); !errors.Is(err, agenthost.ErrQuotaUnavailable) {
		t.Fatalf("Quota unset = %v", err)
	}
	h.SetQuota(agenthost.Quota{Harness: agenthost.Claude, OK: true, Windows: []agenthost.QuotaWindow{{Name: "weekly", UsedPercent: 91}}})
	q, err := h.Quota(ctx, agenthost.Claude)
	if err != nil || !q.OK || q.Windows[0].UsedPercent != 91 {
		t.Fatalf("Quota = %+v, %v", q, err)
	}
}

func TestStartRefusesMissingPromptFile(t *testing.T) {
	h := fake.New(t.TempDir())
	co, _ := h.CreateCheckout(context.Background(), agenthost.CheckoutSpec{Name: "w"})
	_, err := h.Start(context.Background(), agenthost.StartSpec{
		Kind: agenthost.KindRoom, Checkout: co.Path, Harness: agenthost.Claude,
		Model: "m", PromptFile: "/nonexistent/prompt.txt",
	})
	if !errors.Is(err, agenthost.ErrBadSpec) {
		t.Fatalf("Start = %v; want ErrBadSpec", err)
	}
}
