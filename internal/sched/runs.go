package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/iantanwx/loop/internal/admission"
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

// ErrNoRun is the error of a run id the store does not hold.
var ErrNoRun = errors.New("sched: no such run")

func (s *Scheduler) runByID(ctx context.Context, id string) (runRow, error) {
	rs, err := s.queryRuns(ctx, `r.run_id = ?`, id)
	if err != nil {
		return runRow{}, err
	}
	if len(rs) == 0 {
		return runRow{}, fmt.Errorf("%w: %s", ErrNoRun, id)
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

// RunView is one run as `run show` and `run list` answer it. Lease,
// Failure and Alerts are set on run show only.
type RunView struct {
	RunID    string       `json:"run"`
	Project  string       `json:"project"`
	Stage    string       `json:"stage"`
	Item     string       `json:"item,omitempty"`
	Harness  string       `json:"harness"`
	Model    string       `json:"model"`
	State    string       `json:"state"`
	Outcome  string       `json:"outcome,omitempty"` // outcome_class
	Fault    string       `json:"fault,omitempty"`
	PackHash string       `json:"pack_hash,omitempty"`
	Started  time.Time    `json:"started_at"`
	Ended    *time.Time   `json:"ended_at,omitempty"`
	LastCall *time.Time   `json:"last_cli_call_at,omitempty"`
	Checkout string       `json:"checkout,omitempty"`
	Session  string       `json:"session,omitempty"` // the agent host's session id
	Lease    *LeaseView   `json:"lease,omitempty"`
	Failure  *FailureView `json:"failure,omitempty"`
	Alerts   []AlertRef   `json:"alerts,omitempty"`
}

// LeaseView is the lease a run owns.
type LeaseView struct {
	Item    string    `json:"item"`
	Epoch   int64     `json:"epoch"`
	Granted time.Time `json:"granted_at"`
	Renewed time.Time `json:"renewed_at"`
}

// FailureView is a run's failures row.
type FailureView struct {
	Stage  string    `json:"stage"`
	Class  string    `json:"class"`
	Inputs string    `json:"inputs"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"recorded_at"`
}

// AlertRef is one alert that names a run.
type AlertRef struct {
	Entry   string `json:"entry"` // alert/<id>, as the inbox names it
	Kind    string `json:"kind"`
	State   string `json:"state"` // open or closed
	Subject string `json:"subject"`
}

// RunFilter narrows `run list`. The handler checks 1 <= Limit <= 500.
type RunFilter struct {
	Project, Stage string
	Live           bool
	Limit          int
}

// RunEvent is one transition or one audit row of a run.
type RunEvent struct {
	At     time.Time       `json:"at"`
	Source string          `json:"source"` // transition or audit
	Item   string          `json:"item,omitempty"`
	Verb   string          `json:"verb,omitempty"`
	From   string          `json:"from,omitempty"`
	To     string          `json:"to,omitempty"`
	Says   string          `json:"says,omitempty"`
	Action string          `json:"action,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// runAlerts caps the alerts that run show answers.
const runAlerts = 20

const viewColumns = `r.run_id, r.project, r.stage, r.item, r.harness, r.model, r.state, coalesce(r.outcome_class, ''),
	r.fault_id, r.pack_hash, r.started_at, r.ended_at, r.last_cli_call_at, r.host_identity,
	coalesce((SELECT path FROM checkouts c WHERE c.run_id = r.run_id ORDER BY c.created_at DESC LIMIT 1), '')`

func scanView(sc store.Scanner) (RunView, error) {
	var (
		v               RunView
		started         int64
		ended, lastCall sql.NullInt64
		rec             string
	)
	if err := sc.Scan(&v.RunID, &v.Project, &v.Stage, &v.Item, &v.Harness, &v.Model, &v.State, &v.Outcome,
		&v.Fault, &v.PackHash, &started, &ended, &lastCall, &rec, &v.Checkout); err != nil {
		return v, err
	}
	v.Started = time.UnixMilli(started)
	v.Ended, v.LastCall = msPtr(ended), msPtr(lastCall)
	if s := decodeRecord(rec).Session; s != nil {
		v.Session = s.ID.String()
	}
	return v, nil
}

func msPtr(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.UnixMilli(n.Int64)
	return &t
}

// ShowRun answers one run with its lease, its failures row and the
// alerts that name it, open or closed, the last seen first. An unknown run is
// ErrNoRun.
func ShowRun(ctx context.Context, st *store.Store, run string) (RunView, error) {
	var v RunView
	err := st.Read(ctx, func(tx *sql.Tx) error {
		var err error
		v, err = scanView(tx.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM runs r WHERE r.run_id = ?`, run))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNoRun, run)
		}
		if err != nil {
			return err
		}
		var l LeaseView
		var granted, renewed int64
		err = tx.QueryRowContext(ctx, `SELECT item, epoch, coalesce(granted_at, 0), coalesce(renewed_at, 0) FROM leases
			WHERE owner_run_id = ? LIMIT 1`, run).Scan(&l.Item, &l.Epoch, &granted, &renewed)
		switch {
		case err == nil:
			l.Granted, l.Renewed = time.UnixMilli(granted), time.UnixMilli(renewed)
			v.Lease = &l
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		var f FailureView
		var at int64
		err = tx.QueryRowContext(ctx, `SELECT stage, class, inputs_hash, note, recorded_at FROM failures WHERE run_id = ?`, run).
			Scan(&f.Stage, &f.Class, &f.Inputs, &f.Note, &at)
		switch {
		case err == nil:
			f.At = time.UnixMilli(at)
			v.Failure = &f
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		v.Alerts, err = alertsNaming(ctx, tx, run)
		return err
	})
	return v, err
}

