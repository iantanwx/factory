package admission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/store"
)

// Class is a run's outcome class (KTD8). Only a counted class adds to the
// failure budget.
type Class string

// The outcome classes of KTD8.
const (
	ClassDone     Class = "done"      // the run finished with done
	ClassFailed   Class = "failed"    // the run finished with failed
	ClassBlocked  Class = "blocked"   // the run finished with blocked
	ClassStalled  Class = "stalled"   // the run finished with done and moved nothing
	ClassCrashed  Class = "crashed"   // the session ended with no finish
	ClassNotReady Class = "not-ready" // the agent never became ready
	ClassQuotaHit Class = "quota-hit" // the run hit a usage limit
	ClassInfra    Class = "infra"     // a server outage explains the end
	// ClassUnknown is a run whose end time nobody knows. It is never
	// counted, and RecordOutcome settles nothing for it.
	ClassUnknown Class = "unknown"
)

// Counted reports whether the class adds one failure for the run's inputs.
// A run that reports blocked asks for a person or a dependency, and its
// attempt is no failure of the task. A run that reports done and moved
// nothing did no part of the task, so it counts, or the next fire takes
// the item again with the same inputs, forever (tickertown admission.ts
// RELEASED, parity 2.12).
func (c Class) Counted() bool { return c == ClassFailed || c == ClassCrashed || c == ClassStalled }

// Outage is one span the server was down: from the last liveness tick to
// the next start (KTD8).
type Outage struct {
	Start time.Time
	End   time.Time
}

// Settlement is what the scheduler knows when a run ends.
type Settlement struct {
	RunID string
	// Finish is the outcome of `loop run finish`: done, failed or blocked.
	// Empty means the run never finished.
	Finish string
	// Held means the run still held its item's lease at its finish. Every
	// move that does the stage's work ends the claim, so a done finish
	// that still holds it moved nothing.
	Held bool
	// NotReady means the agent never became ready.
	NotReady bool
	// QuotaHit means the run hit a usage limit.
	QuotaHit bool
	// Started is when the run was admitted. RecordOutcome reads it from
	// the run's row when it is zero.
	Started time.Time
	// Ended is when the session ended. Zero means nobody knows.
	Ended time.Time
	// LastCLICall is the run's last CLI call the server observed. Zero
	// means the server observed none.
	LastCLICall time.Time
	// Outages are the recorded outage spans. The scheduler reads them
	// from the outages table.
	Outages []Outage
	// Note is a short reason the failure record keeps.
	Note string
}

// Classify returns the run's class. A finish decides, and a done finish
// whose run still held its item is stalled. A session that
// ended with no finish is infra when an outage began after the run's last
// observed activity and before the session ended: the run then made no CLI
// call after the outage. Every other such session is crashed, whether or
// not it made a CLI call (KTD8).
func Classify(s Settlement) Class {
	switch s.Finish {
	case cli.OutcomeDone:
		if s.Held {
			return ClassStalled
		}
		return ClassDone
	case cli.OutcomeFailed:
		return ClassFailed
	case cli.OutcomeBlocked:
		return ClassBlocked
	}
	switch {
	case s.NotReady:
		return ClassNotReady
	case s.QuotaHit:
		return ClassQuotaHit
	case s.Ended.IsZero():
		return ClassUnknown
	}
	last := s.Started
	if s.LastCLICall.After(last) {
		last = s.LastCLICall
	}
	for _, o := range s.Outages {
		if o.Start.After(last) && !o.Start.After(s.Ended) {
			return ClassInfra
		}
	}
	return ClassCrashed
}

// Outcome is what RecordOutcome settled.
type Outcome struct {
	Class   Class `json:"class"`
	Counted bool  `json:"counted"`
	// Replayed means an earlier call settled the run, and this call wrote
	// nothing. The class is the stored one.
	Replayed bool `json:"replayed"`
	// Inputs is the retry identity of the run's attempt. It is empty for a
	// run with no item, and for a replay.
	Inputs string `json:"inputs,omitempty"`
	// Failures are the counted failures of the run's item and stage at
	// those inputs, oldest first, when this run counted. FailureLimit of
	// them stop the stage from taking the item (R13).
	Failures []Failure `json:"failures,omitempty"`
}

// Failure is one counted failure of an item at a stage.
type Failure struct {
	RunID string `json:"run_id"`
	Class Class  `json:"class"`
	Note  string `json:"note,omitempty"`
}

// ErrUnknownRun is a settlement of a run that admission never admitted.
var ErrUnknownRun = errors.New("admission: no admitted run with that id")

// RecordOutcome settles a run's admission records in one write: it ends
// the run's row with its class, turns the attempt into a counted failure
// or drops it, and frees any stage mutex the run holds. A replay returns
// the stored class and changes nothing, so a run counts once (KTD6). A run
// with an unknown end settles nothing. The outcome names the attempt's
// inputs, and each counted failure at them when this run counted, so the
// scheduler can tell the operator that the item stopped.
func (a *Admission) RecordOutcome(ctx context.Context, s Settlement) (Outcome, error) {
	var out Outcome
	err := a.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		var (
			started int64
			ended   sql.NullInt64
			class   sql.NullString
		)
		err := tx.QueryRow(`SELECT started_at, ended_at, outcome_class FROM runs WHERE run_id = ?`, s.RunID).
			Scan(&started, &ended, &class)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrUnknownRun, s.RunID)
		}
		if err != nil {
			return err
		}
		if ended.Valid {
			c := Class(class.String)
			out = Outcome{Class: c, Counted: c.Counted(), Replayed: true}
			return nil
		}
		if s.Started.IsZero() {
			s.Started = time.UnixMilli(started)
		}
		c := Classify(s)
		out = Outcome{Class: c, Counted: c.Counted()}
		if c == ClassUnknown {
			return nil
		}
		now := tx.Now().UnixMilli()
		if _, err := tx.Exec(`UPDATE runs SET state = 'settled', outcome_class = ?, ended_at = ? WHERE run_id = ?`,
			string(c), s.Ended.UnixMilli(), s.RunID); err != nil {
			return err
		}
		var project, item, stage string
		err = tx.QueryRow(`SELECT project, item, stage, inputs_hash FROM failures WHERE run_id = ?`, s.RunID).
			Scan(&project, &item, &stage, &out.Inputs)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if c.Counted() {
			if _, err := tx.Exec(`UPDATE failures SET class = ?, note = ?, recorded_at = ? WHERE run_id = ?`,
				string(c), s.Note, now, s.RunID); err != nil {
				return err
			}
			if out.Inputs != "" {
				if out.Failures, err = countedFailures(tx, project, item, stage, out.Inputs); err != nil {
					return err
				}
			}
		} else if _, err := tx.Exec(`DELETE FROM failures WHERE run_id = ? AND class = ?`, s.RunID, attemptPending); err != nil {
			return err
		}
		return releaseMutexes(tx, s.RunID)
	})
	return out, err
}
