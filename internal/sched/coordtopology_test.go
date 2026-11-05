package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
)

// Spec factorio-1edo, work entry 3: until each project has its own
// coordinator, Topology, Coordinators and Session read the one
// coordinator row. The topology's loop project is the registered project
// of the loop's repository, no project has a home coordinator, and the
// old session's project is the row's. Session names the one session for
// every project, and for "".
func TestTheTopologyReadsTheOneCoordinatorRow(t *testing.T) {
	w := newWorld(t)
	projects := []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}, {ID: "loop", Repo: coord.LoopRepo, Path: t.TempDir()}}
	w.cfg.Projects = func() ([]config.Project, error) { return projects, nil }
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	if err := w.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.s.Topology(); got.Loop != "loop" || len(got.Homes) != 0 || got.Old != "" {
		t.Fatalf("topology before the first start = %+v", got)
	}
	if got := w.s.Coordinators(); len(got) != 0 {
		t.Fatalf("coordinators before the first start = %+v", got)
	}
	if _, ok := w.s.Session(prj); ok {
		t.Fatal("a session before the first start")
	}

	w.tick()
	cs := w.s.Coordinator()
	if cs.Session == nil || cs.Project != prj {
		t.Fatalf("no coordinator in %s: %+v", prj, cs)
	}
	if got := w.s.Topology(); got.Loop != "loop" || len(got.Homes) != 0 || got.Old != prj {
		t.Fatalf("topology = %+v, want loop project loop and old session %s", got, prj)
	}
	if got := w.s.Coordinators(); len(got) != 1 || got[prj].RunID != cs.RunID {
		t.Fatalf("coordinators = %+v, want the one row under %s", got, prj)
	}
	for _, p := range []string{prj, "loop", "beta", ""} {
		if id, ok := w.s.Session(p); !ok || id != cs.Session.ID {
			t.Fatalf("Session(%q) = %v %v, want %v", p, id, ok, cs.Session.ID)
		}
	}
}
