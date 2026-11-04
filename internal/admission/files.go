package admission

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// flight is one live implementer run, and the "May change" entries of the
// ticket it holds. Two implementers that edit one file open two pull
// requests that conflict, and a conflict costs a review round. So
// admission takes no ticket whose list meets a list in flight
// (implementer-queue.ts, alpha, plan 2026-10-08 decision 2).
type flight struct {
	run, item string
	entries   []string
}

// flights reads the run and the item of each live implementer run of the
// project, oldest first, and the list of each one's ticket from beads. A
// reservation that has no lease yet counts, since its claim lands next. It
// makes outside reads, so no caller holds a lock. One firing of a stage
// runs at a time in each project, and it admits its runs in turn, so no
// other implementer of the project is admitted between this read and the
// reservation.
func (a *Admission) flights(ctx context.Context, project string) ([]flight, error) {
	var out []flight
	err := a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT run_id, item FROM runs WHERE project = ? AND stage = ? AND ended_at IS NULL
			AND item <> '' ORDER BY started_at, run_id`, project, config.StageImplementer)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var f flight
			if err := rows.Scan(&f.run, &f.item); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for i := range out {
		t, err := a.task(ctx, project, out[i].item)
		if err != nil {
			return nil, fmt.Errorf("admission: read %s in flight: %w", out[i].item, err)
		}
		out[i].entries = MayChangeEntries(t.Body)
	}
	return out, nil
}

// filesRefusal reads the item's list from beads, and refuses the item when
// an entry meets an entry of a ticket in flight. An item with no list is
// never refused. It makes an outside read, so no caller holds a lock.
func (a *Admission) filesRefusal(ctx context.Context, it machine.Item, flights []flight) (*cli.Refusal, error) {
	if len(flights) == 0 {
		return nil, nil
	}
	t, err := a.task(ctx, it.Project, it.ID)
	if err != nil {
		return nil, fmt.Errorf("admission: read %s: %w", it.ID, err)
	}
	entries := MayChangeEntries(t.Body)
	for _, f := range flights {
		if mine, theirs, ok := MeetingEntries(entries, f.entries); ok && f.item != it.ID {
			return &cli.Refusal{Code: cli.CodeCapFull, Rule: "files_in_flight", Item: it.ID, CurrentStatus: it.Status,
				Hint: fmt.Sprintf("%s may change `%s`, which meets `%s` of %s, and implementer run %s holds %s until it settles",
					it.ID, mine, theirs, f.item, f.run, f.item)}, nil
		}
	}
	return nil, nil
}

// MeetingEntries returns the first entry of mine and the entry of theirs
// that it meets by config.EntriesMeet, when two "May change" lists meet.
// Admission refuses a ticket with rule files_in_flight by it, and the
// stalls part reads that refusal by it.
func MeetingEntries(mine, theirs []string) (string, string, bool) {
	for _, m := range mine {
		for _, o := range theirs {
			if config.EntriesMeet(m, o) {
				return m, o, true
			}
		}
	}
	return "", "", false
}
