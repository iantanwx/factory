//go:build beads

package beadsx_test

// The contract suite on the pinned bd 1.3.1 and Dolt 2.2.0. Each test makes
// its own state directory, starts its own supervised Dolt on a socket in it,
// and stops that Dolt by its own PID at the end.
//
// Run: go test -tags beads ./internal/beadsx/...
// LOOP_BD and LOOP_DOLT override the binary paths.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
)

func binPath(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

var (
	bdBin   = binPath("LOOP_BD", "/home/operator/.cache/loop-dev/bin/bd")
	doltBin = binPath("LOOP_DOLT", "/home/operator/.cache/loop-dev/bin/dolt")
)

type instance struct {
	layout beadsx.Layout
	sup    *beadsx.Supervisor
}

// startInstance makes a short state directory (a unix socket path has a
// length limit) and starts a supervised Dolt in it.
func startInstance(t *testing.T, cfg beadsx.DoltConfig) *instance {
	t.Helper()
	dir, err := os.MkdirTemp("", "bx")
	if err != nil {
		t.Fatal(err)
	}
	layout := beadsx.Layout{StateDir: dir}
	cfg.Binary, cfg.Dir = doltBin, layout.DoltDir()
	sup, err := beadsx.NewSupervisor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("start dolt: %v", err)
	}
	t.Cleanup(func() {
		pid := sup.PID()
		sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := sup.Stop(sctx); err != nil {
			t.Errorf("stop dolt: %v", err)
		}
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			t.Errorf("dolt pid %d still runs after Stop", pid)
		}
		if t.Failed() {
			if b, err := os.ReadFile(filepath.Join(layout.DoltDir(), "dolt.log")); err == nil {
				t.Logf("dolt.log tail:\n%s", tail(string(b), 2000))
			}
		}
		_ = os.RemoveAll(dir)
	})
	return &instance{layout: layout, sup: sup}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func (in *instance) workspace(t *testing.T, project string) *beadsx.BD {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	b, err := beadsx.EnsureWorkspace(ctx, beadsx.WorkspaceSpec{
		BD: bdBin, Layout: in.layout, Project: project, Socket: in.sup.Socket(),
		Statuses: testStatuses, Types: testTypes,
	})
	if err != nil {
		t.Fatalf("workspace %s: %v", project, err)
	}
	return b
}

func TestContractReal(t *testing.T) {
	in := startInstance(t, beadsx.DoltConfig{})
	b := in.workspace(t, "ct")
	runContract(t, b)
}

// waitRestart waits for the next restart event.
func waitRestart(t *testing.T, sup *beadsx.Supervisor, d time.Duration) beadsx.RestartEvent {
	t.Helper()
	select {
	case ev := <-sup.Restarts():
		return ev
	case <-time.After(d):
		t.Fatalf("no restart within %s", d)
	}
	return beadsx.RestartEvent{}
}

// A stop condition of the plan: an update bd reported as done must survive
// a SIGKILL of the sql-server right after it.
func TestDurabilityAfterSIGKILL(t *testing.T) {
	in := startInstance(t, beadsx.DoltConfig{MinBackoff: 100 * time.Millisecond})
	b := in.workspace(t, "du")
	ctx := context.Background()
	is, err := b.Create(ctx, beadsx.CreateSpec{Title: "durable", Type: "ticket"})
	if err != nil {
		t.Fatal(err)
	}
	prev := "open"
	for i := 1; i <= 10; i++ {
		next := []string{"implementing", "in_review"}[i%2]
		if _, err := b.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: prev, Status: next,
			SetMetadata: map[string]string{beadsx.MetaIntentSeq: fmt.Sprint(i)}}); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
		pid := in.sup.PID()
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		waitRestart(t, in.sup, 90*time.Second)
		got, err := b.Get(ctx, is.ID)
		if err != nil {
			t.Fatalf("read after restart %d: %v", i, err)
		}
		seq, _ := got.IntentSeq()
		if got.Status != next || seq != int64(i) {
			t.Fatalf("STOP CONDITION: update %d lost after SIGKILL: status %q seq %d", i, got.Status, seq)
		}
		prev = next
	}
}

