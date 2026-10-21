package escalation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// Escalation states in the escalations table.
const (
	StateAwaitingPanel = "awaiting_panel"
	StateLoop          = "loop"
	StatePerson        = "person"
	StateAnswered      = "answered"
)

// FixerStage is the stage that routes escalations.
const FixerStage = "fixer"

// HoldKind is the holds row kind an escalation that waits for its panel
// takes.
const HoldKind = "escalation"

// Record is one escalation as the store holds it.
type Record struct {
	ID        int64  `json:"escalation"`
	Project   string `json:"project"`
	Item      string `json:"item"`
	RunID     string `json:"run_id"`
	Stage     string `json:"stage"`
	Level     int    `json:"level"`
	LevelName string `json:"level_name"`
	Choice    string `json:"choice"`
	Finding   string `json:"finding"`
	State     string `json:"state"`
	RoutedBy  string `json:"routed_by,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// HolderOf names the holds row of escalation id.
func HolderOf(id int64) string { return "escalation:" + strconv.FormatInt(id, 10) }

// Service serves `escalation raise` and `escalation route`.
type Service struct {
	Store    *store.Store
	Machine  *machine.Machine
	Redactor *secrets.Redactor
	// Changed is called after each write that changes what the inbox
	// shows. Optional. The inbox's notifier rings the coordinator from it.
	Changed func()
	Logger  *slog.Logger
}

// Register sets the handlers of raise and route.
func (s *Service) Register(srv *rpc.Server) error {
	if err := srv.Handle("escalation raise", s.raise); err != nil {
		return err
	}
	return srv.Handle("escalation route", s.route)
}

func (s *Service) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

func (s *Service) log() *slog.Logger {
	if s.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Logger
}

func refuse(code, rule, hint string) cli.Result {
	return cli.Refuse(cli.Refusal{Code: code, Rule: rule, Hint: hint})
}

// secretRefusal refuses text that holds a stored secret. The hint names the
// secret and never its value.
func secretRefusal(red *secrets.Redactor, where string, texts ...string) *cli.Refusal {
	if red == nil {
		return nil
	}
	var names []string
	for _, t := range texts {
		if hit, n := red.Find(t); hit {
			names = append(names, n...)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeSecretInOutput, Rule: "secret_in_" + where,
		Hint: fmt.Sprintf("the %s holds the value of %s; remove it and write again", strings.ReplaceAll(where, "_", " "), strings.Join(names, ", "))}
}

// raise is `loop escalation raise`: a run scores its own finding. A score
// the rubric routes to the loop is refused with ROUTED_TO_LOOP, and a
// triaged note on the item records the reason; the item keeps its state.
// Any other score waits for the fixer's panel, and holds its item so no
// stage takes it first.
func (s *Service) raise(ctx context.Context, call *rpc.Call) cli.Result {
	p, inv := call.Principal, call.Inv
	if p.Item == "" || p.Project == "" {
		return refuse(cli.CodeUsage, "no_item", "a run raises an escalation on the item it holds, and this run holds none")
	}
	tr, err := ParseTriage(inv.Flags["score"], inv.Flags["choice"])
	if err != nil {
		return refuse(cli.CodeUsage, "bad_score", err.Error())
	}
	body := strings.TrimSpace(inv.Flags["body"])
	if body == "" {
		return refuse(cli.CodeUsage, "no_body", "--body holds the finding and the evidence behind the score")
	}
	if r := secretRefusal(s.Redactor, "escalation", body); r != nil {
		return cli.Refuse(*r)
	}
	if tr.Route() == RouteLoop {
		res := s.Machine.Apply(ctx, machine.Request{Project: p.Project, Item: p.Item, Verb: "note",
			Params: map[string]string{"body": TriagedNote(tr, body)},
			Actor:  machine.Actor{Name: p.Actor(), RunID: p.RunID}})
		if res.Refusal != nil {
			s.log().WarnContext(ctx, "the triaged note was not written", "item", p.Item, "code", res.Refusal.Code)
		}
		return cli.Refuse(cli.Refusal{Code: cli.CodeRoutedToLoop, Rule: "triage_routes_to_loop", Item: p.Item, Hint: RefusalHint(tr)})
	}
	var rec Record
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		prior, err := openFor(tx, p.Project, p.Item)
		if err != nil {
			return err
		}
		if prior != nil {
			rec = *prior
			return nil
		}
		now := tx.Now().UnixMilli()
		res, err := tx.Exec(`INSERT INTO escalations (project, item, run_id, stage, level, choice, finding, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Project, p.Item, p.RunID, p.Stage, tr.Level.Ordinal, tr.Choice, body, StateAwaitingPanel, now, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO holds (project, item, holder, kind, created_at) VALUES (?, ?, ?, ?, ?)`,
			p.Project, p.Item, HolderOf(id), HoldKind, now); err != nil {
			return err
		}
		rec = Record{ID: id, Project: p.Project, Item: p.Item, RunID: p.RunID, Stage: p.Stage, Level: tr.Level.Ordinal,
			LevelName: tr.Level.Name, Choice: tr.Choice, Finding: body, State: StateAwaitingPanel, CreatedAt: now, UpdatedAt: now}
		return nil
	})
	if err != nil {
		return refuse(cli.CodeInternal, "internal", err.Error())
	}
	s.changed()
	return cli.Found(map[string]any{"escalation": rec,
		"next": "the fixer's panel routes it; no stage takes the item until then, and your lease is untouched"})
}

// route is `loop escalation route`: the fixer submits the panel file that
// `escalation judge` wrote. The server routes the panel again with its own
// thresholds, so the route is never a value the fixer typed.
func (s *Service) route(ctx context.Context, call *rpc.Call) cli.Result {
	p, inv := call.Principal, call.Inv
	// The surface's "fixer" label admits every run, so the stage narrows
	// it here: only the fixer's panel routes.
	if p.Stage != FixerStage {
		return refuse(cli.CodeCapabilityDenied, "fixer_only", "only a fixer run routes an escalation; a run raises its own with `loop escalation raise`")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inv.Arg("escalation")), 10, 64)
	if err != nil || id < 1 {
		return refuse(cli.CodeUsage, "bad_escalation", "the escalation id is a positive number; `loop work peek --stage fixer` lists them")
	}
	content, ok := cli.FileContent(inv)
	if !ok {
		return refuse(cli.CodeUsage, "no_file", "--file names the panel file that `loop escalation judge` wrote")
	}
	if r := secretRefusal(s.Redactor, "panel_file", content); r != nil {
		return cli.Refuse(*r)
	}
	panels, err := ParsePanels(content)
	if err != nil {
		return refuse(cli.CodeUsage, "bad_panel", err.Error())
	}
	rec, err := s.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return refuse(cli.CodeNotFound, "no_escalation", fmt.Sprintf("no escalation %d", id))
	}
	if err != nil {
		return refuse(cli.CodeInternal, "internal", err.Error())
	}
	if p.Project != "" && rec.Project != p.Project {
		return refuse(cli.CodeCapabilityDenied, "other_project", "a fixer routes the escalations of its own project only")
	}
	if rec.State != StateAwaitingPanel {
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "already_routed", Item: rec.Item,
			Hint: fmt.Sprintf("escalation %d is already routed: %s", id, rec.State)})
	}
	d := DecideAll(panels, DefaultThresholds)
	raw, err := json.Marshal(map[string]any{"panels": panels, "decision": d})
	if err != nil {
		return refuse(cli.CodeInternal, "internal", err.Error())
	}
	if d.Route == RouteLoop {
		tr, _ := ParseTriage(strconv.Itoa(rec.Level), rec.Choice)
		note := TriagedNote(tr, "The fixer's panel routed this escalation back to the loop: "+strings.Join(d.Because, "; ")+
			"\n\nThe finding: "+rec.Finding)
		res := s.Machine.Apply(ctx, machine.Request{Project: rec.Project, Item: rec.Item, Verb: "note",
			Params: map[string]string{"body": note}, Actor: machine.Server("escalation")})
		if res.Refusal != nil {
			s.log().WarnContext(ctx, "the triaged note was not written", "item", rec.Item, "code", res.Refusal.Code)
		}
	} else {
		res := s.Machine.Apply(ctx, machine.Request{Project: rec.Project, Item: rec.Item, Verb: "escalate",
			Params: map[string]string{"escalation": strconv.FormatInt(id, 10)}, Actor: machine.Server("escalation")})
		if res.Refusal != nil {
			return res
		}
	}
	state := StateLoop
	if d.Route == RoutePerson {
		state = StatePerson
	}
	if err := s.Store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(`UPDATE escalations SET state = ?, panel = ?, routed_by = ?, updated_at = ? WHERE id = ?`,
			state, string(raw), p.Actor(), tx.Now().UnixMilli(), id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM holds WHERE project = ? AND item = ? AND holder = ?`, rec.Project, rec.Item, HolderOf(id))
		return err
	}); err != nil {
		return refuse(cli.CodeInternal, "internal", err.Error())
	}
	s.changed()
	rec.State, rec.RoutedBy = state, p.Actor()
	return cli.Found(map[string]any{"escalation": rec, "route": d.Route, "decision": d})
}