// alertsNaming lists the alerts whose dedupe key or subject names the run
// by admission.AlertNamesRun, the last seen first, at most runAlerts.
func alertsNaming(ctx context.Context, tx *sql.Tx, run string) ([]AlertRef, error) {
	rs, err := tx.QueryContext(ctx, `SELECT id, kind, state, subject, dedupe_key FROM alerts
		WHERE instr(dedupe_key, ?) > 0 OR instr(subject, ?) > 0 ORDER BY last_at DESC, id DESC`, run, run)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []AlertRef
	for rs.Next() && len(out) < runAlerts {
		var (
			a   AlertRef
			id  int64
			key string
		)
		if err := rs.Scan(&id, &a.Kind, &a.State, &a.Subject, &key); err != nil {
			return nil, err
		}
		if admission.AlertNamesRun(key, a.Subject, run) {
			a.Entry = fmt.Sprintf("alert/%d", id)
			out = append(out, a)
		}
	}
	return out, rs.Err()
}

// ListRuns answers the runs that match f, newest first, at most f.Limit.
func ListRuns(ctx context.Context, st *store.Store, f RunFilter) ([]RunView, error) {
	out := []RunView{}
	err := st.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT `+viewColumns+` FROM runs r
			WHERE (? = '' OR r.project = ?) AND (? = '' OR r.stage = ?) AND (NOT ? OR r.ended_at IS NULL)
			ORDER BY r.started_at DESC, r.run_id DESC LIMIT ?`, f.Project, f.Project, f.Stage, f.Stage, f.Live, f.Limit)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			v, err := scanView(rs)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rs.Err()
	})
	return out, err
}

// RunEvents answers the transitions with the run's run_id and the audit
// rows whose actor is the run, oldest first. At one time a transition
// comes before an audit row, then the row id decides. An unknown run is
// ErrNoRun.
func RunEvents(ctx context.Context, st *store.Store, run string) ([]RunEvent, error) {
	out := []RunEvent{}
	err := st.Read(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE run_id = ?`, run).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: %s", ErrNoRun, run)
		}
		// A run's actor is <kind>:<run id>, as rpc.Principal.Actor writes it.
		rs, err := tx.QueryContext(ctx, `SELECT at, 0 AS src, intent_id AS id, item, verb, from_status, to_status, says, '', ''
				FROM transitions WHERE run_id = ?
			UNION ALL
			SELECT at, 1, id, '', '', '', '', '', action, detail FROM audit WHERE actor IN (?, ?, ?)
			ORDER BY at, src, id`, run, "run:"+run, "room:"+run, "coordinator:"+run)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var (
				e           RunEvent
				at, src, id int64
				detail      string
			)
			if err := rs.Scan(&at, &src, &id, &e.Item, &e.Verb, &e.From, &e.To, &e.Says, &e.Action, &detail); err != nil {
				return err
			}
			e.At, e.Source = time.UnixMilli(at), "transition"
			if src == 1 {
				e.Source, e.Detail = "audit", json.RawMessage(detail)
			}
			out = append(out, e)
		}
		return rs.Err()
	})
	return out, err
}
