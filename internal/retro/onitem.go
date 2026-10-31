package retro

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// OnItem reads what the retro keeps on one item: its signals, oldest
// first. work show prints them.
func OnItem(ctx context.Context, st *store.Store, project, item string) (machine.RetroOnItem, error) {
	var out machine.RetroOnItem
	err := st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, kind, scope, cases, at FROM signals WHERE project = ? AND item = ? ORDER BY id`, project, item)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
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
			raw, err := json.Marshal(s)
			if err != nil {
				return err
			}
			out.Signals = append(out.Signals, raw)
		}
		return rows.Err()
	})
	return out, err
}
