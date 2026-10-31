package sched

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// DrainCallWait bounds how long one `loop drain` call waits. It stays under
// the CLI's wait budget, and the caller calls again until Done.
const DrainCallWait = 60 * time.Second

// DrainState is the answer of `loop drain`.
type DrainState struct {
	Draining bool `json:"draining"`
	// Drained is true when no run is in flight.
	Drained bool `json:"drained"`
	// TimedOut is true when the drain timeout passed with runs in flight.
	TimedOut bool      `json:"timed_out"`
	Done     bool      `json:"done"`
	InFlight int       `json:"in_flight"`
	Deadline time.Time `json:"deadline"`
}

// Draining reports whether a drain is on.
func (s *Scheduler) Draining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

// Drain is one drain of the server, from its start until the restart that
// ended it. A zero To: the drain is on.
type Drain struct{ From, To time.Time }

// Drains reads each drain that the store records, oldest first.
func Drains(ctx context.Context, tx *sql.Tx) ([]Drain, error) {
	rs, err := tx.QueryContext(ctx, `SELECT started_at, ended_at FROM drains ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	var out []Drain
	for rs.Next() {
		var from int64
		var to sql.NullInt64
		if err := rs.Scan(&from, &to); err != nil {
			_ = rs.Close()
			return nil, err
		}
		d := Drain{From: time.UnixMilli(from)}
		if to.Valid {
			d.To = time.UnixMilli(to.Int64)
		}
		out = append(out, d)
	}
	return out, errors.Join(rs.Err(), rs.Close())
}

// EndDrains ends at at each drain that the store holds on. The server runs
// it at each start: a drain lasts until the server restarts.
func EndDrains(ctx context.Context, st *store.Store, at time.Time) error {
	return st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE drains SET ended_at = ? WHERE ended_at IS NULL`, at.UnixMilli())
		return err
	})
}

// Drain stops admission (AE6): admission refuses every run with DRAINING
// and no tick dispatches. It then waits up to wait for the runs in flight
// to finish. The drain ends when they finish or DrainTimeout passes. It
// touches no lease: a run in flight keeps its lease and may finish across
// an upgrade. The drain lasts until the server restarts. The store records
// its start, so the stalls part still reads it after the restart.
func (s *Scheduler) Drain(ctx context.Context, wait time.Duration) DrainState {
	var from time.Time
	s.mu.Lock()
	if !s.draining {
		from = s.now()
		s.draining = true
		s.drainEnd = from.Add(s.cfg.DrainTimeout)
	}
	s.mu.Unlock()
	s.cfg.Admission.SetDraining(true)
	if !from.IsZero() {
		err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
			_, err := tx.Exec(`INSERT INTO drains (started_at) VALUES (?)`, from.UnixMilli())
			return err
		})
		if err != nil {
			s.log.ErrorContext(ctx, "record the drain", "err", err)
		}
	}
	stop := time.Now().Add(wait)
	for {
		st := s.drainState(ctx)
		if st.Done || !time.Now().Before(stop) {
			return st
		}
		select {
		case <-ctx.Done():
			return st
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *Scheduler) drainState(ctx context.Context) DrainState {
	s.mu.Lock()
	st := DrainState{Draining: s.draining, Deadline: s.drainEnd}
	s.mu.Unlock()
	_ = s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE ended_at IS NULL`).Scan(&st.InFlight)
	})
	st.Drained = st.InFlight == 0
	st.TimedOut = !st.Drained && !s.now().Before(st.Deadline)
	st.Done = st.Drained || st.TimedOut
	return st
}

// drain is the handler of `loop drain`.
func (s *Scheduler) drain(ctx context.Context, _ *rpc.Call) cli.Result {
	wait := DrainCallWait
	if dl, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(dl)-time.Second)
	}
	return cli.Found(s.Drain(ctx, wait))
}