func TestRestartGapIsUnavailable(t *testing.T) {
	// A long first backoff holds the gap open.
	in := startInstance(t, beadsx.DoltConfig{MinBackoff: 4 * time.Second, HealthInterval: 200 * time.Millisecond})
	b := in.workspace(t, "gp")
	ctx := context.Background()
	is, err := b.Create(ctx, beadsx.CreateSpec{Title: "gap", Type: "ticket"})
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(in.sup.PID(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	_, err = b.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "implementing"})
	if !errors.Is(err, beadsx.ErrUnavailable) || errors.Is(err, beadsx.ErrCASMiss) {
		t.Fatalf("update in the gap: %v, want ErrUnavailable", err)
	}
	if _, err := b.Get(ctx, is.ID); !errors.Is(err, beadsx.ErrUnavailable) {
		t.Fatalf("get in the gap: %v, want ErrUnavailable", err)
	}
	ev := waitRestart(t, in.sup, 90*time.Second)
	if ev.N != 1 || ev.PID == 0 || in.sup.RestartCount() != 1 {
		t.Fatalf("restart event %+v count %d", ev, in.sup.RestartCount())
	}
	got, err := b.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "implementing"})
	if err != nil || got.Status != "implementing" {
		t.Fatalf("update after restart: %+v %v", got, err)
	}
}

func TestSupervisorRefusesServedSocket(t *testing.T) {
	in := startInstance(t, beadsx.DoltConfig{})
	other, err := beadsx.NewSupervisor(beadsx.DoltConfig{Binary: doltBin,
		Dir: filepath.Join(in.layout.StateDir, "other"), Socket: in.sup.Socket()})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Start(context.Background()); !errors.Is(err, beadsx.ErrRefused) {
		_ = other.Stop(context.Background())
		t.Fatalf("second supervisor on one socket: %v", err)
	}
}

