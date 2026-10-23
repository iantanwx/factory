package sched

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

// Heartbeat writes the liveness tick (KTD8). Start runs it every
// LivenessInterval, so a SIGKILL or a power loss still leaves the time the
// server was last up.
func (s *Scheduler) Heartbeat(ctx context.Context) error {
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO liveness_ticks (id, last_tick_at) VALUES (1, ?)
			ON CONFLICT (id) DO UPDATE SET last_tick_at = excluded.last_tick_at`, s.now64())
		return err
	})
}

// recordOutage records the span from the last liveness tick to now as an
// outage, then writes a new tick, in one write.
func (s *Scheduler) recordOutage(ctx context.Context) error {
	now := s.now64()
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		var last int64
		err := tx.QueryRow(`SELECT last_tick_at FROM liveness_ticks WHERE id = 1`).Scan(&last)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case now > last:
			if _, err := tx.Exec(`INSERT INTO outages (started_at, ended_at) VALUES (?, ?)`, last, now); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`INSERT INTO liveness_ticks (id, last_tick_at) VALUES (1, ?)
			ON CONFLICT (id) DO UPDATE SET last_tick_at = excluded.last_tick_at`, now)
		return err
	})
}

// outagesSince lists the outage spans that ended at or after t.
func (s *Scheduler) outagesSince(ctx context.Context, t time.Time) ([]admission.Outage, error) {
	var out []admission.Outage
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT started_at, ended_at FROM outages WHERE ended_at >= ? ORDER BY id`, t.UnixMilli())
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var a, b int64
			if err := rs.Scan(&a, &b); err != nil {
				return err
			}
			out = append(out, admission.Outage{Start: time.UnixMilli(a), End: time.UnixMilli(b)})
		}
		return rs.Err()
	})
	return out, err
}

