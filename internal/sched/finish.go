package sched

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// FinishResult is the answer of `loop run finish`.
type FinishResult struct {
	RunID   string          `json:"run_id"`
	Outcome string          `json:"outcome"`
	Class   admission.Class `json:"class"`
	Counted bool            `json:"counted"`
	// Released is true when the run still held its item's lease and the
	// finish released it.
	Released bool `json:"released"`
	// Replayed is true when the run had already settled. Class is then the
	// stored one.
	Replayed bool `json:"replayed"`
	// Fault is the loop fault the run's record keeps, from
	// `run finish --fault` (KTD31).
	Fault string `json:"fault,omitempty"`
}

// Finish settles a run by its own report (KTD7): done, failed or blocked.
// It releases the lease the run still holds, which restores the
// stage-correct state, settles the checkout, removes .env and revokes the
// token. The next tick closes the session and keeps the checkout.
func (s *Scheduler) Finish(ctx context.Context, runID, outcome string) (FinishResult, *cli.Refusal) {
	return s.FinishFault(ctx, runID, outcome, "")
}

// FinishFault is Finish with the loop fault that stopped the run (KTD31).
// The run's record keeps the fault id. A blocked run never counts toward
// the failure limit, with a fault or without one (KTD8). A fault id the
// server never gave out is NOT_FOUND, and the run stays unsettled.
func (s *Scheduler) FinishFault(ctx context.Context, runID, outcome, faultID string) (FinishResult, *cli.Refusal) {
	switch outcome {
	case cli.OutcomeDone, cli.OutcomeFailed, cli.OutcomeBlocked:
	default:
		return FinishResult{}, &cli.Refusal{Code: cli.CodeUsage, Rule: "bad_outcome", Hint: "the outcome is done, failed or blocked"}
	}
	ctx = context.WithoutCancel(ctx)
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	r, err := s.runByID(ctx, runID)
	if errors.Is(err, errNoRun) {
		return FinishResult{}, &cli.Refusal{Code: cli.CodeNotFound, Rule: "no_run", Hint: "the server has no run " + runID}
	}
	if err != nil {
		return FinishResult{}, cli.InternalRefusal(err)
	}
	kept, ref := s.keepFault(ctx, runID, faultID)
	if ref != nil {
		return FinishResult{}, ref
	}
	now := s.now()
	out, err := s.cfg.Admission.RecordOutcome(ctx, admission.Settlement{RunID: runID, Finish: outcome, Ended: now, LastCLICall: now})
	if err != nil {
		return FinishResult{}, cli.InternalRefusal(err)
	}
	res := FinishResult{RunID: runID, Outcome: outcome, Class: out.Class, Counted: out.Counted, Replayed: out.Replayed, Fault: kept}
	if out.Replayed {
		return res, nil
	}
	res.Released = s.afterSettle(ctx, r, "release", CheckoutSettled)
	s.alertHeld(ctx, r, out, kept)
	return res, nil
}

