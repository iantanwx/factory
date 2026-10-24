package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iantanwx/loop/internal/sched"
)

// livenessSchedule names the liveness tick in its key.
const livenessSchedule = "liveness"

// timersPart holds work for each timer that stopped (C17): a cron row of
// schedule whose last fire is older than two periods of its cron, and a
// liveness tick older than two liveness intervals.
func timersPart() Part {
	return Part{Name: "timer-health", Read: func(ctx context.Context, w World) (Reading, error) {
		rows, tick, err := readTimerRows(ctx, w)
		if err != nil {
			return Reading{}, err
		}
		return classifyTimers(w.Now(), w.Intervals().Liveness, rows, tick), nil
	}}
}

// cronTimer is one cron row of schedule. since is its last fire, or when
// the row was made when it never fired.
type cronTimer struct {
	name, expr string
	since      time.Time
	fired      bool
}

// readTimerRows reads the cron rows and the liveness tick. A store with no
// tick returns a nil tick.
func readTimerRows(ctx context.Context, w World) ([]cronTimer, *time.Time, error) {
	var (
		rows []cronTimer
		tick *time.Time
	)
	err := w.Store().Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT project, stage, cron_expr, coalesce(last_fired_at, created_at), last_fired_at IS NOT NULL
			FROM schedule WHERE kind = 'cron' ORDER BY project, stage`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var (
				project, stage string
				r              cronTimer
				since          int64
			)
			if err := rs.Scan(&project, &stage, &r.expr, &since, &r.fired); err != nil {
				return err
			}
			r.name, r.since = stage, time.UnixMilli(since).UTC()
			if project != "" {
				r.name = project + "/" + stage
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
		var at int64
		switch err := tx.QueryRowContext(ctx, `SELECT last_tick_at FROM liveness_ticks WHERE id = 1`).Scan(&at); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			t := time.UnixMilli(at).UTC()
			tick = &t
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read the timers: %w", err)
	}
	return rows, tick, nil
}

// classifyTimers is the timer-health part's rule. A cron row is late when
// now is past the second fire of its cron after its last fire. A liveness
// of zero means that no scheduler runs, so the tick is not judged.
func classifyTimers(now time.Time, liveness time.Duration, rows []cronTimer, tick *time.Time) Reading {
	var r Reading
	add := func(name, line string) {
		key := Key("timer-health", name)
		r.Findings = append(r.Findings, Finding{Key: key, Line: fmt.Sprintf("%s (%s)", line, key)})
	}
	switch {
	case liveness <= 0:
		r.Notes = append(r.Notes, "no scheduler runs, so no liveness tick is judged")
	case tick == nil:
		add(livenessSchedule, "the store holds no liveness tick")
	case now.Sub(*tick) > 2*liveness:
		add(livenessSchedule, fmt.Sprintf("the liveness tick is from %s, older than two intervals of %s", tick.Format(time.RFC3339), liveness))
	}
	for _, t := range rows {
		last := "last fired at " + t.since.Format(time.RFC3339)
		if !t.fired {
			last = "was made at " + t.since.Format(time.RFC3339) + " and never fired"
		}
		second, err := secondFire(t.expr, t.since)
		if err != nil {
			add(t.name, fmt.Sprintf("schedule %s %s, and its cron cannot fire: %v", t.name, last, err))
			continue
		}
		if now.After(second) {
			add(t.name, fmt.Sprintf("schedule %s (cron %q) %s, and missed its fires up to %s", t.name, t.expr, last, second.Format(time.RFC3339)))
		}
	}
	r.Notes = append(r.Notes, fmt.Sprintf("cron rows read: %d", len(rows)))
	return r
}

// secondFire is the second time expr fires after since: since plus two
// periods of a regular cron.
func secondFire(expr string, since time.Time) (time.Time, error) {
	first, err := sched.NextDue(expr, since)
	if err != nil {
		return time.Time{}, err
	}
	return sched.NextDue(expr, first)
}
