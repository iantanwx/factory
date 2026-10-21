package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
)

// A service that starts before any project is registered starts the
// coordinator at the first tick after the operator adds one. It needs no
// restart, and the wait raises no alert.
func TestCoordinatorStartsOnceAProjectIsAdded(t *testing.T) {
	w := newWorld(t)
	var projects []config.Project
	w.cfg.Projects = func() ([]config.Project, error) { return projects, nil }
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	if err := w.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.tick()
	if got := w.s.Coordinator(); got.Session != nil {
		t.Fatalf("a coordinator started with no project: %+v", got)
	}
	if n := w.alerts(AlertCoordinator); n != 0 {
		t.Fatalf("%d coordinator alerts before any project", n)
	}

	projects = []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}}
	w.tick()
	if got := w.s.Coordinator(); got.Session == nil || !w.liveToken(got.RunID) {
		t.Fatalf("no coordinator after the first project: %+v", got)
	}
}