// alertHeld raises the inbox alert of an item that a settlement stopped at
// the run's stage, so no item waits in silence. Two counted failures with
// unchanged inputs stop it (R13). The alert names each failed run, and
// only changed inputs admit the item again. A blocked finish stops it too,
// unless an escalation or a room already holds the item, or the item left
// the states the stage takes. That alert is the hold itself
// (admission.BlockedKey): changed inputs, a move of the item, or an answer
// to the alert lifts it. A second blocked finish at the same inputs counts
// up on the open alert.
func (s *Scheduler) alertHeld(ctx context.Context, r runRow, out admission.Outcome, faultID string) {
	if r.Item == "" || out.Inputs == "" {
		return
	}
	if out.Counted && len(out.Failures) >= admission.FailureLimit {
		var runs []string
		for _, f := range out.Failures {
			line := fmt.Sprintf("- run %s ended %s", f.RunID, f.Class)
			if f.Note != "" {
				line += ": " + f.Note
			}
			runs = append(runs, line)
		}
		s.raise(ctx, config.Alert{Project: r.Project, Kind: AlertRetryLimit,
			DedupeKey: AlertRetryLimit + ":" + r.Project + "/" + r.Item + "/" + r.Stage + "/" + out.Inputs,
			Subject:   fmt.Sprintf("%s stopped taking %s after %d failed attempts at the same inputs", r.Stage, r.Item, len(out.Failures)),
			Body: strings.Join(runs, "\n") + "\n\n" + fmt.Sprintf("`loop work show %s` shows the item's last note, and `loop run show <run>` shows each run. "+
				"Only changed inputs admit a new attempt: `loop work edit %s --title <title> --body <body>`, a change of a declared file "+
				"or a cited document on the base, a push to the pull request of a code review, or a new release. The count never resets. Answer this entry with what changed.", r.Item, r.Item)})
		return
	}
	if out.Class != admission.ClassBlocked {
		return
	}
	var held bool
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM escalations WHERE run_id = ?)
			OR EXISTS (SELECT 1 FROM holds WHERE project = ? AND item = ?)`, r.ID, r.Project, r.Item).Scan(&held)
	})
	if err != nil {
		s.log.ErrorContext(ctx, "read the holds of a blocked run", "run", r.ID, "err", err)
	}
	if held {
		return
	}
	// An item that cannot be read is held all the same: an alert too many
	// costs less than an item that runs blocked on every tick.
	if it, ref := s.cfg.Machine.Show(ctx, r.Project, r.Item); ref == nil && !admission.Takes(r.Stage, it) {
		return
	}
	body := fmt.Sprintf("No %s run takes %s until its inputs or its status change, or until an answer to this entry. "+
		"`loop run show %s` and `loop work show %s` show why it stopped.", r.Stage, r.Item, r.ID, r.Item)
	if faultID != "" {
		body += fmt.Sprintf(" The run named loop fault %s. A release that fixes it changes the inputs.", faultID)
	}
	s.raise(ctx, config.Alert{Project: r.Project, Kind: AlertRunBlocked, DedupeKey: admission.BlockedKey(r.Project, r.Item, r.Stage, out.Inputs),
		Subject: fmt.Sprintf("%s run %s on %s finished blocked", r.Stage, r.ID, r.Item), Body: body})
}

// keepFault writes faultID on a run that has not ended, and returns the
// fault id the run's record holds.
func (s *Scheduler) keepFault(ctx context.Context, runID, faultID string) (string, *cli.Refusal) {
	var kept string
	missing := false
	err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		if faultID != "" {
			ok, err := fault.Exists(tx, faultID)
			if err != nil {
				return err
			}
			if !ok {
				missing = true
				return nil
			}
			if _, err := tx.Exec(`UPDATE runs SET fault_id = ? WHERE run_id = ? AND ended_at IS NULL`, faultID, runID); err != nil {
				return err
			}
		}
		return tx.QueryRow(`SELECT fault_id FROM runs WHERE run_id = ?`, runID).Scan(&kept)
	})
	if err != nil {
		return "", cli.InternalRefusal(err)
	}
	if missing {
		return "", &cli.Refusal{Code: cli.CodeNotFound, Rule: "no_fault",
			Hint: fmt.Sprintf("the server gave out no fault %s; `loop fault report` names the id", faultID)}
	}
	return kept, nil
}

// Register sets the handlers of the verbs the scheduler serves: run
// finish and drain.
func (s *Scheduler) Register(srv *rpc.Server) error {
	if err := srv.Handle("run finish", s.runFinish); err != nil {
		return err
	}
	return srv.Handle("drain", s.drain)
}

func (s *Scheduler) runFinish(ctx context.Context, call *rpc.Call) cli.Result {
	p := call.Principal
	if p.Kind != cli.PrincipalRun || p.RunID == "" {
		return cli.Reject(cli.CodeCapabilityDenied, "run_only", "only a stage run finishes, from its own checkout")
	}
	res, ref := s.FinishFault(ctx, p.RunID, call.Inv.Flags["outcome"], call.Inv.Flags["fault"])
	if ref != nil {
		return ref.Result()
	}
	return cli.Found(res)
}

// OnCall records a run's CLI call as its last observed activity (KTD7,
// KTD8). It writes at most once every few seconds for each run.
func (s *Scheduler) OnCall(ctx context.Context, p rpc.Principal) {
	if p.Kind != cli.PrincipalRun || p.RunID == "" {
		return
	}
	now := s.now()
	s.mu.Lock()
	last := s.lastCall[p.RunID]
	if !last.IsZero() && now.Sub(last) < callWriteGap && now.After(last) {
		s.mu.Unlock()
		return
	}
	s.lastCall[p.RunID] = now
	s.mu.Unlock()
	if err := s.markCall(ctx, p.RunID, now); err != nil {
		s.log.WarnContext(ctx, "record a CLI call", "run", p.RunID, "err", err)
	}
}

// callWriteGap bounds how often one run's calls are written.
const callWriteGap = 5 * time.Second

// pruneCalls forgets each run's last call that is callWriteGap old or
// more. Such an entry holds back no write, so forgetting it changes no
// answer, and a settled run leaves nothing behind.
func (s *Scheduler) pruneCalls() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for run, at := range s.lastCall {
		if now.Sub(at) >= callWriteGap {
			delete(s.lastCall, run)
		}
	}
}
