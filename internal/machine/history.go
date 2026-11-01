package machine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// Fact returns the value the move set on one fact, or "".
func (t Transition) Fact(name string) string { return t.Set[factPrefix+name] }

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

// mergeFacts are the facts that record a merge commit. A document's merge
// outran its review when a doc_merged row from one of its states came
// before the merge row.
var mergeFacts = map[string][]string{
	FactMergedSHA:    nil,
	FactPRDMerged:    {PRDReview, PRDAmending},
	FactSpecMerged:   {SpecReview, SpecAmending},
	FactDesignMerged: {DesignReview},
	FactADRMerged:    nil,
}

// MergeRecord is the transition row that recorded a merge commit.
type MergeRecord struct {
	Item         string
	Kind         Kind
	Verb         string // merged, doc_merged or adr_landed
	Fact         string // merged_sha, prd_merged, spec_merged, design_merged or adr_merged
	ReviewedHead string // the item's reviewed_head at that transition, or ""
	Outran       bool   // the document merged during its review or amendment
	Seq          int64
}

// MergeOf finds the record of one commit. ok is false when no row of the
// project set a record fact to sha. A later row that clears the fact
// leaves the record, and of two rows that set one commit the lower
// intent_id wins.
func (m *Machine) MergeOf(ctx context.Context, project, sha string) (rec MergeRecord, ok bool, err error) {
	keys := make([]any, 0, len(mergeFacts))
	for f := range mergeFacts {
		keys = append(keys, factPrefix+f)
	}
	var key string
	err = m.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT t.item, t.kind, t.verb, t.seq, j.key FROM transitions t, json_each(t.facts, '$.set') j
			WHERE t.project = ? AND j.value = ? AND j.key IN (?`+strings.Repeat(", ?", len(keys)-1)+`) ORDER BY t.intent_id LIMIT 1`,
			append([]any{project, sha}, keys...)...).Scan(&rec.Item, &rec.Kind, &rec.Verb, &rec.Seq, &key)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return MergeRecord{}, false, nil
	}
	if err != nil {
		return MergeRecord{}, false, err
	}
	rec.Fact = strings.TrimPrefix(key, factPrefix)
	if rec.Fact == FactADRMerged {
		return rec, true, nil
	}
	h, err := m.History(ctx, project, rec.Item)
	if err != nil {
		return MergeRecord{}, false, err
	}
	for _, t := range h {
		if t.Seq >= rec.Seq {
			break
		}
		if v, set := t.Set[factPrefix+FactReviewedHead]; set {
			rec.ReviewedHead = v
		} else if slices.Contains(t.Unset, factPrefix+FactReviewedHead) {
			rec.ReviewedHead = ""
		}
		if t.Verb == "doc_merged" && slices.Contains(mergeFacts[rec.Fact], t.From) {
			rec.Outran = true
		}
	}
	return rec, true, nil
}
