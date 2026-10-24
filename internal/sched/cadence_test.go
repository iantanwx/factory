package sched

import (
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// verdictMedian is the median time from a code-reviewer dispatch to its
// verdict. Tickertown measured it on 2026-10-09 over 87 verdicts: a pass
// landed a median 8.8 minutes after the reviewer's tick and a failure 9.2
// (scripts/sdlc/timer-headroom.test.ts, alpha). The loop holds no
// measurement of its own yet.
const verdictMedian = 9 * time.Minute

// deadlines lists every deadline of expr in the day from start.
func deadlines(t *testing.T, expr string, start time.Time) []time.Time {
	t.Helper()
	var out []time.Time
	for at := start.Add(-time.Second); ; {
		next, err := NextDue(expr, at)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if !next.Before(start.Add(24 * time.Hour)) {
			return out
		}
		out = append(out, next)
		at = next
	}
}

// wait is the time from at to the next deadline of expr.
func wait(t *testing.T, expr string, at time.Time) time.Duration {
	t.Helper()
	next, err := NextDue(expr, at)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return next.Sub(at)
}

// A code review writes one of two outcomes, and a different stage reads
// each one. A pass leaves the ticket in ready_to_merge, which merge-handoff
// hands to the merge mechanism. A reject sends it back to todo as rework,
// which the implementer claims. Each scheduler tick dispatches at most one
// run of a stage for each project, so the implementer's ticks bound both how
// soon a rework round starts and how many implementer runs start in an hour.
//
// Tickertown runs three implementer rows on main, at minutes 3, 8 and 13 of
// each fifteen, and holds that each pipeline transition is offered within
// five minutes (scripts/sdlc/timer-cadence.test.ts, alpha). One loop stage
// stands for those three rows. At minutes 3, 18, 33 and 48 alone it leaves a
// nine-minute verdict unread for up to fourteen minutes, and a ticket's
// review rounds pay that wait each time. Nothing errors, and the loop runs
// slow.
func TestDefaultSchedulesReadAReviewVerdictWithinOneReviewerCycle(t *testing.T) {
	d := config.DefaultThroughput()
	review := d[config.StageCodeReviewer].Schedule
	implement := d[config.StageImplementer].Schedule
	handoff := d["merge-handoff"].Schedule
	start := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)

	ticks := deadlines(t, review, start)
	var cycle time.Duration
	for i := 1; i < len(ticks); i++ {
		cycle = max(cycle, ticks[i].Sub(ticks[i-1]))
	}
	if cycle == 0 {
		t.Fatalf("code-reviewer %q fires fewer than twice a day", review)
	}

	for _, r := range ticks {
		// The implementer fires after the reviewer, never in its minute.
		if got := wait(t, implement, r.Add(-time.Second)); got == time.Second {
			t.Errorf("the implementer fires with the code-reviewer at %s", r.Format("15:04"))
		}
		landed := r.Add(verdictMedian)
		toMerge, toImplement := wait(t, handoff, landed), wait(t, implement, landed)
		// A pass is the commoner verdict, and its merge releases the next
		// ticket of a chain, so it reaches its reader first.
		if toMerge >= toImplement {
			t.Errorf("a verdict at %s waits %s for merge-handoff and %s for the implementer",
				landed.Format("15:04"), toMerge, toImplement)
		}
		if toImplement > cycle {
			t.Errorf("a reject at %s waits %s for the implementer, over the code-reviewer's cycle of %s",
				landed.Format("15:04"), toImplement, cycle)
		}
	}
}
