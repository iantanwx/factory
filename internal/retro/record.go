package retro

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// StateRunning is a retro that one run holds.
const StateRunning = "running"

// Retro is one row of retros.
type Retro struct {
	Project  string       `json:"project"`
	Item     string       `json:"item"`
	Kind     machine.Kind `json:"kind"`
	State    string       `json:"state"`
	RunID    string       `json:"run_id"`
	Attempts int          `json:"attempts"`
}

const retroColumns = `project, item, kind, state, run_id, attempts`

// Bind returns the retro that run holds, or binds the oldest pending one of
// project. ok is false when the project holds none. The read and the bind
// share one write transaction, so two runs never bind one retro, and a run
// that holds a retro never binds a second.
func Bind(ctx context.Context, st *store.Store, project, runID string) (r Retro, ok bool, err error) {
	err = st.Write(ctx, func(tx *store.Tx) error {
		held := func() error {
			r, ok, err = scanRetro(tx.QueryRow(`SELECT `+retroColumns+` FROM retros WHERE run_id = ?`, runID))
			return err
		}
		if err := held(); err != nil || ok {
			return err
		}
		res, err := tx.Exec(`UPDATE retros SET state = ?, run_id = ? WHERE rowid = (
			SELECT rowid FROM retros WHERE project = ? AND state = ? ORDER BY created_at, rowid LIMIT 1)`,
			StateRunning, runID, project, StatePending)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		return held()
	})
	return r, ok, err
}

func scanRetro(row *sql.Row) (Retro, bool, error) {
	var r Retro
	err := row.Scan(&r.Project, &r.Item, &r.Kind, &r.State, &r.RunID, &r.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return Retro{}, false, nil
	}
	return r, err == nil, err
}

// Record is everything a retro run reads about its retro's item: the item,
// and for an epic each of its tickets.
type Record struct {
	Retro    Retro        `json:"retro"`
	Item     ItemRecord   `json:"item"`
	Children []ItemRecord `json:"children,omitempty"` // an epic's tickets
}

// ItemRecord is the history of one item. Each element is a case that a
// learning can cite.
type ItemRecord struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Rounds      []Case `json:"rounds,omitempty"`      // review rounds and their findings
	Runs        []Case `json:"runs,omitempty"`        // every run on the item, with its outcome class and note
	Escalations []Case `json:"escalations,omitempty"` // with each route
	Faults      []Case `json:"faults,omitempty"`      // loop faults whose item is this one
	Comments    []Case `json:"comments,omitempty"`    // comments on the item's pull requests
	Notes       []Case `json:"notes,omitempty"`       // every note, from transitions
	Signals     []Case `json:"signals,omitempty"`
}

// caseLists lists each list of cases of the record's items. Each list
// shares its elements with the record, so a write through it changes the
// record.
func (r Record) caseLists() [][]Case {
	var out [][]Case
	for _, ir := range append([]ItemRecord{r.Item}, r.Children...) {
		out = append(out, ir.Rounds, ir.Runs, ir.Escalations, ir.Faults, ir.Comments, ir.Notes, ir.Signals)
	}
	return out
}