// Get reads one escalation.
func (s *Service) Get(ctx context.Context, id int64) (Record, error) {
	var rec Record
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		r, err := scan(tx.QueryRowContext(ctx, selectCols+` WHERE id = ?`, id))
		rec = r
		return err
	})
	return rec, err
}

// Pending lists a project's escalations that wait for the fixer's panel,
// oldest first. The fixer reads them through `loop work peek --stage fixer`.
func (s *Service) Pending(ctx context.Context, project string) ([]Record, error) {
	var out []Record
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = query(ctx, tx, selectCols+` WHERE project = ? AND state = ? ORDER BY id`, project, StateAwaitingPanel)
		return err
	})
	return out, err
}

// Querier is a read handle: a *sql.Tx or a *store.Tx.
type Querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

type ctxQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const selectCols = `SELECT id, project, item, run_id, stage, level, choice, finding, state, routed_by, created_at, updated_at FROM escalations`

func scan(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.Project, &r.Item, &r.RunID, &r.Stage, &r.Level, &r.Choice, &r.Finding, &r.State, &r.RoutedBy,
		&r.CreatedAt, &r.UpdatedAt)
	if err == nil && r.Level >= 1 && r.Level <= len(Levels) {
		r.LevelName = Levels[r.Level-1].Name
	}
	return r, err
}

func query(ctx context.Context, q ctxQuerier, sqlText string, args ...any) ([]Record, error) {
	rs, err := q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	out := []Record{}
	for rs.Next() {
		r, err := scan(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// openFor returns the item's escalation that waits for its panel, or nil.
func openFor(q Querier, project, item string) (*Record, error) {
	r, err := scan(q.QueryRow(selectCols+` WHERE project = ? AND item = ? AND state = ? ORDER BY id LIMIT 1`,
		project, item, StateAwaitingPanel))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Latest returns the item's last escalation in state, or nil. The inbox
// shows its finding beside the escalated item.
func Latest(ctx context.Context, tx *sql.Tx, project, item, state string) (*Record, error) {
	r, err := scan(tx.QueryRowContext(ctx, selectCols+` WHERE project = ? AND item = ? AND state = ? ORDER BY id DESC LIMIT 1`,
		project, item, state))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// MarkAnswered records that a person answered the item's escalation.
func MarkAnswered(tx *store.Tx, project, item string) error {
	_, err := tx.Exec(`UPDATE escalations SET state = ?, updated_at = ? WHERE project = ? AND item = ? AND state = ?`,
		StateAnswered, tx.Now().UnixMilli(), project, item, StatePerson)
	return err
}