func TestPinnedVersionsAccepted(t *testing.T) {
	dir := t.TempDir()
	if _, err := beadsx.NewBD(context.Background(), beadsx.BDConfig{Binary: bdBin, Workspace: dir, Home: dir}); err != nil {
		t.Fatal(err)
	}
	if err := beadsx.CheckDoltVersion(context.Background(), doltBin,
		[]string{"PATH=" + os.Getenv("PATH"), "DOLT_ROOT_PATH=" + dir, "HOME=" + dir}, dir); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceIdempotentAndIsolated(t *testing.T) {
	in := startInstance(t, beadsx.DoltConfig{})
	b := in.workspace(t, "id")
	is, err := b.Create(context.Background(), beadsx.CreateSpec{Title: "kept", Type: "ticket"})
	if err != nil {
		t.Fatal(err)
	}
	b2 := in.workspace(t, "id")
	if got, err := b2.Get(context.Background(), is.ID); err != nil || got.Title != "kept" {
		t.Fatalf("second EnsureWorkspace lost data: %+v %v", got, err)
	}
	// Two projects share one server and keep separate databases.
	o := in.workspace(t, "ot")
	if _, err := o.Get(context.Background(), is.ID); !errors.Is(err, beadsx.ErrNotFound) {
		t.Fatalf("project ot reads project id's issue: %v", err)
	}
	// bd's user config stays in the instance, with metrics off.
	cfg, err := os.ReadFile(filepath.Join(in.layout.Home(), ".config", "bd", "config.yaml"))
	if err != nil || !strings.Contains(string(cfg), "disabled: true") {
		t.Fatalf("isolated bd config: %q %v", cfg, err)
	}
	ws := in.layout.Workspace("id")
	for _, f := range []string{"AGENTS.md", "CLAUDE.md", ".agents", ".cursor"} {
		if _, err := os.Stat(filepath.Join(ws, f)); err == nil {
			t.Errorf("bd init wrote agent integration %s", f)
		}
	}
}

func TestGuardRefuses(t *testing.T) {
	in := startInstance(t, beadsx.DoltConfig{})
	ctx := context.Background()
	cases := []struct {
		name, project, finding string
		arm                    func(t *testing.T, b *beadsx.BD)
	}{
		{"DeferUntil", "gd", "has defer_until", func(t *testing.T, b *beadsx.BD) {
			is, err := b.Create(ctx, beadsx.CreateSpec{Title: "snooze", Type: "ticket"})
			if err != nil {
				t.Fatal(err)
			}
			raw(t, b, "defer", is.ID, "--until", "+1d", "--json")
		}},
		{"LeaseReclaim", "gl", "claim lease", func(t *testing.T, b *beadsx.BD) {
			is, err := b.Create(ctx, beadsx.CreateSpec{Title: "claimed", Type: "ticket"})
			if err != nil {
				t.Fatal(err)
			}
			raw(t, b, "update", is.ID, "--claim", "--json")
		}},
		{"AutoExport", "ge", "export.auto", func(t *testing.T, b *beadsx.BD) {
			raw(t, b, "config", "set", "export.auto", "true", "--json")
		}},
		{"DoltRemote", "gr", "Dolt remote", func(t *testing.T, b *beadsx.BD) {
			raw(t, b, "dolt", "remote", "add", "origin", "file://"+filepath.Join(in.layout.StateDir, "remote"), "--json")
		}},
		{"Metrics", "gm", "metrics", func(t *testing.T, b *beadsx.BD) {
			p := filepath.Join(b.HomeForTest(), ".config", "bd", "config.yaml")
			if err := os.WriteFile(p, []byte("metrics:\n    disabled: false\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = beadsx.EnsureHome(b.HomeForTest()) })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := in.workspace(t, tc.project)
			if err := beadsx.Guard(ctx, b); err != nil {
				t.Fatalf("clean workspace refused: %v", err)
			}
			tc.arm(t, b)
			err := beadsx.Guard(ctx, b)
			if !errors.Is(err, beadsx.ErrRefused) || !strings.Contains(err.Error(), tc.finding) {
				t.Fatalf("guard: %v, want a refusal naming %q", err, tc.finding)
			}
			t.Logf("refused: %v", err)
		})
	}
}

func raw(t *testing.T, b *beadsx.BD, args ...string) {
	t.Helper()
	if _, err := b.RunForTest(context.Background(), args...); err != nil {
		t.Fatalf("bd %v: %v", args, err)
	}
}

// TestHeadSurvivesAPruneReal proves Head reads the highest seq ever
// assigned, not the highest retained record: after every record is pruned,
// Head still names the last one, and Tail from the head reads nothing.
func TestHeadSurvivesAPruneReal(t *testing.T) {
	in := startInstance(t, beadsx.DoltConfig{})
	b := in.workspace(t, "hp")
	ctx := context.Background()
	empty, err := b.Head(ctx)
	if err != nil {
		t.Fatalf("head of an empty journal: %v", err)
	}
	for range 3 {
		if _, err := b.Create(ctx, beadsx.CreateSpec{Title: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	head, err := b.Head(ctx)
	if err != nil || head != empty+3 {
		t.Fatalf("head after three creates = %d, %v; want %d", head, err, empty+3)
	}
	for _, kv := range [][2]string{{"events-journal-retain-days", "0"}, {"events-journal-retain-rows", "0"}} {
		if _, err := b.RunForTest(ctx, "config", "set", kv[0], kv[1], "--json"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.RunForTest(ctx, "events", "prune", "--before", fmt.Sprint(head+1), "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Tail(ctx, 0, 1); !errors.Is(err, beadsx.ErrJournalTruncated) {
		t.Fatalf("tail from 0 after the prune: %v, want truncated", err)
	}
	pruned, err := b.Head(ctx)
	if err != nil || pruned != head {
		t.Fatalf("head after the prune = %d, %v; want %d", pruned, err, head)
	}
	if evs, err := b.Tail(ctx, pruned, 0); err != nil || len(evs) != 0 {
		t.Fatalf("tail from the head after the prune: %+v %v", evs, err)
	}
}
