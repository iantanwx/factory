package sched

import (
	"testing"
)

// Spec factorio-1edo, work entry 7: Topology, Coordinators and Session
// read the home_coordinator rows. Each project's own session serves it,
// and no session serves a project with no coordinator or "".
func TestTheTopologyReadsTheHomeRows(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "loop"}, map[string]bool{"alpha": true, "loop": true})
	if got := hw.s.Topology(); got.Loop != "loop" || len(got.Homes) != 0 || got.Old != "" {
		t.Fatalf("topology before the first start = %+v", got)
	}
	if _, ok := hw.s.Session("alpha"); ok {
		t.Fatal("a session before the first start")
	}

	hw.tick()
	if got := hw.s.Topology(); got.Loop != "loop" || !got.Homes["alpha"] || !got.Homes["loop"] || got.Old != "" {
		t.Fatalf("topology = %+v, want a home of alpha and of loop", got)
	}
	if got := hw.s.Coordinators(); len(got) != 2 || got["alpha"].Project != "alpha" || got["loop"].Project != "loop" {
		t.Fatalf("coordinators = %+v", got)
	}
	for _, p := range []string{"alpha", "loop"} {
		if id, ok := hw.s.Session(p); !ok || id != hw.mustHome(p).Session.ID {
			t.Fatalf("Session(%q) = %v %v, want its own %v", p, id, ok, hw.mustHome(p).Session.ID)
		}
	}
	for _, p := range []string{"beta", ""} {
		if id, ok := hw.s.Session(p); ok {
			t.Fatalf("Session(%q) = %v, want no session", p, id)
		}
	}
}
