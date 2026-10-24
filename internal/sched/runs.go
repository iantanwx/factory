package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/store"
)

// Run states the scheduler writes. Admission writes admitted and settled.
const (
	RunStarting = "starting"
	RunRunning  = "running"
	RunUnknown  = "unknown"
)

// hostRecord is what the server keeps about a run's session. It lives in
// the runs.host_identity column as JSON, so no migration is needed.
type hostRecord struct {
	Session *agenthost.Session `json:"session,omitempty"`
	// UnknownSince is when the host first could not say whether the
	// session lives, in unix milliseconds. Zero while it can.
	UnknownSince int64 `json:"unknown_since,omitempty"`
	// ClosedAt is when the server closed the ended session.
	ClosedAt int64 `json:"closed_at,omitempty"`
	// Resume counts the nudges the server sent a session idle on a
	// transient model API error (resume.go).
	Resume *resumeRecord `json:"resume,omitempty"`
}

// resumeRecord is the resume state of one outage as the session saw it.
type resumeRecord struct {
	// First is the APIError.First of the outage, in unix milliseconds. An
	// error with another First is a new outage, and its nudges start over.
	First int64 `json:"first"`
	// Nudges are the times of each nudge sent, in unix milliseconds.
	Nudges []int64 `json:"nudges,omitempty"`
}

func (r hostRecord) encode() string {
	raw, _ := json.Marshal(r)
	return string(raw)
}

func decodeRecord(s string) hostRecord {
	var r hostRecord
	if s != "" {
		_ = json.Unmarshal([]byte(s), &r)
	}
	return r
}

// runRow is one run as the scheduler reads it.
type runRow struct {
	ID, Project, Stage, Item, Harness, State string
	Started, LastCall                        time.Time
	Rec                                      hostRecord
	Checkout                                 string
	Ended                                    bool
	Class                                    string
}

const runColumns = `r.run_id, r.project, r.stage, r.item, r.harness, r.state, r.started_at, r.last_cli_call_at,
	r.host_identity, coalesce(c.path, ''), r.ended_at IS NOT NULL, coalesce(r.outcome_class, '')`

func scanRun(sc store.Scanner) (runRow, error) {
	var (
		r        runRow
		started  int64
		lastCall sql.NullInt64
		rec      string
	)
	if err := sc.Scan(&r.ID, &r.Project, &r.Stage, &r.Item, &r.Harness, &r.State, &started, &lastCall,
		&rec, &r.Checkout, &r.Ended, &r.Class); err != nil {
		return r, err
	}
	r.Started = time.UnixMilli(started)
	if lastCall.Valid {
		r.LastCall = time.UnixMilli(lastCall.Int64)
	}
	r.Rec = decodeRecord(rec)
	return r, nil
}

func (s *Scheduler) queryRuns(ctx context.Context, where string, args ...any) ([]runRow, error) {
	var out []runRow
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT `+runColumns+` FROM runs r LEFT JOIN checkouts c ON c.run_id = r.run_id
			WHERE `+where+` ORDER BY r.started_at, r.run_id`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			r, err := scanRun(rs)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rs.Err()
	})
	return out, err
}

// openRuns lists every run that has not ended.
func (s *Scheduler) openRuns(ctx context.Context) ([]runRow, error) {
	return s.queryRuns(ctx, `r.ended_at IS NULL`)
}

var errNoRun = errors.New("sched: no such run")

func (s *Scheduler) runByID(ctx context.Context, id string) (runRow, error) {
	rs, err := s.queryRuns(ctx, `r.run_id = ?`, id)
	if err != nil {
		return runRow{}, err
	}
	if len(rs) == 0 {
		return runRow{}, fmt.Errorf("%w: %s", errNoRun, id)
	}
	return rs[0], nil
}

// setRun writes a run's state and session record while it has not ended.
func (s *Scheduler) setRun(ctx context.Context, id, state string, rec hostRecord) error {
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE runs SET state = ?, host_identity = ? WHERE run_id = ? AND ended_at IS NULL`, state, rec.encode(), id)
		return err
	})
}

// setRecord writes a run's session record, ended or not.
func (s *Scheduler) setRecord(ctx context.Context, id string, rec hostRecord) error {
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE runs SET host_identity = ? WHERE run_id = ?`, rec.encode(), id)
		return err
	})
}