func (s *Scheduler) markCall(ctx context.Context, run string, t time.Time) error {
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE runs SET last_cli_call_at = ? WHERE run_id = ? AND ended_at IS NULL`, t.UnixMilli(), run)
		return err
	})
}

// watch checks every live run against the agent host, which stays the
// truth (KTD9). A run admitted and still starting holds no session yet,
// and its dispatch owns it. At start-up, a run with no session is an
// orphan of a crash, and it settles not-ready.
func (s *Scheduler) watch(ctx context.Context, hc *config.HostConfig, startup bool) error {
	runs, err := s.openRuns(ctx)
	if err != nil {
		return err
	}
	// One inventory read serves the whole watch. A failed read is an
	// incomplete inventory, which never reads a session as absent.
	var (
		inv  agenthost.Inventory
		read bool
	)
	for _, r := range runs {
		if r.Rec.Session == nil {
			if startup {
				s.abandon(ctx, r, false, errors.New("the server stopped before the run started"))
			}
			continue
		}
		if !read {
			read = true
			var err error
			if inv, err = s.cfg.Agents.Inventory(ctx); err != nil {
				inv.Complete = false
			}
		}
		s.check(ctx, hc, r, inv)
	}
	return nil
}

// check decides one run: ended, live or unknown. It reuses the liveness
// read Ended made, so each run's liveness is read once a watch.
func (s *Scheduler) check(ctx context.Context, hc *config.HostConfig, r runRow, inv agenthost.Inventory) {
	sess := *r.Rec.Session
	rep, err := s.cfg.Agents.Ended(ctx, sess)
	if err == nil && rep.Ended {
		end := rep.Since
		if end.IsZero() {
			end = s.now()
		}
		s.settleEnded(ctx, r, end, rep.Reason)
		return
	}
	var st agenthost.Settlement
	if rep.Settlement != nil {
		// A failed read carries Unknown, which the switch treats as an
		// error does.
		st, err = *rep.Settlement, nil
	} else {
		st, err = s.cfg.Agents.Settle(ctx, sess.ID)
	}
	switch {
	case err == nil && st.Liveness == agenthost.Exited:
		s.settleEnded(ctx, r, s.now(), st.Reason)
	case err == nil && st.Liveness == agenthost.Live:
		s.markLive(ctx, hc, r, inv)
	case inv.Lookup(sess) == agenthost.Absent:
		s.settleEnded(ctx, r, s.now(), "a complete inventory of the session's epoch no longer lists it")
	default:
		s.markUnknown(ctx, hc, r)
	}
}

// markLive re-adopts a live run: it moves the session's epoch to the
// inventory's, renews the lease and keeps its epoch (KTD7, AE2), and
// raises one alert when the run passed its budget. A run over budget keeps
// running (KTD9).
func (s *Scheduler) markLive(ctx context.Context, hc *config.HostConfig, r runRow, inv agenthost.Inventory) {
	rec := r.Rec
	if moved, ok := inv.Readopted(*rec.Session); ok {
		rec.Session = &moved
	}
	rec.UnknownSince = 0
	if r.State != RunRunning || rec.encode() != r.Rec.encode() {
		if err := s.setRun(ctx, r.ID, RunRunning, rec); err != nil {
			s.log.ErrorContext(ctx, "re-adopt a run", "run", r.ID, "err", err)
		}
	}
	if r.Item != "" {
		if _, err := s.cfg.Machine.Renew(ctx, r.Project, r.Item, r.ID); err != nil && !errors.Is(err, errNotOwner) {
			s.log.WarnContext(ctx, "renew a lease", "run", r.ID, "err", err)
		}
	}
	budget := s.budget(ctx, hc, r)
	if over := s.now().Sub(r.Started); over > budget {
		s.raiseOnce(ctx, config.Alert{Project: r.Project, Kind: AlertRunOverBudget, DedupeKey: AlertRunOverBudget + ":" + r.ID,
			Subject: fmt.Sprintf("%s run %s on %s passed its run budget of %s", r.Stage, r.ID, r.Item, budget),
			Body:    "The run keeps running. The coordinator decides whether to look at it or stop it."})
	}
}

// markUnknown keeps the lease of a run whose liveness reads unknown. Past
// two run budgets it raises one alert and still reclaims nothing (KTD9).
func (s *Scheduler) markUnknown(ctx context.Context, hc *config.HostConfig, r runRow) {
	rec := r.Rec
	if rec.UnknownSince == 0 {
		rec.UnknownSince = s.now64()
		if err := s.setRun(ctx, r.ID, RunUnknown, rec); err != nil {
			s.log.ErrorContext(ctx, "mark a run unknown", "run", r.ID, "err", err)
		}
		return
	}
	bound := 2 * s.budget(ctx, hc, r)
	if s.now().Sub(time.UnixMilli(rec.UnknownSince)) > bound {
		s.raiseOnce(ctx, config.Alert{Project: r.Project, Kind: AlertRunUnknown, DedupeKey: AlertRunUnknown + ":" + r.ID,
			Subject: fmt.Sprintf("the agent host cannot say whether %s run %s on %s lives", r.Stage, r.ID, r.Item),
			Body:    fmt.Sprintf("Liveness has read unknown for more than %s. The server keeps the lease and reclaims nothing.", bound)})
	}
}

// budget is the run budget of the run's stage: the project's override,
// host config's run_budget, or DefaultRunBudget.
func (s *Scheduler) budget(ctx context.Context, hc *config.HostConfig, r runRow) time.Duration {
	if snap, err := s.cfg.ProjectConfig(ctx, r.Project); err == nil && snap != nil {
		if sc, ok := snap.Config.Stages[r.Stage]; ok && sc.RunBudget > 0 {
			return sc.RunBudget
		}
	}
	if hc != nil && hc.RunBudget > 0 {
		return hc.RunBudget
	}
	return DefaultRunBudget
}

// settleEnded settles a run whose session ended with no finish (KTD8): it
// classes the run, reclaims its lease to the stage-correct state, keeps
// its checkout without .env, revokes its token, closes its session and
// raises an inbox alert.
func (s *Scheduler) settleEnded(ctx context.Context, r runRow, ended time.Time, reason string) {
	ctx = context.WithoutCancel(ctx)
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	outages, err := s.outagesSince(ctx, r.Started)
	if err != nil {
		s.log.ErrorContext(ctx, "read outages", "err", err)
		return
	}
	quota := s.quotaFull(ctx, agenthost.Harness(r.Harness))
	out, err := s.cfg.Admission.RecordOutcome(ctx, admission.Settlement{RunID: r.ID, Ended: ended, LastCLICall: r.LastCall,
		Outages: outages, QuotaHit: quota != "", Note: reason})
	if err != nil {
		s.log.ErrorContext(ctx, "settle an ended run", "run", r.ID, "err", err)
		return
	}
	if !out.Replayed {
		if out.Class == admission.ClassQuotaHit {
			s.pauseHarness(ctx, agenthost.Harness(r.Harness), quota)
		}
		s.afterSettle(ctx, r, "reclaim", CheckoutKept)
		s.alertEnded(ctx, r, out.Class, reason)
		s.alertHeld(ctx, r, out, "")
	}
	s.closeSession(ctx, r)
}

// alertEnded raises the inbox alert of an ended run. A counted crash
// raises one each time. A class that does not count raises one only when
// the stage's last two runs both ended with it (KTD8).
func (s *Scheduler) alertEnded(ctx context.Context, r runRow, class admission.Class, reason string) {
	if class == admission.ClassCrashed {
		s.raise(ctx, config.Alert{Project: r.Project, Kind: AlertRunCrashed, DedupeKey: AlertRunCrashed + ":" + r.ID,
			Subject: fmt.Sprintf("%s run %s on %s ended with no finish", r.Stage, r.ID, r.Item),
			Body:    "The run counts as crashed. Its checkout stays at " + r.Checkout + ". " + reason})
		return
	}
	var last []string
	_ = s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT coalesce(outcome_class, '') FROM runs WHERE project = ? AND stage = ? AND ended_at IS NOT NULL
			ORDER BY ended_at DESC LIMIT 2`, r.Project, r.Stage)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var c string
			if err := rs.Scan(&c); err != nil {
				return err
			}
			last = append(last, c)
		}
		return rs.Err()
	})
	if len(last) == 2 && last[0] == string(class) && last[1] == string(class) {
		s.raise(ctx, config.Alert{Project: r.Project, Kind: AlertRunRepeated, DedupeKey: AlertRunRepeated + ":" + stageKey(r.Project, r.Stage) + ":" + string(class),
			Subject: fmt.Sprintf("%s runs on %s keep ending as %s", r.Stage, r.Project, class),
			Body:    "These runs do not count against the failure limit. " + reason})
	}
}

