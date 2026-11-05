package retro

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/store"
)

// LoopRepo is coord.LoopRepo, the repository of the loop's own project.
const LoopRepo = coord.LoopRepo

// StateDone is a retro whose learnings all have their routes.
const StateDone = "done"

// noticeSource is the source of a retro's notice.
const noticeSource = "retro"

// EntryKind is the inbox kind of an escalated learning, inbox.KindLearning.
const EntryKind = "learning"

// EntryOf is the inbox entry id of the escalated learning id.
func EntryOf(id int64) string { return EntryKind + "/" + strconv.FormatInt(id, 10) }

// learningID is the id a person reads, L-<n>.
func learningID(n int64) string { return "L-" + strconv.FormatInt(n, 10) }

func parseLearningID(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "L-"), 10, 64)
	return n, strings.HasPrefix(s, "L-") && err == nil && n > 0
}

// Stored is one row of learnings, as `loop retro held` and work show print
// it.
type Stored struct {
	ID        string         `json:"id"`
	RetroItem string         `json:"retro_item"`
	Statement string         `json:"statement"`
	Cases     []CaseRef      `json:"cases"`
	Extends   string         `json:"extends,omitempty"`
	Target    Target         `json:"target,omitempty"`
	Status    LearningStatus `json:"status"`
	Reason    Reason         `json:"reason,omitempty"`
	LandedRef string         `json:"landed_ref,omitempty"`
}

const storedColumns = `id, retro_item, statement, cases, coalesce(extends, 0), target, status, reason, landed_ref`

func scanStored(scan func(...any) error) (Stored, error) {
	var (
		s       Stored
		id, ext int64
		cases   string
	)
	if err := scan(&id, &s.RetroItem, &s.Statement, &cases, &ext, &s.Target, &s.Status, &s.Reason, &s.LandedRef); err != nil {
		return Stored{}, err
	}
	s.ID = learningID(id)
	if ext > 0 {
		s.Extends = learningID(ext)
	}
	return s, json.Unmarshal([]byte(cases), &s.Cases)
}

// readStored reads the learnings that where matches, oldest first.
func readStored(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]Stored, error) {
	var out []Stored
	err := eachRow(ctx, tx, `SELECT `+storedColumns+` FROM learnings WHERE `+where+` ORDER BY id`, args, func(rows *sql.Rows) error {
		s, err := scanStored(rows.Scan)
		out = append(out, s)
		return err
	})
	return out, err
}

// Held lists the held learnings of project, oldest first.
func Held(ctx context.Context, st *store.Store, project string) ([]Stored, error) {
	var out []Stored
	err := st.Read(ctx, func(tx *sql.Tx) (err error) {
		out, err = readStored(ctx, tx, `project = ? AND status = ?`, project, StatusHeld)
		return err
	})
	return out, err
}

// Escalation is an escalated learning, as its inbox entry reads it.
type Escalation struct {
	Stored
	Project string
	Panel   Panel
	RunID   string // the retro run that sent it
	At      int64  // when the route stored it, in Unix milliseconds
}

