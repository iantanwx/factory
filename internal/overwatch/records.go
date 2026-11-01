package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/machine"
)

// Tokens splits text on every character a key cannot hold, and trims a
// final "." or ":" from each token. One tokenizer reads finding keys and
// fault ids, so F-1 does not match F-12.
func Tokens(text string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return !keyRune(r) }) {
		if t := strings.TrimRight(f, ".:"); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Records is every open record's tokens, and the record that holds each
// one: a bug or a decision in any project, an unanswered prepared
// question, and an open or acknowledged loop fault.
type Records struct {
	names  map[string]string    // token -> record name, such as "factorio-x1y", "F-3" or "question 7"
	before map[string]time.Time // token -> the time a bounded record files it before
}

// Names returns the record that holds key as a token, for a finding with
// no time.
func (r Records) Names(key string) (record string, ok bool) {
	return r.Files(Finding{Key: key})
}

// Files returns the record that holds f's key as a token. A bounded
// record files f only when f is older than the bound.
func (r Records) Files(f Finding) (record string, ok bool) {
	record, ok = r.names[f.Key]
	if until, bounded := r.before[f.Key]; ok && bounded && !f.At.Before(until) {
		return "", false
	}
	return record, ok
}

// add files each token of text under record. A zero before files it with
// no bound. The first record to hold a token keeps it, except that a
// record with no bound takes a token from a bounded one, and a bounded
// record takes it from one with an earlier bound (C-bug-1).
func (r *Records) add(record, text string, keep func(string) bool, before time.Time) {
	if r.names == nil {
		r.names, r.before = map[string]string{}, map[string]time.Time{}
	}
	for _, t := range Tokens(text) {
		if keep != nil && !keep(t) {
			continue
		}
		_, held := r.names[t]
		until, bounded := r.before[t]
		switch {
		case !held:
		case bounded && before.IsZero():
			delete(r.before, t)
		case bounded && before.After(until):
		default:
			continue
		}
		r.names[t] = record
		if !before.IsZero() {
			r.before[t] = before
		}
	}
}

// ReadRecords reads every open record once (spec, Approach, "Keys and
// records"):
//   - a bug or a decision in any project in a state that is not terminal
//     for its kind, and a bug that `work shape` closed while its ticket is
//     not terminal: its body, and the title of a decision;
//   - a bug that `work duplicate` closed while its duplicate_of item is
//     done or not terminal: its body, for keys that start
//     "overwatch:flaky:" only. While that item is open, the bug files the
//     key as a shaped bug does. Once it is done, the bug files a flake
//     only when the flake's run started before beads last wrote the done
//     item, which is its merge or later. Such a run is at a head older
//     than the fix, so it holds no work (C-bug-1). A newer flake rings. A
//     bug that duplicates a canceled item files nothing;
//   - a prepared question with no answer: its body;
//   - a loop fault in open or acknowledged: its text, for keys that start
//     "overwatch:" only, so a fault that cites another files nothing.
func ReadRecords(ctx context.Context, w World) (Records, error) {
	var r Records
	projects, err := w.Projects(ctx)
	if err != nil {
		return r, fmt.Errorf("read the projects: %w", err)
	}
	for _, p := range projects {
		items, err := w.Items(ctx, p.ID, machine.ListFilter{})
		if err != nil {
			return r, fmt.Errorf("read the items of %s: %w", p.ID, err)
		}
		status := make(map[string]machine.Item, len(items))
		for _, it := range items {
			status[it.ID] = it
		}
		for _, it := range items {
			var keep func(string) bool
			var before time.Time
			if !openRecord(it, status) {
				var ok bool
				if before, ok = duplicateOfAFix(it, status); !ok {
					continue
				}
				keep = func(t string) bool { return strings.HasPrefix(t, "overwatch:flaky:") }
			}
			body, err := w.Body(ctx, p.ID, it.ID)
			if err != nil {
				return r, fmt.Errorf("read the body of %s: %w", it.ID, err)
			}
			if it.Kind == machine.KindDecision {
				body = it.Title + "\n" + body
			}
			r.add(it.ID, body, keep, before)
		}
	}
	st := w.Store()
	if st == nil {
		return r, errors.New("no store to read questions and faults from")
	}
	err = st.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT id, body FROM questions WHERE answered_at IS NULL ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var id int64
			var body string
			if err := rs.Scan(&id, &body); err != nil {
				return err
			}
			r.add(fmt.Sprintf("question %d", id), body, nil, time.Time{})
		}
		if err := rs.Err(); err != nil {
			return err
		}
		faults, err := fault.Pending(ctx, tx)
		if err != nil {
			return err
		}
		for _, f := range faults {
			r.add(f.ID, f.Text, func(t string) bool { return strings.HasPrefix(t, "overwatch:") }, time.Time{})
		}
		return nil
	})
	if err != nil {
		return r, fmt.Errorf("read the questions and faults: %w", err)
	}
	return r, nil
}

// openRecord reports whether it is a record: a bug or a decision whose
// state is not terminal for its kind, or a shaped bug whose ticket, in the
// same project, is not terminal.
func openRecord(it machine.Item, items map[string]machine.Item) bool {
	if it.Kind != machine.KindBug && it.Kind != machine.KindDecision {
		return false
	}
	if !terminal(it) {
		return true
	}
	if it.Kind != machine.KindBug || it.Status != machine.Shaped {
		return false
	}
	ticket, ok := items[it.Fact(machine.FactTicket)]
	return ok && !terminal(ticket)
}

// duplicateOfAFix reports whether it is a bug closed as a duplicate of an
// item, in the same project, that is done or not terminal. before is when
// beads last wrote the done item, and zero while the item is open. A done
// item with no write time bounds nothing, so the bug files nothing.
func duplicateOfAFix(it machine.Item, items map[string]machine.Item) (before time.Time, ok bool) {
	if it.Kind != machine.KindBug || it.Status != machine.Duplicate {
		return time.Time{}, false
	}
	of, ok := items[it.Fact(machine.FactDuplicateOf)]
	switch {
	case !ok:
		return time.Time{}, false
	case !terminal(of):
		return time.Time{}, true
	}
	return of.Updated, of.Status == machine.Done && !of.Updated.IsZero()
}

func terminal(it machine.Item) bool {
	t, ok := machine.TableFor(it.Kind)
	return ok && t.Terminal(it.Status)
}
