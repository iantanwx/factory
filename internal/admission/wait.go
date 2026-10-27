package admission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// WaitOf builds the wait of one item from the store: the run that holds its
// lease, its holds, every counted failure, and the open alerts of its
// project that name it by AlertNamesItem, the last seen first. It selects
// failures with the counted list, as the retry gate does.
func WaitOf(st *store.Store) machine.WaitReader {
	return func(ctx context.Context, project, item string) (machine.Wait, error) {
		w := machine.Wait{Holds: []machine.WaitHold{}, Failures: []machine.WaitFailure{}, Limit: FailureLimit, Alerts: []machine.WaitAlert{}}
		err := st.Read(ctx, func(tx *sql.Tx) error {
			var l machine.WaitLease
			var granted, renewed int64
			err := tx.QueryRowContext(ctx, `SELECT owner_run_id, epoch, coalesce(granted_at, 0), coalesce(renewed_at, 0) FROM leases
				WHERE project = ? AND item = ? AND coalesce(owner_run_id, '') <> ''`, project, item).Scan(&l.Run, &l.Epoch, &granted, &renewed)
			switch {
			case err == nil:
				l.Granted, l.Renewed = time.UnixMilli(granted), time.UnixMilli(renewed)
				w.Lease = &l
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
			if err := scanRows(ctx, tx, `SELECT holder, kind, created_at FROM holds WHERE project = ? AND item = ?
				ORDER BY created_at, holder`, func(rs *sql.Rows) error {
				var h machine.WaitHold
				var since int64
				if err := rs.Scan(&h.Holder, &h.Kind, &since); err != nil {
					return err
				}
				h.Since = time.UnixMilli(since)
				w.Holds = append(w.Holds, h)
				return nil
			}, project, item); err != nil {
				return err
			}
			if err := scanRows(ctx, tx, `SELECT run_id, stage, class, inputs_hash, note, recorded_at FROM failures
				WHERE project = ? AND item = ? AND class IN `+counted+` ORDER BY recorded_at, run_id`, func(rs *sql.Rows) error {
				var f machine.WaitFailure
				var at int64
				if err := rs.Scan(&f.Run, &f.Stage, &f.Class, &f.Inputs, &f.Note, &at); err != nil {
					return err
				}
				f.At = time.UnixMilli(at)
				w.Failures = append(w.Failures, f)
				return nil
			}, project, item); err != nil {
				return err
			}
			return scanRows(ctx, tx, `SELECT id, kind, subject, dedupe_key FROM alerts WHERE project = ? AND state = 'open'
				AND (instr(dedupe_key, ?) > 0 OR instr(subject, ?) > 0) ORDER BY last_at DESC, id DESC`, func(rs *sql.Rows) error {
				var a machine.WaitAlert
				var id int64
				var key string
				if err := rs.Scan(&id, &a.Kind, &a.Subject, &key); err != nil {
					return err
				}
				if AlertNamesItem(key, a.Subject, item) {
					a.Entry = fmt.Sprintf("alert/%d", id)
					w.Alerts = append(w.Alerts, a)
				}
				return nil
			}, project, item, item)
		})
		if err != nil {
			return machine.Wait{}, err
		}
		return w, nil
	}
}

// scanRows runs one query and hands each row to scan.
func scanRows(ctx context.Context, tx *sql.Tx, query string, scan func(*sql.Rows) error, args ...any) error {
	rs, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		if err := scan(rs); err != nil {
			return err
		}
	}
	return rs.Err()
}