// Case is one element of a record.
type Case struct {
	Ref  CaseRef   `json:"ref"`
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Comment is one comment on a pull request of an item.
type Comment struct {
	ID     int64     `json:"id"`
	Kind   string    `json:"kind"` // review or conversation
	Author string    `json:"author"`
	Body   string    `json:"body"`
	At     time.Time `json:"at"`
}

// CommentReader reads the comments on every pull request of one item. The
// GitHub setup sets it. Its refusal is the verb's answer, so a GitHub
// outage answers WAIT.
type CommentReader func(ctx context.Context, project string, it machine.Item) ([]Comment, *cli.Refusal)

// World is what a record reads: the store, the machine's items and
// history, and the comments on GitHub.
type World struct {
	Store   *store.Store
	Machine *machine.Machine
	// Comments is nil on a server with no GitHub, and the record then
	// holds no comments.
	Comments CommentReader
	// Faults files the loop fault of a landed loop_fault learning.
	Faults *fault.Service
}

// BuildRecord reads the record of r's item and, for an epic, of each of
// its tickets.
func BuildRecord(ctx context.Context, w World, r Retro) (Record, *cli.Refusal) {
	it, ref := w.Machine.Read(ctx, r.Project, r.Item)
	if ref != nil {
		return Record{}, ref
	}
	rec := Record{Retro: r}
	if rec.Item, ref = w.itemRecord(ctx, it); ref != nil {
		return Record{}, ref
	}
	if it.Kind != machine.KindEpic {
		return rec, nil
	}
	children, ref := w.Machine.List(ctx, r.Project, machine.ListFilter{Kind: machine.KindTicket, Facts: map[string]string{machine.FactEpic: it.ID}})
	if ref != nil {
		return Record{}, ref
	}
	for _, c := range children {
		ir, ref := w.itemRecord(ctx, c)
		if ref != nil {
			return Record{}, ref
		}
		rec.Children = append(rec.Children, ir)
	}
	return rec, nil
}

func (w World) itemRecord(ctx context.Context, it machine.Item) (ItemRecord, *cli.Refusal) {
	ir := ItemRecord{ID: it.ID, Kind: string(it.Kind), Title: it.Title, Status: it.Status}
	hist, err := w.Machine.History(ctx, it.Project, it.ID)
	if err != nil {
		return ItemRecord{}, cli.InternalRefusal(err)
	}
	for _, rd := range machine.Rounds(hist) {
		text := rd.Verdict
		if rd.Head != "" {
			text += " at " + rd.Head
		}
		if rd.Body != "" {
			text += ": " + rd.Body
		}
		ir.Rounds = append(ir.Rounds, Case{Ref: CaseRef(fmt.Sprintf("round:%s:%d", it.ID, rd.Round)), At: rd.At, Text: text})
	}
	for _, t := range hist {
		if n := t.Fact(machine.FactNote); n != "" {
			ir.Notes = append(ir.Notes, Case{Ref: CaseRef(fmt.Sprintf("transition:%d", t.IntentID)), At: t.At, Text: t.Actor + ": " + n})
		}
	}
	if err := w.Store.Read(ctx, func(tx *sql.Tx) error { return storeCases(ctx, tx, it, &ir) }); err != nil {
		return ItemRecord{}, cli.InternalRefusal(err)
	}
	if w.Comments != nil {
		cs, ref := w.Comments(ctx, it.Project, it)
		if ref != nil {
			return ItemRecord{}, ref
		}
		for _, c := range cs {
			ir.Comments = append(ir.Comments, Case{Ref: CaseRef(fmt.Sprintf("comment:%s:%d", it.ID, c.ID)), At: c.At,
				Text: c.Author + ", " + c.Kind + ": " + c.Body})
		}
	}
	return ir, nil
}

// storeCases reads the runs, escalations, loop faults and signals of one
// item, oldest first.
func storeCases(ctx context.Context, tx *sql.Tx, it machine.Item, ir *ItemRecord) error {
	err := eachRow(ctx, tx, `SELECT r.run_id, r.stage, r.state, coalesce(r.outcome_class, ''), coalesce(f.note, ''), r.started_at
		FROM runs r LEFT JOIN failures f ON f.run_id = r.run_id WHERE r.project = ? AND r.item = ? ORDER BY r.started_at, r.run_id`,
		[]any{it.Project, it.ID}, func(rows *sql.Rows) error {
			var (
				id, stage, state, class, note string
				at                            int64
			)
			if err := rows.Scan(&id, &stage, &state, &class, &note, &at); err != nil {
				return err
			}
			text := stage + " run " + state
			if class != "" {
				text += ", outcome " + class
			}
			if note != "" {
				text += ": " + note
			}
			ir.Runs = append(ir.Runs, Case{Ref: CaseRef("run:" + id), At: time.UnixMilli(at), Text: text})
			return nil
		})
	if err != nil {
		return err
	}
	err = eachRow(ctx, tx, `SELECT id, stage, level, choice, finding, state, created_at FROM escalations
		WHERE project = ? AND item = ? ORDER BY id`, []any{it.Project, it.ID}, func(rows *sql.Rows) error {
		var (
			id, at                        int64
			level                         int
			stage, choice, finding, state string
		)
		if err := rows.Scan(&id, &stage, &level, &choice, &finding, &state, &at); err != nil {
			return err
		}
		ir.Escalations = append(ir.Escalations, Case{Ref: CaseRef(fmt.Sprintf("escalation:%d", id)), At: time.UnixMilli(at),
			Text: fmt.Sprintf("level %d, %s, from %s, route %s: %s", level, choice, stage, state, finding)})
		return nil
	})
	if err != nil {
		return err
	}
	err = eachRow(ctx, tx, `SELECT n, text, stage, status, reported_at FROM faults WHERE project = ? AND item = ? ORDER BY n`,
		[]any{it.Project, it.ID}, func(rows *sql.Rows) error {
			var (
				n, at               int64
				text, stage, status string
			)
			if err := rows.Scan(&n, &text, &stage, &status, &at); err != nil {
				return err
			}
			ir.Faults = append(ir.Faults, Case{Ref: CaseRef(fmt.Sprintf("fault:F-%d", n)), At: time.UnixMilli(at),
				Text: status + ", from " + stage + ": " + text})
			return nil
		})
	if err != nil {
		return err
	}
	signals, err := readSignals(ctx, tx, it.Project, it.ID)
	if err != nil {
		return err
	}
	for _, s := range signals {
		kind := string(s.Kind)
		if s.Scope != "" {
			kind += " " + s.Scope
		}
		cases := make([]string, len(s.Cases))
		for i, c := range s.Cases {
			cases[i] = string(c)
		}
		ir.Signals = append(ir.Signals, Case{Ref: CaseRef(fmt.Sprintf("signal:%d", s.ID)), At: s.At,
			Text: kind + ": " + strings.Join(cases, ", ")})
	}
	return nil
}

// eachRow runs fn on each row of query.
func eachRow(ctx context.Context, tx *sql.Tx, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
