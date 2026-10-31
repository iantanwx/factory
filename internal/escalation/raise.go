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
	// StateClosed is an escalation whose item reached a terminal state
	// before its panel routed it. No panel can route it, so it waits for
	// none (F-25).
	StateClosed = "closed"
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

// Service serves `escalation raise`, `escalation route` and
// `escalation decisions`.
type Service struct {
	Store    *store.Store
	Machine  *machine.Machine
	Redactor *secrets.Redactor
	// Changed is called after each write that changes what the inbox
	// shows. Optional. The inbox's notifier rings the coordinator from it.
	Changed func()
	Logger  *slog.Logger
}

// Register sets the handlers of raise, route and decisions.
func (s *Service) Register(srv *rpc.Server) error {
	if err := srv.Handle("escalation raise", s.raise); err != nil {
		return err
	}
	if err := srv.Handle("escalation decisions", s.decisions); err != nil {
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

// raise is `loop escalation raise`: a run scores its own finding. A score
// the rubric routes to the loop is refused with ROUTED_TO_LOOP, and a
// triaged note on the item records the reason; the item keeps its state.
// Any other score waits for the fixer's panel, and holds its item so no
// stage takes it first.
func (s *Service) raise(ctx context.Context, call *rpc.Call) cli.Result {
	p, inv := call.Principal, call.Inv
	if p.Item == "" || p.Project == "" {
		return cli.Reject(cli.CodeUsage, "no_item", "a run raises an escalation on the item it holds, and this run holds none")
	}
	tr, err := ParseTriage(inv.Flags["score"], inv.Flags["choice"])
	if err != nil {
		return cli.Reject(cli.CodeUsage, "bad_score", err.Error())
	}
	body := strings.TrimSpace(inv.Flags["body"])
	if body == "" {
		return cli.Reject(cli.CodeUsage, "no_body", "--body holds the finding and the evidence behind the score")
	}
	if r := s.Redactor.Refusal("secret_in_escalation", "escalation", body); r != nil {
		return r.Result()
	}
	if tr.Route() == RouteLoop {
		res := s.Machine.Apply(ctx, machine.Request{Project: p.Project, Item: p.Item, Verb: machine.VerbNote,
			Params: map[string]string{"body": TriagedNote(tr, body)},
			Actor:  machine.Actor{Name: p.Actor(), RunID: p.RunID}})
		if res.Refusal != nil {
			s.log().WarnContext(ctx, "the triaged note was not written", "item", p.Item, "code", res.Refusal.Code)
		}
		// An item that cannot be read has no kind, so the hint names no close.
		it, _ := s.Machine.Read(ctx, p.Project, p.Item)
		return cli.Refuse(cli.Refusal{Code: cli.CodeRoutedToLoop, Rule: "triage_routes_to_loop", Item: p.Item, Hint: RefusalHint(tr, it.Kind)})
	}
	var rec Record
	var ended string
	var allowed []string
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		// The same transaction as the terminal write's close (CloseFor), so
		// either order leaves no escalation on a terminal item (F-25).
		table, status, err := lastStatus(tx, p.Project, p.Item)
		if err != nil {
			return err
		}
		if table != nil && table.Terminal(status) {
			ended, allowed = status, table.AllowedNext(status)
			return nil
		}
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
		return cli.Internal(err)
	}
	if ended != "" {
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "item_terminal", Item: p.Item,
			CurrentStatus: ended, AllowedNext: allowed,
			Hint: fmt.Sprintf("%s is %s, a terminal state, so no panel can route an escalation on it; finish the run", p.Item, ended)})
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
		return cli.Reject(cli.CodeCapabilityDenied, "fixer_only", "only a fixer run routes an escalation; a run raises its own with `loop escalation raise`")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inv.Arg("escalation")), 10, 64)
	if err != nil || id < 1 {
		return cli.Reject(cli.CodeUsage, "bad_escalation", "the escalation id is a positive number; `loop work peek --stage fixer` lists them")
	}
	content, ok := cli.FileContent(inv)
	if !ok {
		return cli.Reject(cli.CodeUsage, "no_file", "--file names the panel file that `loop escalation judge` wrote")
	}
	if r := s.Redactor.Refusal("secret_in_panel_file", "panel file", content); r != nil {
		return r.Result()
	}
	panels, err := ParsePanels(content)
	if err != nil {
		return cli.Reject(cli.CodeUsage, "bad_panel", err.Error())
	}
	rel, res := releaseIn(inv.Flags)
	if res.Refusal != nil {
		return res
	}
	rec, err := s.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return cli.Reject(cli.CodeNotFound, "no_escalation", fmt.Sprintf("no escalation %d", id))
	}
	if err != nil {
		return cli.Internal(err)
	}
	if p.Project != "" && rec.Project != p.Project {
		return cli.Reject(cli.CodeCapabilityDenied, "other_project", "a fixer routes the escalations of its own project only")
	}
	if rec.State == StateClosed {
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "already_routed", Item: rec.Item,
			Hint: fmt.Sprintf("escalation %d is closed: %s reached a terminal state before a panel routed it, so no panel routes it", id, rec.Item)})
	}
	if rec.State != StateAwaitingPanel {
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "already_routed", Item: rec.Item,
			Hint: fmt.Sprintf("escalation %d is already routed: %s", id, rec.State)})
	}
	d := DecideAll(panels, DefaultThresholds)
	body := ""
	if rel != nil && d.Route == RouteLoop {
		rel.Escalation = id
		var res cli.Result
		if body, d, res = s.release(ctx, rec, *rel, d); res.Refusal != nil {
			return res
		}
	}
	released := rel != nil && d.Route == RouteLoop
	panel := map[string]any{"panels": panels, "decision": d}
	if released {
		panel["release"] = rel
	}
	raw, err := json.Marshal(panel)
	if err != nil {
		return cli.Internal(err)
	}
	if d.Route == RouteLoop {
		if body != "" {
			res := s.Machine.Apply(ctx, machine.Request{Project: rec.Project, Item: rec.Item, Verb: "edit",
				Params: map[string]string{"body": body}, Actor: machine.Server("escalation")})
			if res.Refusal != nil {
				return res
			}
		}
		tr, _ := ParseTriage(strconv.Itoa(rec.Level), rec.Choice)
		why := "The fixer's panel routed this escalation back to the loop: " + strings.Join(d.Because, "; ")
		if released {
			why += fmt.Sprintf("\n\nThe fixer released `%s` from `%s` into %q in the item's body: %s",
				rel.Path, rel.Forbids, "May change", rel.Condition)
		}
		note := TriagedNote(tr, why+"\n\nThe finding: "+rec.Finding)
		res := s.Machine.Apply(ctx, machine.Request{Project: rec.Project, Item: rec.Item, Verb: machine.VerbNote,
			Params: map[string]string{"body": note}, Actor: machine.Server("escalation")})
		if res.Refusal != nil {
			s.log().WarnContext(ctx, "the triaged note was not written", "item", rec.Item, "code", res.Refusal.Code)
		}
	} else {
		res := s.Machine.Apply(ctx, machine.Request{Project: rec.Project, Item: rec.Item, Verb: machine.VerbEscalate,
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
		return cli.Internal(err)
	}
	s.changed()
	rec.State, rec.RoutedBy = state, p.Actor()
	out := map[string]any{"escalation": rec, "route": d.Route, "decision": d}
	if released {
		out["released"] = rel.Path
	}
	return cli.Found(out)
}

// releaseIn reads the release flags of `escalation route`: none, or all
// three.
func releaseIn(flags map[string]string) (*Release, cli.Result) {
	rel := Release{Path: strings.TrimSpace(flags["release"]), Forbids: strings.TrimSpace(flags["forbids"]),
		Condition: strings.TrimSpace(flags["condition"])}
	if rel.Path == "" && rel.Forbids == "" && rel.Condition == "" {
		return nil, cli.Result{}
	}
	if rel.Path == "" || rel.Forbids == "" || rel.Condition == "" {
		return nil, cli.Reject(cli.CodeUsage, "incomplete_release",
			"a release names --release, --forbids and --condition together, and one of them is empty")
	}
	// The note and the answer name the path as the body holds it. A path
	// with no clean spelling stays, and the release refuses it.
	if clean := CleanPath(rel.Path); clean != "" {
		rel.Path = clean
	}
	return &rel, cli.Result{}
}

// release edits the item's body for a loop route, and answers the new
// body. A path that is never released sends the escalation to a person
// instead, whatever the judges scored. Any other refusal writes nothing,
// and the escalation still waits for its panel.
func (s *Service) release(ctx context.Context, rec Record, rel Release, d Decision) (string, Decision, cli.Result) {
	old, ref := s.Machine.Body(ctx, rec.Project, rec.Item)
	if ref != nil {
		return "", d, ref.Result()
	}
	body, refusal := ReleasePath(old, rel)
	switch {
	case refusal == nil:
		return body, d, cli.Result{}
	case refusal.Rule == RuleNeverReleased:
		return "", Decision{Route: RoutePerson, Because: []string{"the release reaches a path that is never released: " + refusal.Hint}}, cli.Result{}
	case refusal.Rule == "already_released" && ReleasedBy(old, rel.Path, rec.ID):
		// This route already edited the body, and a crash cut it off before
		// it recorded the route. The edit stands, so finish the route.
		return "", d, cli.Result{}
	}
	return "", d, cli.Reject(cli.CodeUsage, refusal.Rule, "the release writes nothing: "+refusal.Hint)
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

const selectCols = `SELECT id, project, item, run_id, stage, level, choice, finding, state, routed_by, created_at, updated_at FROM escalations`

func scan(row store.Scanner) (Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.Project, &r.Item, &r.RunID, &r.Stage, &r.Level, &r.Choice, &r.Finding, &r.State, &r.RoutedBy,
		&r.CreatedAt, &r.UpdatedAt)
	if err == nil && r.Level >= 1 && r.Level <= len(Levels) {
		r.LevelName = Levels[r.Level-1].Name
	}
	return r, err
}

func query(ctx context.Context, q store.Querier, sqlText string, args ...any) ([]Record, error) {
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

// lastStatus reads the item's status from its last transition, with the
// table of its kind. It answers a nil table for an item with no transition
// or of an unknown kind.
func lastStatus(q Querier, project, item string) (*machine.Table, string, error) {
	var kind, status string
	err := q.QueryRow(`SELECT kind, to_status FROM transitions WHERE project = ? AND item = ? ORDER BY seq DESC LIMIT 1`,
		project, item).Scan(&kind, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	t, ok := machine.TableFor(machine.Kind(kind))
	if !ok {
		return nil, status, nil
	}
	return t, status, nil
}

// CloseFor closes the item's escalations that wait for their panel, and
// drops the item's escalation holds. The machine calls it in the
// transaction that lands the item in a terminal state: no panel can route
// an escalation of a terminal item, so it must not head the fixer's queue
// (F-25).
func CloseFor(tx *store.Tx, ev machine.TerminalEvent) error {
	project, item := ev.Project, ev.Item
	if _, err := tx.Exec(`UPDATE escalations SET state = ?, updated_at = ? WHERE project = ? AND item = ? AND state = ?`,
		StateClosed, tx.Now().UnixMilli(), project, item, StateAwaitingPanel); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM holds WHERE project = ? AND item = ? AND kind = ?`, project, item, HoldKind)
	return err
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
