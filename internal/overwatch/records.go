package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
	names map[string]string // token -> record name, such as "factorio-x1y", "F-3" or "question 7"
}

// Names returns the record that holds key as a token.
func (r Records) Names(key string) (record string, ok bool) {
	record, ok = r.names[key]
	return record, ok
}

// add files each token of text under record. The first record to hold a
// token keeps it.
func (r *Records) add(record, text string, keep func(string) bool) {
	if r.names == nil {
		r.names = map[string]string{}
	}
	for _, t := range Tokens(text) {
		if _, ok := r.names[t]; !ok && (keep == nil || keep(t)) {
			r.names[t] = record
		}
	}
}

// ReadRecords reads every open record once (spec, Approach, "Keys and
// records"):
//   - a bug or a decision in any project in a state that is not terminal
//     for its kind, and a bug that `work shape` closed while its ticket is
//     not terminal: its body, and the title of a decision;
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
			if !openRecord(it, status) {
				continue
			}
			body, err := w.Body(ctx, p.ID, it.ID)
			if err != nil {
				return r, fmt.Errorf("read the body of %s: %w", it.ID, err)
			}
			if it.Kind == machine.KindDecision {
				body = it.Title + "\n" + body
			}
			r.add(it.ID, body, nil)
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
			r.add(fmt.Sprintf("question %d", id), body, nil)
		}
		if err := rs.Err(); err != nil {
			return err
		}
		faults, err := fault.Pending(ctx, tx)
		if err != nil {
			return err
		}
		for _, f := range faults {
			r.add(f.ID, f.Text, func(t string) bool { return strings.HasPrefix(t, "overwatch:") })
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

func terminal(it machine.Item) bool {
	t, ok := machine.TableFor(it.Kind)
	return ok && t.Terminal(it.Status)
}
