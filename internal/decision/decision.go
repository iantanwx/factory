// Package decision files decisions and runs the decision-timers native job.
// A decision waits in pending for its trigger. `decision fire`, a
// --by date, the end of an inbox defer or `decision file --now` sends it to
// a person in needs_human. The other decision verbs are the state machine's: the
// table ports DECISION_MOVES (scripts/sdlc/decision-states.ts, alpha).
package decision

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// JobName is the native job of the timers in host config's throughput.
const JobName = "decision-timers"

// ByFiredKind is the schedule row that records a --by date fired, so a
// decision a person deferred by hand does not fire again on the same date.
const ByFiredKind = "decision_by"

// dateLayout is the --by date.
const dateLayout = "2006-01-02"

// Service serves `decision file` and the decision-timers job.
type Service struct {
	Store    *store.Store
	Machine  *machine.Machine
	Redactor *secrets.Redactor
	// Now is the timers' clock. time.Now when nil.
	Now func() time.Time
	// Changed is called after a write that changes the inbox. Optional.
	Changed func()
	Logger  *slog.Logger
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Service) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

// Register sets the handler of `decision file`.
func (s *Service) Register(srv *rpc.Server) error {
	return srv.Handle("decision file", s.file)
}

// file is `loop decision file`: a question that waits for its trigger. A
// run files in its own project, and the coordinator names one. --now says
// the trigger already fired, so the same write fires the decision to a
// person: a run may not fire a decision it does not hold, and a spec defect
// a code-reviewer finds must page a person now (code-reviewer.md:1098-1130,
// alpha, escalates the epic).
func (s *Service) file(ctx context.Context, call *rpc.Call) cli.Result {
	p, inv := call.Principal, call.Inv
	project, ref := call.Project("a run files decisions in its own project only")
	if ref != nil {
		return ref.Result()
	}
	title, trigger := strings.TrimSpace(inv.Flags["title"]), strings.TrimSpace(inv.Flags["trigger"])
	if title == "" || trigger == "" {
		return cli.Reject(cli.CodeUsage, "no_trigger", "--title holds the question and --trigger the event that forces the answer")
	}
	by := strings.TrimSpace(inv.Flags["by"])
	if by != "" {
		if _, err := time.Parse(dateLayout, by); err != nil {
			return cli.Reject(cli.CodeUsage, "bad_by", "--by is a date, YYYY-MM-DD")
		}
	}
	now := inv.Flags["now"] == "true"
	if now && by != "" {
		return cli.Reject(cli.CodeUsage, "now_and_by", "--now says the trigger fired, and --by names a day it fires; pass one")
	}
	body := inv.Flags["body"]
	if s.Redactor != nil {
		for _, t := range []string{title, trigger, body} {
			if hit, names := s.Redactor.Find(t); hit {
				return cli.Reject(cli.CodeSecretInOutput, "secret_in_decision",
					"the decision holds the value of "+strings.Join(names, ", ")+"; remove it and file again")
			}
		}
	}
	facts := map[string]string{inbox.FactTrigger: trigger, inbox.FactFiledBy: p.RunID}
	if by != "" {
		facts[inbox.FactBy] = by
	}
	actor := machine.Actor{Name: p.Actor(), RunID: p.RunID}
	id, res := s.Machine.Create(ctx, machine.CreateRequest{Project: project, Kind: machine.KindDecision, Title: title,
		Description: body, Verb: machine.VerbFile, Actor: actor, Facts: facts})
	if res.Refusal != nil {
		return res
	}
	status := machine.DecisionPending
	if now {
		if res := s.Machine.Apply(ctx, machine.Request{Project: project, Item: id, Verb: machine.VerbFire, Actor: actor,
			ExpectedStatus: machine.DecisionPending}); res.Refusal != nil {
			return res
		}
		status = machine.NeedsHuman
		s.changed()
	}
	return cli.Found(map[string]any{"decision": id, "project": project, "status": status, "by": by})
}

// Tick is the decision-timers job for one project. It fires each pending
// decision whose inbox defer ended, and each pending decision whose --by
// date has come and has not fired before. A decision a person closed meanwhile
// is left alone.
func (s *Service) Tick(ctx context.Context, project string) error {
	now := s.now()
	type due struct {
		id             int64
		item, kind, by string
	}
	var dues []due
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT id, item FROM schedule WHERE kind = ? AND project = ? AND state = 'active'
			AND due_at <= ? AND stage LIKE 'decision/%' ORDER BY due_at, id`, inbox.DeferKind, project, now.UnixMilli())
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			d := due{kind: inbox.DeferKind}
			if err := rs.Scan(&d.id, &d.item); err != nil {
				return err
			}
			dues = append(dues, d)
		}
		return rs.Err()
	})
	if err != nil {
		return err
	}
	pending, ref := s.Machine.List(ctx, project, machine.ListFilter{Kind: machine.KindDecision, Status: machine.DecisionPending})
	if ref != nil {
		return fmt.Errorf("decision: list %s: %s: %s", project, ref.Code, ref.Hint)
	}
	today := now.UTC().Format(dateLayout)
	for _, it := range pending {
		by := it.Fact(inbox.FactBy)
		if by == "" || by > today {
			continue
		}
		var fired, deferred bool
		if err := s.Store.Read(ctx, func(tx *sql.Tx) error {
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schedule WHERE kind = ? AND project = ? AND item = ? AND stage = ?)`,
				ByFiredKind, project, it.ID, by).Scan(&fired); err != nil {
				return err
			}
			return tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schedule WHERE kind = ? AND project = ? AND item = ?
				AND state = 'active' AND due_at > ?)`, inbox.DeferKind, project, it.ID, now.UnixMilli()).Scan(&deferred)
		}); err != nil {
			return err
		}
		if !fired && !deferred {
			dues = append(dues, due{kind: ByFiredKind, item: it.ID, by: by})
		}
	}
	var errs []string
	for _, d := range dues {
		res := s.Machine.Apply(ctx, machine.Request{Project: project, Item: d.item, Verb: machine.VerbFire, Actor: machine.Server(JobName),
			ExpectedStatus: machine.DecisionPending})
		settled := res.Refusal == nil || res.Refusal.Code == cli.CodeTransitionNotAllowed || res.Refusal.Code == cli.CodeStatusChanged ||
			res.Refusal.Code == cli.CodeNotFound
		if !settled {
			errs = append(errs, d.item+": "+res.Refusal.Code)
			continue
		}
		if err := s.Store.Write(ctx, func(tx *store.Tx) error {
			ms := tx.Now().UnixMilli()
			if d.kind == inbox.DeferKind {
				_, err := tx.Exec(`UPDATE schedule SET state = 'done', last_fired_at = ?, updated_at = ? WHERE id = ?`, ms, ms, d.id)
				return err
			}
			_, err := tx.Exec(`INSERT INTO schedule (project, kind, item, stage, last_fired_at, state, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, 'done', ?, ?)`, project, ByFiredKind, d.item, d.by, ms, ms, ms)
			return err
		}); err != nil {
			return err
		}
		if res.Refusal == nil {
			s.changed()
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("decision: fire: %s", strings.Join(errs, "; "))
	}
	return nil
}
