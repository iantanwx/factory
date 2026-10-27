package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/overwatch"
)

// C25: the production World reads the pauses. A ticket in in_review with
// two code-reviewer fires since it entered stalls. `stage pause` makes it
// no stall, with a note that names the pause's reason, and `stage resume`
// makes it stall again.
func TestC25AStagePauseHoldsTheStallUntilTheResume(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	ticket := w.ticketIn(machine.InReview)
	// The host's code-reviewer fires every five minutes: three fires pass.
	w.clock.Advance(16 * time.Minute)
	key := "overwatch:stalls:" + ticket + "@" + machine.InReview
	stalls := func() overwatch.PartReport {
		return partReport(t, overwatch.Sweep(ctx, w.srv.overwatch.world, overwatch.Parts()), "stalls")
	}
	if got := stalls(); got.Code != overwatch.Work {
		t.Fatalf("stalls before the pause = %+v, want code 1", got)
	}

	if env, code := w.coordinator("stage", "pause", machine.StageCodeReviewer, "--project", "alpha", "--reason", "the reviewer model is down"); code != 0 {
		t.Fatalf("stage pause = %d %v", code, env)
	}
	got := stalls()
	if got.Code != overwatch.Quiet {
		t.Fatalf("stalls while paused = %+v, want code 0", got)
	}
	var note string
	for _, l := range got.Lines {
		if strings.Contains(l, ticket) {
			note = l
		}
	}
	if !strings.Contains(note, "the reviewer model is down") || strings.Contains(note, key) {
		t.Fatalf("the note of %s = %q, want the pause's reason and no key", ticket, note)
	}

	if env, code := w.coordinator("stage", "resume", machine.StageCodeReviewer, "--project", "alpha"); code != 0 {
		t.Fatalf("stage resume = %d %v", code, env)
	}
	if got := stalls(); got.Code != overwatch.Work {
		t.Fatalf("stalls after the resume = %+v, want code 1", got)
	}
}
