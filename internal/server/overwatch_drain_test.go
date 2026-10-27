package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/overwatch"
)

// factorio-6z48: a fire of a stage while the server drains starts no run,
// so it does not count against an item. A ticket that entered in_review
// during a drain, with two code-reviewer fires since then, is no stall
// while the drain holds, and the stalls part names the drain in a note.
func TestC10AFireDuringADrainDoesNotCountAgainstAnItem(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	w.srv.Sched.Drain(ctx, 0)
	if !w.srv.Sched.Draining() {
		t.Fatal("the server does not drain")
	}
	ticket := w.ticketIn(machine.InReview)
	// The host's code-reviewer fires every five minutes: three fires pass.
	w.clock.Advance(16 * time.Minute)
	r := overwatch.Sweep(ctx, w.srv.overwatch.world, overwatch.Parts())
	stalls := partReport(t, r, "stalls")
	key := "overwatch:stalls:" + ticket + "@" + machine.InReview
	var note string
	for _, l := range stalls.Lines {
		if strings.Contains(l, key) {
			t.Fatalf("a fire during the drain counts against %s: %q", ticket, l)
		}
		if strings.Contains(l, ticket) {
			note = l
		}
	}
	if !strings.Contains(note, "drain") {
		t.Fatalf("no stalls note names the drain for %s: %+v", ticket, stalls)
	}
}
