package sched

import (
	"context"
	"database/sql"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
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

// DrainSince is when the drain began, and zero when no drain is on.
func (s *Scheduler) DrainSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drainFrom
}

// Drain stops admission (AE6): admission refuses every run with DRAINING
// and no tick dispatches. It then waits up to wait for the runs in flight
// to finish. The drain ends when they finish or DrainTimeout passes. It
// touches no lease: a run in flight keeps its lease and may finish across
// an upgrade. The drain lasts until the server restarts.
func (s *Scheduler) Drain(ctx context.Context, wait time.Duration) DrainState {
	s.mu.Lock()
	if !s.draining {
		s.draining = true
		s.drainFrom = s.now()
		s.drainEnd = s.drainFrom.Add(s.cfg.DrainTimeout)
	}
	s.mu.Unlock()
	s.cfg.Admission.SetDraining(true)
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
