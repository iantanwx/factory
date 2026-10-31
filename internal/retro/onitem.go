package retro

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// OnItem reads what the retro keeps on one item: its signals and the held
// learnings whose retro item it is, oldest first. work show prints both.
func OnItem(ctx context.Context, st *store.Store, project, item string) (machine.RetroOnItem, error) {
	var out machine.RetroOnItem
	err := st.Read(ctx, func(tx *sql.Tx) error {
		signals, err := readSignals(ctx, tx, project, item)
		if err != nil {
			return err
		}
		for _, s := range signals {
			raw, err := json.Marshal(s)
			if err != nil {
				return err
			}
			out.Signals = append(out.Signals, raw)
		}
		held, err := readStored(ctx, tx, `project = ? AND retro_item = ? AND status = ?`, project, item, StatusHeld)
		if err != nil {
			return err
		}
		for _, l := range held {
			raw, err := json.Marshal(l)
			if err != nil {
				return err
			}
			out.HeldLearnings = append(out.HeldLearnings, raw)
		}
		return nil
	})
	return out, err
}

// readSignals reads the signals of one item, oldest first.
func readSignals(ctx context.Context, tx *sql.Tx, project, item string) ([]Signal, error) {
	var out []Signal
	err := eachRow(ctx, tx, `SELECT id, kind, scope, cases, at FROM signals WHERE project = ? AND item = ? ORDER BY id`,
		[]any{project, item}, func(rows *sql.Rows) error {
			var (
				s     Signal
				cases string
				at    int64
			)
			if err := rows.Scan(&s.ID, &s.Kind, &s.Scope, &cases, &at); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(cases), &s.Cases); err != nil {
				return err
			}
			s.At = time.UnixMilli(at)
			out = append(out, s)
			return nil
		})
	return out, err
}
