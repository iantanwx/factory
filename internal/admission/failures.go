package admission

import (
	"context"
	"database/sql"

	"github.com/iantanwx/loop/internal/store"
)

// FailureLimit is the number of counted failures with unchanged inputs
// that stops a stage from taking an item again (R13).
const FailureLimit = 2

// attemptPending is the class of an attempt in flight. Admission writes the
// row when it reserves the run, so the attempt's inputs stay with the run
// id. Settlement turns the row into a counted failure or deletes it. A
// pending row never counts: a run whose end is unknown is never counted
// (KTD8).
const attemptPending = "pending"

// counted is the SQL list of the classes that count.
const counted = `('` + string(ClassFailed) + `', '` + string(ClassCrashed) + `', '` + string(ClassStalled) + `')`

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

type readTx struct {
	ctx context.Context
	tx  *sql.Tx
}

func (r readTx) QueryRow(query string, args ...any) *sql.Row {
	return r.tx.QueryRowContext(r.ctx, query, args...)
}

// failuresFor counts the counted failures of one item and stage with these
// inputs.
func failuresFor(q querier, project, item, stage, inputs string) (int, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM failures WHERE project = ? AND item = ? AND stage = ? AND inputs_hash = ?
		AND class IN `+counted, project, item, stage, inputs).Scan(&n)
	return n, err
}

// mayBeHeld reports whether some inputs of the item and stage reached the
// limit. Only then is the item's identity worth an outside read.
func mayBeHeld(q querier, project, item, stage string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT coalesce(max(c), 0) FROM (SELECT count(*) AS c FROM failures
		WHERE project = ? AND item = ? AND stage = ? AND class IN `+counted+` GROUP BY inputs_hash)`,
		project, item, stage).Scan(&n)
	return n >= FailureLimit, err
}

// countedFailures lists the counted failures of one item and stage with
// these inputs, oldest first.
func countedFailures(tx *store.Tx, project, item, stage, inputs string) ([]Failure, error) {
	rs, err := tx.Query(`SELECT run_id, class, note FROM failures WHERE project = ? AND item = ? AND stage = ? AND inputs_hash = ?
		AND class IN `+counted+` ORDER BY recorded_at, run_id`, project, item, stage, inputs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []Failure
	for rs.Next() {
		var f Failure
		if err := rs.Scan(&f.RunID, &f.Class, &f.Note); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rs.Err()
}
