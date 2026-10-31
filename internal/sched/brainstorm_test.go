package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// Brainstorm runs in a room the coordinator opens (plan: "brainstorm runs in
// one"), and the pack holds no unattended brainstorm runbook. A project that
// enables the brainstorm stage gets no scheduled brainstorm run, and a dispatch
// of it is refused before any claim or checkout.
func TestBrainstormStartsNoUnattendedRun(t *testing.T) {
	w := newWorld(t)
	w.project.set(snapshot(t, "stages:\n  implementer: {}\n  brainstorm: {}\nmerge:\n  mechanism: direct\n", ""))
	w.writeHost(nil, map[string]string{"brainstorm": "*/5 * * * *"})

	st, ref := w.s.Dispatch(context.Background(), prj, "brainstorm")
	if st != nil || ref == nil || ref.Code != cli.CodeUsage {
		t.Fatalf("a brainstorm dispatch = %+v, %+v; want a %s refusal", st, ref, cli.CodeUsage)
	}

	w.clock.Set(at(t, "2026-10-21T10:00:30Z"))
	w.tick()
	w.clock.Set(at(t, "2026-10-21T10:05:30Z"))
	w.tick()
	w.s.Wait()
	if n := len(w.host.Launches()); n != 0 {
		t.Fatalf("%d brainstorm runs started", n)
	}
	if n := w.count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("%d runs recorded", n)
	}
}
