package sched

import (
	"testing"
)

// Spec factorio-1edo, work entry 7: Topology, Coordinators and Session
// read the home_coordinator rows. With no old session, the loop project's
// session serves every destination until each project's ring reaches its
// own session.
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
	loop := hw.mustHome("loop")
	for _, p := range []string{"alpha", "loop", "beta", ""} {
		if id, ok := hw.s.Session(p); !ok || id != loop.Session.ID {
			t.Fatalf("Session(%q) = %v %v, want the loop project's %v", p, id, ok, loop.Session.ID)
		}
	}
}