// Escalations reads every learning that waits for the operator, oldest
// first.
func Escalations(ctx context.Context, tx *sql.Tx) ([]Escalation, error) {
	var out []Escalation
	err := eachRow(ctx, tx, `SELECT l.id, l.retro_item, l.statement, l.cases, coalesce(l.extends, 0), l.target, l.status, l.reason, l.landed_ref,
		l.project, l.panel, coalesce(r.run_id, ''), l.created_at
		FROM learnings l LEFT JOIN retros r ON r.project = l.project AND r.item = l.retro_item
		WHERE l.status = ? ORDER BY l.id`, []any{StatusEscalated}, func(rows *sql.Rows) error {
		var (
			e     Escalation
			panel string
		)
		s, err := scanStored(func(dest ...any) error { return rows.Scan(append(dest, &e.Project, &panel, &e.RunID, &e.At)...) })
		if err != nil {
			return err
		}
		e.Stored = s
		if err := json.Unmarshal([]byte(panel), &e.Panel); err != nil {
			return err
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

// Settle records the operator's answer to the escalated learning id. It
// reports false when no learning id waits for one.
func Settle(tx *store.Tx, id int64, body string) (bool, error) {
	r, err := tx.Exec(`UPDATE learnings SET status = ?, answer = ? WHERE id = ? AND status = ?`, StatusAnswered, body, id, StatusEscalated)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// Routed is the route of one learning.
type Routed struct {
	ID     string `json:"id"`
	Route  string `json:"route"` // land, hold or escalate
	Target Target `json:"target,omitempty"`
	Reason Reason `json:"reason,omitempty"`
	Entry  string `json:"entry,omitempty"` // the inbox entry of an escalated learning
	// Ref is the ticket, bug or fault a landed learning made.
	Ref string `json:"landed_ref,omitempty"`
}

// Result is the answer of `loop retro route`: each learning's route, and
// the retro's notice.
type Result struct {
	Retro     Retro    `json:"retro"`
	Learnings []Routed `json:"learnings"`
	Notice    string   `json:"notice"`
}

// errRefused rolls back a route that a refusal ended.
var errRefused = errors.New("retro: the route was refused")

// RouteAll checks every case, decides each panel, stores its learning and
// marks the retro done, in one transaction. A refusal writes nothing. Then
// it lands each landed learning and puts the one notice. A done retro with
// no notice is a route that stopped before its notice, and a repeat by the
// run that holds it finishes that route.
func RouteAll(ctx context.Context, w World, runID string, panels []Panel) (Result, *cli.Refusal) {
	var (
		r  Retro
		ok bool
	)
	const bound = `SELECT ` + retroColumns + ` FROM retros WHERE run_id = ?`
	err := w.Store.Read(ctx, func(tx *sql.Tx) (err error) {
		r, ok, err = scanRetro(tx.QueryRowContext(ctx, bound, runID))
		return err
	})
	if err != nil {
		return Result{}, cli.InternalRefusal(err)
	}
	if ok && r.State == StateDone {
		var n int
		err := w.Store.Read(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT count(*) FROM notices WHERE source = ? AND ref = ?`, noticeSource, r.Project+"/"+r.Item).Scan(&n)
		})
		if err != nil {
			return Result{}, cli.InternalRefusal(err)
		}
		if n == 0 {
			return w.finish(ctx, r)
		}
	}
	if ref := routable(r, ok); ref != nil {
		return Result{}, ref
	}
	rec, ref := BuildRecord(ctx, w, r)
	if ref != nil {
		return Result{}, ref
	}
	record := map[CaseRef]bool{}
	for _, cs := range rec.caseLists() {
		for _, c := range cs {
			record[c.Ref] = true
		}
	}
	err = w.Store.Write(ctx, func(tx *store.Tx) error {
		// A second route can start while the first writes, so the state
		// is read again where it changes.
		r, ok, err = scanRetro(tx.QueryRow(bound, runID))
		if err != nil {
			return err
		}
		if ref = routable(r, ok); ref != nil {
			return errRefused
		}
		c := RouteContext{Thresholds: escalation.DefaultThresholds}
		if c.LoopProject, err = loopProject(tx, r.Project); err != nil {
			return err
		}
		for _, p := range panels {
			refused, err := routeOne(tx, r, p, c, record)
			if err != nil {
				return err
			}
			if refused != nil {
				ref = refused
				return errRefused
			}
		}
		_, err = tx.Exec(`UPDATE retros SET state = ?, finished_at = ? WHERE project = ? AND item = ?`,
			StateDone, tx.Now().UnixMilli(), r.Project, r.Item)
		return err
	})
	if ref != nil {
		return Result{}, ref
	}
	if err != nil {
		return Result{}, cli.InternalRefusal(err)
	}
	return w.finish(ctx, r)
}

// finish lands each landed learning of the done retro r that holds no
// landed_ref, records the id it made, and puts the notice last.
func (w World) finish(ctx context.Context, r Retro) (Result, *cli.Refusal) {
	var ls []Stored
	err := w.Store.Read(ctx, func(tx *sql.Tx) (err error) {
		ls, err = readStored(ctx, tx, `project = ? AND retro_item = ?`, r.Project, r.Item)
		return err
	})
	if err != nil {
		return Result{}, cli.InternalRefusal(err)
	}
	r.State = StateDone
	res := Result{Retro: r, Learnings: []Routed{}}
	for _, l := range ls {
		n, _ := parseLearningID(l.ID)
		if l.Status == StatusLanded && l.LandedRef == "" {
			id, ref := w.Land(ctx, r, l)
			if ref != nil {
				return Result{}, ref
			}
			if err := w.Store.Write(ctx, func(tx *store.Tx) error {
				_, err := tx.Exec(`UPDATE learnings SET landed_ref = ? WHERE id = ?`, id, n)
				return err
			}); err != nil {
				return Result{}, cli.InternalRefusal(err)
			}
			l.LandedRef = id
		}
		rt := Routed{ID: l.ID, Route: "hold", Target: l.Target, Reason: l.Reason, Ref: l.LandedRef}
		switch l.Status.Route(l.Target, l.Reason).(type) {
		case Land:
			rt.Route = "land"
		case Escalate:
			rt.Route, rt.Entry = "escalate", EntryOf(n)
		}
		res.Learnings = append(res.Learnings, rt)
	}
	err = w.Store.Write(ctx, func(tx *store.Tx) error {
		id, err := putNotice(tx, r, res.Learnings)
		// The entry id of a notice, as the inbox prints it.
		res.Notice = "notice/" + strconv.FormatInt(id, 10)
		return err
	})
	if err != nil {
		return Result{}, cli.InternalRefusal(err)
	}
	return res, nil
}

// routable refuses a route of the retro that a run holds, when it holds
// none or the retro has its route.
func routable(r Retro, ok bool) *cli.Refusal {
	switch {
	case ok && r.State == StateDone:
		return &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "retro_done", Item: r.Item,
			Hint: fmt.Sprintf("the retro of %s is done; a retro takes one route", r.Item)}
	case !ok || r.State != StateRunning:
		return &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "retro_not_bound",
			Hint: "this run holds no retro; `loop retro record` binds the oldest pending retro of the project"}
	}
	return nil
}

// loopProject reports whether project is the loop project, by
// coord.LoopProject over every registered project.
func loopProject(tx *store.Tx, project string) (bool, error) {
	rows, err := tx.Query(`SELECT id, repo FROM projects`)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	var ps []config.Project
	for rows.Next() {
		var p config.Project
		if err := rows.Scan(&p.ID, &p.Repo); err != nil {
			return false, err
		}
		ps = append(ps, p)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	id, ok := coord.LoopProject(ps)
	return ok && id == project, nil
}

// routeOne checks the cases of one panel, decides its route and stores its
// learning. A learning that extends a held one takes its cases, and the
// held one moves to counted.
func routeOne(tx *store.Tx, r Retro, p Panel, c RouteContext, record map[CaseRef]bool) (*cli.Refusal, error) {
	l := p.Learning
	var (
		prior   []CaseRef
		extends sql.NullInt64
	)
	if l.Extends != "" {
		n, ok := parseLearningID(l.Extends)
		var held Stored
		var err error
		if ok {
			held, err = scanStored(tx.QueryRow(`SELECT `+storedColumns+` FROM learnings WHERE id = ? AND project = ? AND status = ?`,
				n, r.Project, StatusHeld).Scan)
		}
		if !ok || errors.Is(err, sql.ErrNoRows) {
			return &cli.Refusal{Code: cli.CodeUsage, Rule: "extends_not_held",
				Hint: fmt.Sprintf("the learning %q extends %s, which is no held learning of %s; `loop retro held` lists them", l.Statement, l.Extends, r.Project)}, nil
		}
		if err != nil {
			return nil, err
		}
		prior, extends = held.Cases, sql.NullInt64{Int64: n, Valid: true}
	}
	for _, cs := range l.Cases {
		if !record[cs] && !slices.Contains(prior, cs) {
			return &cli.Refusal{Code: cli.CodeUsage, Rule: "case_not_in_record", Item: r.Item,
				Hint: fmt.Sprintf("the learning %q names case %s, which is neither in the record of %s nor in the learning it extends; `loop retro record` prints the record",
					l.Statement, cs, r.Item)}, nil
		}
	}
	cases := slices.Clone(prior)
	for _, cs := range l.Cases {
		if !slices.Contains(cases, cs) {
			cases = append(cases, cs)
		}
	}
	if extends.Valid {
		if _, err := tx.Exec(`UPDATE learnings SET status = ? WHERE id = ?`, StatusCounted, extends.Int64); err != nil {
			return nil, err
		}
	}
	// A land route's target is the one every judge named.
	status, reason := StatusHeld, Reason("")
	switch rt := Decide(p, c).(type) {
	case Land:
		status = StatusLanded
	case Escalate:
		status, reason = StatusEscalated, rt.Reason
	}
	rawCases, err := json.Marshal(cases)
	if err != nil {
		return nil, err
	}
	panel, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO learnings (project, retro_item, statement, cases, extends, panel, reason, target, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.Project, r.Item, l.Statement, string(rawCases), extends, string(panel),
		reason, sharedTarget(p.Answers), status, tx.Now().UnixMilli())
	return nil, err
}

// sharedTarget is the one target every judge named, or empty.
func sharedTarget(as []Answer) Target {
	if len(as) == 0 || slices.ContainsFunc(as, func(a Answer) bool { return a.Target != as[0].Target }) {
		return ""
	}
	return as[0].Target
}

// putNotice puts the one notice of r, keyed retro and <project>/<item>, and
// returns its row. It is inbox.PutNotice's insert: retro cannot import
// inbox, which reaches retro through sched. The notice asks nothing, so
// it names each learning by its id and holds no learning's text.
func putNotice(tx *store.Tx, r Retro, routed []Routed) (int64, error) {
	ref := r.Project + "/" + r.Item
	subject := fmt.Sprintf("%s: the retro of %s routed %d learnings", r.Project, r.Item, len(routed))
	body := fmt.Sprintf("The retro of %s found no learning.", r.Item)
	if len(routed) > 0 {
		var landed, held, escalated []string
		for _, rt := range routed {
			switch rt.Route {
			case "land":
				landed = append(landed, fmt.Sprintf("- %s at %s: %s", rt.ID, rt.Target, rt.Ref))
			case "escalate":
				escalated = append(escalated, fmt.Sprintf("- %s, reason %s, entry %s", rt.ID, rt.Reason, rt.Entry))
			default:
				held = append(held, "- "+rt.ID)
			}
		}
		parts := []string{fmt.Sprintf("The retro of %s routed %d learnings.", r.Item, len(routed))}
		for _, sec := range []struct {
			head  string
			lines []string
		}{{"Landed:", landed}, {"Held for a second case:", held}, {"Escalated to the operator:", escalated}} {
			if len(sec.lines) > 0 {
				parts = append(parts, sec.head+"\n"+strings.Join(sec.lines, "\n"))
			}
		}
		body = strings.Join(parts, "\n\n")
	}
	if _, err := tx.Exec(`INSERT INTO notices (project, source, ref, subject, body, at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, ref) DO NOTHING`, r.Project, noticeSource, ref, subject, body, tx.Now().UnixMilli()); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRow(`SELECT id FROM notices WHERE source = ? AND ref = ?`, noticeSource, ref).Scan(&id)
	return id, err
}
