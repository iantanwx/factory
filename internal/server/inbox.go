package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/decision"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/pipeline"
	"github.com/iantanwx/loop/internal/room"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

// HumanOptions holds the optional parts of the inbox, escalations,
// decisions and rooms.
type HumanOptions struct {
	// Now is the clock of the inbox and the decision timers. Sched.Now,
	// then time.Now, when nil.
	Now func() time.Time
	// PollInterval is how often `inbox wait` reads the inbox again.
	PollInterval time.Duration
	// WatchInterval is how often the server watches rooms and rings the
	// coordinator. Sched.TickInterval, then 15 seconds, when zero.
	WatchInterval time.Duration
	// RoomHarness and RoomModel run each room. The coordinator's harness,
	// then Claude Code, when empty.
	RoomHarness agenthost.Harness
	RoomModel   string
	// NewRoomID names a room. A random id when nil.
	NewRoomID func() string
	// Link returns the inbox link of a project's item. When nil, an epic
	// links to its current document PR, which the operator approves,
	// and every other item to its `loop work show` line.
	Link func(project, item string) string
}

// human is the composed inbox, escalation, decision and room parts and
// their watch loop.
type human struct {
	pokes  chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// composeHuman builds the inbox, escalations and decisions on a server
// with the machine, and rooms on a server with the scheduler too. Each
// write that changes the inbox wakes `inbox wait` and rings the
// coordinator.
func (s *Server) composeHuman() error {
	o := s.opts.Human
	now := s.humanClock()
	h := &human{pokes: make(chan struct{}, 1)}
	s.human = h
	link := o.Link
	var itemLink func(machine.Item) string
	if link == nil {
		// An epic's entry links to its current document PR.
		link, itemLink = pipeline.Link(s.Machine, s.Projects), pipeline.ItemLink(s.Projects)
	}
	s.Inbox = inbox.New(inbox.Config{Store: s.Store, Machine: s.Machine, Projects: s.Projects.List, Redactor: s.Redactor,
		Link: link, ItemLink: itemLink, Now: now, PollInterval: o.PollInterval, Logger: s.log,
		Topology: s.topology, Sessions: s.session})
	changed := func() {
		s.Inbox.Changed()
		select {
		case h.pokes <- struct{}{}:
		default:
		}
	}
	// Each ring of the doorbell writes the ring record of each destination
	// it names, which the sweep's inbox part reads (ADR 0009).
	s.Notifier = &inbox.Notifier{Inbox: s.Inbox, Ring: s.inboxRing}
	// A new fault is an inbox entry, so it rings the coordinator (KTD31).
	s.Faults.Changed = changed
	s.Escalations = &escalation.Service{Store: s.Store, Machine: s.Machine, Redactor: s.Redactor, Changed: changed, Logger: s.log}
	s.Decisions = &decision.Service{Store: s.Store, Machine: s.Machine, Redactor: s.Redactor, Projects: s.Projects.List, Now: now,
		Changed: changed, Logger: s.log}
	for _, reg := range []func(*rpc.Server) error{s.Inbox.Register, s.Escalations.Register, s.Decisions.Register} {
		if err := reg(s.RPC); err != nil {
			return err
		}
	}
	if err := s.composeOverwatch(); err != nil {
		return err
	}
	if s.Sched == nil {
		return nil
	}
	if err := s.Sched.RegisterJob(decision.JobName, s.Decisions.Tick); err != nil {
		return err
	}
	s.Sched.SetLeftOpen(s.leftOpen)
	harness, model := o.RoomHarness, o.RoomModel
	if harness == "" && s.opts.Sched.Coordinator != nil {
		harness = s.opts.Sched.Coordinator.Harness
	}
	checkouts := s.opts.Sched.Checkouts
	if checkouts == nil {
		agents := s.opts.Agents
		checkouts = func(string) sched.CheckoutHost { return agents }
	}
	rc := room.Config{Store: s.Store, Machine: s.Machine, Agents: s.opts.Agents, Checkouts: checkouts, BaseHead: s.baseHead(),
		Secrets: s.Secrets, Tokens: s.Tokens, Redactor: s.Redactor, ProjectConfig: s.projectSource(), Pack: s.opts.Sched.Pack, SkillStore: s.opts.Sched.SkillStore,
		Harness: harness, Model: model, Release: cli.Version, Changed: changed, NewID: o.NewRoomID, Logger: s.log}
	if s.Admission != nil {
		rc.BrainstormCap = s.Admission.BrainstormCap
	}
	rooms, err := room.New(rc)
	if err != nil {
		return err
	}
	s.Rooms = rooms
	return rooms.Register(s.RPC)
}

// projectSource is the scheduler's source of project config: the option,
// or the config loader over Refs.
func (s *Server) projectSource() sched.ProjectSource {
	o := s.opts.Sched
	if o.ProjectConfig != nil {
		return o.ProjectConfig
	}
	refs := o.Refs
	if refs == nil {
		refs = LocalRefs
	}
	return s.projectLoader(refs)
}

func (s *Server) session(project string) (agenthost.RunID, bool) {
	if s.Sched == nil {
		return agenthost.RunID{}, false
	}
	return s.Sched.Session(project)
}

func (s *Server) coordinatorSend(ctx context.Context, call *rpc.Call) cli.Result {
	project := call.Inv.Flags["project"]
	if project == "" {
		return cli.Reject(cli.CodeUsage, "no_project", "name the coordinator's project with --project; "+s.runningCoordinators())
	}
	if _, err := s.Projects.Show(project); err != nil {
		if errors.Is(err, config.ErrProjectNotFound) {
			return cli.Reject(cli.CodeNotFound, "unknown_project", fmt.Sprintf("project %s is not registered", project))
		}
		return cli.Internal(err)
	}
	id, ok := s.session(project)
	if !ok || s.opts.Agents == nil {
		return cli.Reject(cli.CodeNotFound, "no_coordinator", fmt.Sprintf("no coordinator session runs for project %s", project))
	}
	if err := s.opts.Agents.Notify(ctx, id, call.Inv.Flags["body"]); err != nil {
		return cli.Internal(err)
	}
	return cli.Found(map[string]any{"sent": true, "run_id": s.Sched.Coordinators()[project].RunID})
}

func (s *Server) runningCoordinators() string {
	var ps []string
	if s.Sched != nil {
		for p, cs := range s.Sched.Coordinators() {
			if cs.Session != nil {
				ps = append(ps, p)
			}
		}
	}
	if len(ps) == 0 {
		return "no coordinator session runs now"
	}
	slices.Sort(ps)
	return "a coordinator session runs for " + strings.Join(ps, ", ")
}

// leftOpen lists the work a previous coordinator of project left open:
// each question and each room that routes to project, and each other entry
// of project after its doorbell cursor. Each line holds server text only,
// as a ring does.
func (s *Server) leftOpen(ctx context.Context, project string) ([]string, error) {
	es, err := s.Inbox.Entries(ctx)
	if err != nil {
		return nil, err
	}
	var at int64
	err = s.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT coalesce((SELECT due_at FROM schedule WHERE kind = ? AND project = ?),
			(SELECT due_at FROM schedule WHERE kind = ?), -1)`, inbox.HomeNotifyKind, project, inbox.NotifyKind).Scan(&at)
	})
	if err != nil {
		return nil, err
	}
	t := s.Sched.Topology()
	var out []string
	for _, e := range es {
		if inbox.Route(e, t) == project && (e.Kind == inbox.KindQuestion || e.Kind == inbox.KindRoom || e.At > at) {
			out = append(out, fmt.Sprintf("%s (%s, awaits %s)", e.ID, e.Kind, e.Awaits))
		}
	}
	return out, nil
}

// startHuman starts the watch loop: each interval, and at once after an
// inbox change, it watches rooms and rings the coordinator. The server
// runs it after the scheduler starts.
func (s *Server) startHuman(ctx context.Context) error {
	h := s.human
	every := s.opts.Human.WatchInterval
	if every <= 0 {
		every = s.opts.Sched.TickInterval
	}
	if every <= 0 {
		every = sched.DefaultTickInterval
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	h.cancel = cancel
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			watch := false
			select {
			case <-loopCtx.Done():
				return
			case <-t.C:
				watch = true
			case <-h.pokes:
			}
			if watch && s.Rooms != nil {
				if err := s.Rooms.Watch(loopCtx); err != nil && loopCtx.Err() == nil {
					s.log.ErrorContext(loopCtx, "watch rooms", "err", err)
				}
			}
			ring, done := context.WithTimeout(loopCtx, time.Minute)
			if err := s.Notifier.Poke(ring); err != nil && loopCtx.Err() == nil {
				s.log.WarnContext(loopCtx, "ring the coordinator", "err", err)
			}
			done()
		}
	}()
	return nil
}

func (s *Server) stopHuman(context.Context) error {
	if s.human != nil && s.human.cancel != nil {
		s.human.cancel()
		s.human.wg.Wait()
	}
	return nil
}

// peek answers `loop work peek --stage`. For the triage stage it adds each
// escalation that waits for its panel, oldest first, so the triage run knows
// what to judge and route (R20).
func (s *Server) peek(ctx context.Context, call *rpc.Call) cli.Result {
	res := s.workPeek(ctx, call)
	if call.Inv.Flags["stage"] != escalation.TriageStage || s.Escalations == nil || res.Refusal != nil || res.Exit != cli.ExitDone {
		return res
	}
	c, ok := res.Data.(*admission.Candidate)
	if !ok {
		return res
	}
	pending, err := s.Escalations.Pending(ctx, c.Project)
	if err != nil {
		return cli.Internal(err)
	}
	return cli.Found(map[string]any{"candidate": res.Data, "escalations": pending,
		"next": "for each escalation: `loop escalation judge --finding <its finding>`, then `loop escalation route <id> --file <panel file>`"})
}