// closeSession closes a settled run's session and keeps its checkout.
func (s *Scheduler) closeSession(ctx context.Context, r runRow) {
	if r.Rec.Session == nil || r.Rec.ClosedAt != 0 {
		return
	}
	err := s.cfg.Agents.Close(ctx, r.Rec.Session.ID)
	if err != nil && !errors.Is(err, agenthost.ErrStaleHandle) && !errors.Is(err, agenthost.ErrExitUnproven) {
		s.log.WarnContext(ctx, "close a session", "run", r.ID, "err", err)
		return
	}
	if err != nil {
		// The host cannot reach the handle. A handle that is gone has
		// nothing left to close; a later inventory shows any trace.
		s.log.InfoContext(ctx, "close a session", "run", r.ID, "err", err)
	}
	rec := r.Rec
	rec.ClosedAt = s.now64()
	if err := s.setRecord(ctx, r.ID, rec); err != nil {
		s.log.ErrorContext(ctx, "record a closed session", "run", r.ID, "err", err)
	}
}

// closeEnded closes the session of each settled run that still has one
// open: a finished run, whose session the finish did not close.
func (s *Scheduler) closeEnded(ctx context.Context) error {
	runs, err := s.queryRuns(ctx, `r.ended_at IS NOT NULL AND r.host_identity LIKE '%"session"%' AND r.host_identity NOT LIKE '%"closed_at"%'`)
	if err != nil {
		return err
	}
	for _, r := range runs {
		s.closeSession(ctx, r)
	}
	return nil
}
