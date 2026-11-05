package overwatch

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// claimsPart holds work for each lease whose owner run ended, or has no
// run row (C15). The scheduler frees such a lease on its tick, so one that
// a sweep sees is a lease the scheduler failed to free. A lease of a live
// run reads code 0.
func claimsPart() Part {
	return Part{Name: "claims", Read: func(ctx context.Context, w World) (Reading, error) {
		leases, err := readLeases(ctx, w)
		if err != nil {
			return Reading{}, err
		}
		return classifyClaims(leases), nil
	}}
}

// heldLease is one lease with an owner, and its owner's run row.
type heldLease struct {
	project, item, run string
	hasRun             bool
	ended              time.Time // zero while the run lives
}

func readLeases(ctx context.Context, w World) ([]heldLease, error) {
	var out []heldLease
	err := w.Store().Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT l.project, l.item, l.owner_run_id, r.run_id IS NOT NULL, r.ended_at
			FROM leases l LEFT JOIN runs r ON r.run_id = l.owner_run_id
			WHERE l.owner_run_id IS NOT NULL AND l.owner_run_id <> ''
			ORDER BY l.project, l.item`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var (
				l     heldLease
				ended sql.NullInt64
			)
			if err := rs.Scan(&l.project, &l.item, &l.run, &l.hasRun, &ended); err != nil {
				return err
			}
			if ended.Valid {
				l.ended = time.UnixMilli(ended.Int64).UTC()
			}
			out = append(out, l)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read the leases: %w", err)
	}
	return out, nil
}

// classifyClaims is the claims part's rule over its rows.
func classifyClaims(leases []heldLease) Reading {
	var r Reading
	live := 0
	for _, l := range leases {
		key := Key("claims", l.item)
		switch {
		case !l.hasRun:
			r.Findings = append(r.Findings, Finding{Key: key, Project: l.project,
				Line: fmt.Sprintf("%s of %s: its lease names run %s, which has no run row (%s)", l.item, l.project, l.run, key)})
		case !l.ended.IsZero():
			r.Findings = append(r.Findings, Finding{Key: key, Project: l.project,
				Line: fmt.Sprintf("%s of %s: its lease names run %s, which ended at %s (%s)", l.item, l.project, l.run,
					l.ended.Format(time.RFC3339), key)})
		default:
			live++
		}
	}
	r.Notes = append(r.Notes, fmt.Sprintf("leases that a live run holds: %d", live))
	return r
}
