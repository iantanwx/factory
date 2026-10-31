package server

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/overwatch"
)

// C-bug-1 (factorio-gqyt, F-57): a fire that the server refused while it
// drained never counts, also after a restart ends the drain. A ticket
// enters in_review during a drain, and three code-reviewer fires pass
// before the restart. Right after the restart no fire has started a run on
// it, and no fire since the drain began admitted one. The restart ends the
// drain in the store, so two fires after the restart count against it.
func TestC10AFireDuringADrainDoesNotCountAfterTheRestart(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	w.srv.Sched.Drain(ctx, 0)
	if !w.srv.Sched.Draining() {
		t.Fatal("the server does not drain")
	}
	ticket := w.ticketIn(machine.InReview)
	// The host's code-reviewer fires every five minutes: three fires pass.
	w.clock.Advance(16 * time.Minute)
	w.restart()
	if w.srv.Sched.Draining() {
		t.Fatal("the server still drains after the restart")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE item = ?`, ticket); n != 0 {
		t.Fatalf("%d runs on %s, want none: the fixture needs no fire since the drain to admit a run", n, ticket)
	}
	r := overwatch.Sweep(ctx, w.srv.overwatch.world, overwatch.Parts())
	key := "overwatch:stalls:" + ticket + "@" + machine.InReview
	for _, l := range partReport(t, r, "stalls").Lines {
		if strings.Contains(l, key) {
			t.Fatalf("a fire during the drain before the restart counts against %s: %q", ticket, l)
		}
	}
	w.clock.Advance(11 * time.Minute)
	if n := w.count(`SELECT count(*) FROM runs WHERE item = ?`, ticket); n != 0 {
		t.Fatalf("%d runs on %s, want none: the fixture needs no fire after the restart to admit a run", n, ticket)
	}
	r = overwatch.Sweep(ctx, w.srv.overwatch.world, overwatch.Parts())
	stalls := partReport(t, r, "stalls")
	if !slices.ContainsFunc(stalls.Lines, func(l string) bool { return strings.Contains(l, key) }) {
		t.Fatalf("two fires after the restart do not count against %s: %+v", ticket, stalls)
	}
}
