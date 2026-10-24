package overwatch

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// cronRow writes one cron row of schedule, last fired at fired, or never
// when fired is zero.
func (w *schedWorld) cronRow(t *testing.T, project, stage, expr string, fired time.Time) {
	t.Helper()
	var at sql.NullInt64
	if !fired.IsZero() {
		at = sql.NullInt64{Int64: fired.UnixMilli(), Valid: true}
	}
	created := w.now.Add(-24 * time.Hour).UnixMilli()
	execStore(t, w.st, `INSERT INTO schedule (project, kind, stage, due_at, cron_expr, last_fired_at, created_at, updated_at)
		VALUES (?, 'cron', ?, 0, ?, ?, ?, ?)`, project, stage, expr, at, created, created)
}

// tick writes the liveness tick at t.
func (w *schedWorld) tick(t *testing.T, at time.Time) {
	t.Helper()
	execStore(t, w.st, `INSERT INTO liveness_ticks (id, last_tick_at) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET last_tick_at = excluded.last_tick_at`, at.UnixMilli())
}

func readTimers(t *testing.T, w World) PartReport {
	t.Helper()
	r := Sweep(context.Background(), w, []Part{timersPart()})
	if len(r.Parts) != 1 || r.Parts[0].Name != "timer-health" {
		t.Fatalf("report = %+v", r)
	}
	return r.Parts[0]
}

// The overwatch schedule fires every ten minutes, at minute 3.
const everyTen = "3,13,23,33,43,53 * * * *"

// C17: a cron row one period after its last fire reads code 0. Past two
// periods it is work, keyed by its schedule name.
func TestC17ACronRowPastTwoPeriodsIsWork(t *testing.T) {
	w := newSchedWorld(t)
	w.tick(t, w.now)
	w.cronRow(t, "", "overwatch", everyTen, w.now.Add(-10*time.Minute))
	w.cronRow(t, "factorio", "implementer", "*/5 * * * *", w.now.Add(-5*time.Minute))
	if p := readTimers(t, w); p.Code != Quiet {
		t.Fatalf("at one period: code %d, lines %q; want 0", p.Code, p.Lines)
	}

	w.now = w.now.Add(11 * time.Minute) // overwatch last fired 21 minutes ago; implementer 16
	w.tick(t, w.now)
	p := readTimers(t, w)
	if p.Code != Work {
		t.Fatalf("past two periods: code %d, lines %q; want 1", p.Code, p.Lines)
	}
	all := strings.Join(p.Lines, "\n")
	for _, key := range []string{"overwatch:timer-health:overwatch", "overwatch:timer-health:factorio/implementer"} {
		if !strings.Contains(all, key) {
			t.Fatalf("lines %q; want one that quotes %s", p.Lines, key)
		}
	}
}

// C17: a row that fired on time reads code 0, however long ago it was
// made.
func TestC17ARowThatFiredOnTimeReadsQuiet(t *testing.T) {
	w := newSchedWorld(t)
	w.tick(t, w.now.Add(-20*time.Second))
	w.cronRow(t, "", "overwatch", everyTen, w.now.Add(-time.Second))
	w.cronRow(t, "alpha", "doc-reviewer", "0 9 * * *", w.now.Add(-24*time.Hour-3*time.Minute)) // daily, fired at 09:00 yesterday
	if p := readTimers(t, w); p.Code != Quiet {
		t.Fatalf("code %d, lines %q; want 0", p.Code, p.Lines)
	}
}

// C17: a row that never fired counts from when it was made.
func TestC17ARowThatNeverFiredCountsFromItsCreation(t *testing.T) {
	w := newSchedWorld(t)
	w.tick(t, w.now)
	w.cronRow(t, "factorio", "merge-handoff", everyTen, time.Time{})
	p := readTimers(t, w)
	if p.Code != Work || !strings.Contains(strings.Join(p.Lines, "\n"), "overwatch:timer-health:factorio/merge-handoff") {
		t.Fatalf("code %d, lines %q; want work for a row that never fired in a day", p.Code, p.Lines)
	}
}

// C17: a liveness tick older than two liveness intervals is work. One
// inside them reads code 0.
func TestC17ALivenessTickPastTwoIntervalsIsWork(t *testing.T) {
	w := newSchedWorld(t)
	w.tick(t, w.now.Add(-29*time.Second))
	if p := readTimers(t, w); p.Code != Quiet {
		t.Fatalf("tick 29s old: code %d, lines %q; want 0", p.Code, p.Lines)
	}
	w.tick(t, w.now.Add(-31*time.Second))
	p := readTimers(t, w)
	if p.Code != Work || !strings.Contains(strings.Join(p.Lines, "\n"), "overwatch:timer-health:liveness") {
		t.Fatalf("tick 31s old: code %d, lines %q; want work keyed liveness", p.Code, p.Lines)
	}
}

// C17: a store with no liveness tick holds a timer that never ran.
func TestC17NoLivenessTickIsWork(t *testing.T) {
	w := newSchedWorld(t)
	p := readTimers(t, w)
	if p.Code != Work || !strings.Contains(strings.Join(p.Lines, "\n"), "overwatch:timer-health:liveness") {
		t.Fatalf("code %d, lines %q; want work keyed liveness", p.Code, p.Lines)
	}
}

// C17: a cron that does not parse never fires, so it is work.
func TestC17ACronThatDoesNotParseIsWork(t *testing.T) {
	w := newSchedWorld(t)
	w.tick(t, w.now)
	w.cronRow(t, "factorio", "flaky", "not a cron", w.now)
	p := readTimers(t, w)
	if p.Code != Work || !strings.Contains(strings.Join(p.Lines, "\n"), "overwatch:timer-health:factorio/flaky") {
		t.Fatalf("code %d, lines %q; want work for the bad cron", p.Code, p.Lines)
	}
}

// C17: with no liveness interval, no scheduler runs, so the part judges
// no liveness tick and says so.
func TestC17NoSchedulerJudgesNoLivenessTick(t *testing.T) {
	w := newSchedWorld(t)
	w.intervals = Intervals{}
	p := readTimers(t, w)
	if p.Code != Quiet || len(p.Lines) == 0 {
		t.Fatalf("code %d, lines %q; want 0 with a note", p.Code, p.Lines)
	}
}
