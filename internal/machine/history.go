package machine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/iantanwx/loop/internal/store"
)

// Transition is one applied move in the history of an item.
type Transition struct {
	IntentID int64             `json:"intent_id"`
	Project  string            `json:"project"`
	Item     string            `json:"item"`
	Seq      int64             `json:"seq"`
	Kind     Kind              `json:"kind"`
	Verb     string            `json:"verb"`
	From     string            `json:"from"`
	To       string            `json:"to"`
	Epoch    int64             `json:"epoch"`
	Actor    string            `json:"actor"`
	RunID    string            `json:"run_id"`
	Says     string            `json:"says,omitempty"`
	Set      map[string]string `json:"set,omitempty"`
	Unset    []string          `json:"unset,omitempty"`
	At       time.Time         `json:"at"`
}

// insertHistory writes the history row of an intent in its applied mark.
func insertHistory(tx *store.Tx, in store.Intent, f facts) error {
	raw, err := json.Marshal(struct {
		Set   map[string]string `json:"set,omitempty"`
		Unset []string          `json:"unset,omitempty"`
	}{f.Set, f.Unset})
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO transitions
		(intent_id, project, item, seq, kind, verb, from_status, to_status, epoch, actor, run_id, says, facts, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Project, in.Item, in.Seq, in.Kind, in.Verb, in.ExpectedStatus, in.TargetStatus, f.Epoch,
		in.Actor, in.RunID, f.Says, string(raw), tx.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("machine: history of intent %d: %w", in.ID, err)
	}
	return nil
}

// History returns the applied moves of one item in sequence order.
func (m *Machine) History(ctx context.Context, project, item string) ([]Transition, error) {
	var out []Transition
	err := m.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT intent_id, project, item, seq, kind, verb, from_status, to_status,
			epoch, actor, run_id, says, facts, at FROM transitions WHERE project = ? AND item = ? ORDER BY seq`, project, item)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				t   Transition
				raw string
				at  int64
			)
			if err := rows.Scan(&t.IntentID, &t.Project, &t.Item, &t.Seq, &t.Kind, &t.Verb, &t.From, &t.To,
				&t.Epoch, &t.Actor, &t.RunID, &t.Says, &raw, &at); err != nil {
				return err
			}
			var f struct {
				Set   map[string]string `json:"set"`
				Unset []string          `json:"unset"`
			}
			if err := json.Unmarshal([]byte(raw), &f); err != nil {
				return err
			}
			t.Set, t.Unset, t.At = f.Set, f.Unset, time.UnixMilli(at)
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}
