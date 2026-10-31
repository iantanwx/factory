package retro

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// SignalKind is the fault an item showed for the second time.
type SignalKind string

const (
	SignalRedBuild     SignalKind = "red_build"     // a ticket's red build; no scope
	SignalAttempts     SignalKind = "attempts"      // a failed attempt at the same inputs; scope is the stage
	SignalSecondReject SignalKind = "second_reject" // a document reject; scope is prd or spec
)

// Signal is one row of signals, as work show prints it. It holds at least
// two cases.
type Signal struct {
	ID    int64      `json:"id"`
	Kind  SignalKind `json:"kind"`
	Scope string     `json:"scope"`
	Cases []CaseRef  `json:"cases"`
	At    time.Time  `json:"at"`
}

// rejectScopes is the scope of a reject from each document review.
var rejectScopes = map[string]string{machine.PRDReview: "prd", machine.SpecReview: "spec"}

// SignalsFor is the machine's Applied hook. It writes the red_build signal
// at a ticket's second red build, and the second_reject signal at an
// epic's second reject of one document. It counts over transitions, which
// already hold ev's own row, and writes one case for each counted row.
// Every other move writes nothing.
func SignalsFor(tx *store.Tx, ev machine.AppliedEvent) error {
	if ev.Kind == machine.KindTicket && (ev.Verb == machine.VerbCIRed || ev.Verb == "dequeue_red") {
		return countInto(tx, ev, SignalRedBuild, "", `verb IN (?, 'dequeue_red')`, machine.VerbCIRed)
	}
	if scope, ok := rejectScopes[ev.From]; ok && ev.Kind == machine.KindEpic && ev.Verb == "review_reject" {
		return countInto(tx, ev, SignalSecondReject, scope, `verb = 'review_reject' AND from_status = ?`, ev.From)
	}
	return nil
}

// countInto adds a case for each transition of ev's item that where
// matches to the signal of kind and scope.
func countInto(tx *store.Tx, ev machine.AppliedEvent, kind SignalKind, scope, where string, args ...any) error {
	rows, err := tx.Query(`SELECT intent_id FROM transitions WHERE project = ? AND item = ? AND `+where+` ORDER BY intent_id`,
		append([]any{ev.Project, ev.Item}, args...)...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var cases []CaseRef
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		cases = append(cases, CaseRef(fmt.Sprintf("transition:%d", id)))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return addCases(tx, ev.Project, ev.Item, kind, scope, cases)
}

// RecordAttempts writes the attempts signal of item at stage, with one case
// for each failed run. alertHeld calls it when it raises retry_limit.
func RecordAttempts(ctx context.Context, st *store.Store, project, item, stage string, runs []string) error {
	cases := make([]CaseRef, 0, len(runs))
	for _, r := range runs {
		cases = append(cases, CaseRef("run:"+r))
	}
	return st.Write(ctx, func(tx *store.Tx) error { return addCases(tx, project, item, SignalAttempts, stage, cases) })
}

// addCases joins cases to the signal row of (project, item, kind, scope),
// and keeps each case once. A signal with fewer than two cases is never
// written.
func addCases(tx *store.Tx, project, item string, kind SignalKind, scope string, cases []CaseRef) error {
	var raw string
	err := tx.QueryRow(`SELECT cases FROM signals WHERE project = ? AND item = ? AND kind = ? AND scope = ?`,
		project, item, kind, scope).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var all []CaseRef
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &all); err != nil {
			return err
		}
	}
	for _, c := range cases {
		if !slices.Contains(all, c) {
			all = append(all, c)
		}
	}
	if len(all) < 2 {
		return nil
	}
	b, err := json.Marshal(all)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO signals (project, item, kind, scope, cases, at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (project, item, kind, scope) DO UPDATE SET cases = excluded.cases`,
		project, item, kind, scope, string(b), tx.Now().UnixMilli())
	return err
}
