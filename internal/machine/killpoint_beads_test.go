//go:build beads

package machine

// The kill points on the pinned bd 1.3.1 and Dolt 2.2.0.
//
// Run: go test -tags beads ./internal/machine/...
// LOOP_BD and LOOP_DOLT override the binary paths.

import (
	"context"
	"os"
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

// realBeads starts a supervised Dolt in a short temp directory, makes one
// workspace with every machine status, and stops that Dolt by its own PID
// at the end.
func realBeads(t *testing.T) beadsx.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "mk")
	if err != nil {
		t.Fatal(err)
	}
	layout := beadsx.Layout{StateDir: dir}
	sup, err := beadsx.NewSupervisor(beadsx.DoltConfig{Binary: doltBin, Dir: layout.DoltDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
		_ = os.RemoveAll(dir)
	})
	b, err := beadsx.EnsureWorkspace(ctx, beadsx.WorkspaceSpec{
		BD: bdBin, Layout: layout, Project: "mk", Socket: sup.Socket(), Statuses: Statuses(),
	})
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return b
}

func TestKillPointsReal(t *testing.T) {
	b := realBeads(t)
	runKillPoints(t, func(t *testing.T) *world { return newWorldOn(t, b) })
}

func TestEpicCompleteKillPointReal(t *testing.T) {
	b := realBeads(t)
	runEpicCompleteKill(t, func(t *testing.T) *world { return newWorldOn(t, b) })
}
