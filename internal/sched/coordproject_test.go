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

// The coordinator's checkout clones one project. When the operator removes
// that project, the session ends, and the next one starts in a project
// that is still registered.
func TestCoordinatorMovesWhenItsProjectIsRemoved(t *testing.T) {
	w := newWorld(t)
	projects := []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}}
	w.cfg.Projects = func() ([]config.Project, error) { return projects, nil }
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	if err := w.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.tick()
	first := w.s.Coordinator()
	if first.Session == nil || first.Project != prj {
		t.Fatalf("no coordinator in %s: %+v", prj, first)
	}

	projects = []config.Project{{ID: "beta", Repo: "iantanwx/beta", Path: w.repo}}
	w.tick()
	got := w.s.Coordinator()
	if got.Session == nil || got.Project != "beta" || got.RunID == first.RunID {
		t.Fatalf("the coordinator stayed in the removed project: %+v; %d coordinator alerts", got, w.alerts(AlertCoordinator))
	}
	if w.liveToken(first.RunID) || !w.liveToken(got.RunID) {
		t.Fatalf("tokens: old live %v, new live %v", w.liveToken(first.RunID), w.liveToken(got.RunID))
	}
}
