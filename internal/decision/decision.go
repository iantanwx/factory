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
	"github.com/iantanwx/loop/internal/config"
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
	// Projects lists the projects that `decision list` reads when its
	// caller names none.
	Projects func() ([]config.Project, error)
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

// Register sets the handlers of `decision file` and `decision list`.
func (s *Service) Register(srv *rpc.Server) error {
	if err := srv.Handle("decision file", s.file); err != nil {
		return err
	}
	return srv.Handle("decision list", s.list)
}

// Listed is one decision that `decision list` answers.
type Listed struct {
	Project  string `json:"project"`
	Decision string `json:"decision"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Trigger  string `json:"trigger"`
}

// list is `loop decision list`: the decisions of one project, in id order.
// A caller that names no project, such as the coordinator, reads every
// project. A decision is no work, so `work list` never names one.
func (s *Service) list(ctx context.Context, call *rpc.Call) cli.Result {
	var projects []string
	prj, ref := call.Project("a run lists the decisions of its own project only")
	switch {
	case ref == nil:
		projects = []string{prj}
	case ref.Rule == "no_project" && s.Projects != nil:
		ps, err := s.Projects()
		if err != nil {
			return cli.Internal(err)
		}
		for _, p := range ps {
			projects = append(projects, p.ID)
		}
	default:
		return ref.Result()
	}
	out := []Listed{}
	for _, prj := range projects {
		its, ref := s.Machine.List(ctx, prj, machine.ListFilter{Kind: machine.KindDecision, Status: call.Inv.Flags["status"]})
		if ref != nil {
			return ref.Result()
		}
		for _, it := range its {
			out = append(out, Listed{Project: prj, Decision: it.ID, Title: it.Title, Status: it.Status, Trigger: it.Fact(inbox.FactTrigger)})
		}
	}
	return cli.List(out)
}

// file is `loop decision file`: a question that waits for its trigger. A
// run files in its own project, and the coordinator names one. Exactly one
// of --by and --waits-for-a-person names what notices the trigger: a
// decision with neither has no reader (ADR 0005, D5). --now says
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
	waits := strings.TrimSpace(inv.Flags["waits-for-a-person"])
	if (by == "") == (waits == "") {
		return cli.Reject(cli.CodeUsage, "no_notice",
			`name what notices the trigger, exactly one of --by <YYYY-MM-DD> and --waits-for-a-person "<who or what>"`)
	}
	now := inv.Flags["now"] == "true"
	if now && by != "" {
		return cli.Reject(cli.CodeUsage, "now_and_by", "--now says the trigger fired, and --by names a day it fires; pass one")
	}
	body := inv.Flags["body"]
	if s.Redactor != nil {
		for _, t := range []string{title, trigger, waits, body} {
			if hit, names := s.Redactor.Find(t); hit {
				return cli.Reject(cli.CodeSecretInOutput, "secret_in_decision",
					"the decision holds the value of "+strings.Join(names, ", ")+"; remove it and file again")
			}
		}
	}
	facts := map[string]string{inbox.FactTrigger: trigger, inbox.FactFiledBy: p.RunID}
	if by != "" {
		facts[inbox.FactBy] = by
	} else {
		facts[inbox.FactWaitsFor] = waits
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
			Params:         map[string]string{"why": "decision file --now: the trigger already fired when it was filed"},
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
// is left alone. Each fire records why, and a fire that ends an inbox defer
// carries that defer's reason (spec Decision 19).
func (s *Service) Tick(ctx context.Context, project string) error {
	now := s.now()
	type due struct {
		id             int64
		item, kind, by string
		params         map[string]string
	}
	var dues []due
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT id, item, due_at, coalesce(json_extract(payload, '$.reason'), '') FROM schedule
			WHERE kind = ? AND project = ? AND state = 'active' AND due_at <= ? AND stage LIKE ? ORDER BY due_at, id`,
			inbox.DeferKind, project, now.UnixMilli(), inbox.KindHold+"/%")
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			d := due{kind: inbox.DeferKind}
			var at int64
			var reason string
			if err := rs.Scan(&d.id, &d.item, &at, &reason); err != nil {
				return err
			}
			d.params = map[string]string{"defer_reason": reason,
				"why": "decision-timers fired it: the inbox defer until " + time.UnixMilli(at).UTC().Format(time.RFC3339) + " ended"}
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
			dues = append(dues, due{kind: ByFiredKind, item: it.ID, by: by,
				params: map[string]string{"why": "decision-timers fired it: its --by date " + by + " came"}})
		}
	}
	var errs []string
	for _, d := range dues {
		res := s.Machine.Apply(ctx, machine.Request{Project: project, Item: d.item, Verb: machine.VerbFire, Params: d.params,
			Actor: machine.Server(JobName), ExpectedStatus: machine.DecisionPending})
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
