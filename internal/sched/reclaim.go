package sched

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/machine"
)

var errNotOwner = machine.ErrNotOwner

// Reconcile is the scheduler's start-up step (R17). It records the outage
// since the last liveness tick, settles each run that a crash left before
// its start, and checks every run with a session against the agent host.
// A run the host shows live is re-adopted and its lease renewed with the
// same epoch, before anything is reclaimed (AE2). Only a session the host
// proves ended is settled. Unknown reclaims nothing.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	if err := s.recordOutage(ctx); err != nil {
		return fmt.Errorf("sched: record the outage: %w", err)
	}
	hc, err := s.cfg.Host.Load(ctx)
	if err != nil {
		return fmt.Errorf("sched: load host config: %w", err)
	}
	if err := s.watch(ctx, hc, true); err != nil {
		return fmt.Errorf("sched: reconcile runs: %w", err)
	}
	return s.sweepLeases(ctx)
}

// sweepLeases frees each lease whose owner run has settled. A settlement
// that met a beads fault leaves one, and this retries it. A run that
// ended with no report is reclaimed. Every other is released.
func (s *Scheduler) sweepLeases(ctx context.Context) error {
	type held struct{ project, item, run, class string }
	var rows []held
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT l.project, l.item, l.owner_run_id, coalesce(r.outcome_class, '')
			FROM leases l JOIN runs r ON r.run_id = l.owner_run_id WHERE r.ended_at IS NOT NULL`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var h held
			if err := rs.Scan(&h.project, &h.item, &h.run, &h.class); err != nil {
				return err
			}
			rows = append(rows, h)
		}
		return rs.Err()
	})
	if err != nil {
		return err
	}
	for _, h := range rows {
		verb := "release"
		switch admission.Class(h.class) {
		case admission.ClassCrashed, admission.ClassInfra, admission.ClassQuotaHit:
			verb = "reclaim"
		}
		if _, err := s.releaseLease(ctx, h.project, h.item, h.run, verb); err != nil {
			s.log.WarnContext(ctx, "free a settled run's lease", "run", h.run, "item", h.item, "err", err)
		}
	}
	return nil
}
