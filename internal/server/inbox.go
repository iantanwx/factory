package server

import (
	"context"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/cli"
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
	// links to its current document PR, whose merge is the sign-off,
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
	now := o.Now
	if now == nil {
		now = s.opts.Sched.Now
	}
	if now == nil {
		now = time.Now
	}
	h := &human{pokes: make(chan struct{}, 1)}
	s.human = h
	link := o.Link
	var itemLink func(machine.Item) string
	if link == nil {
		// An epic's entry links to its current document PR.
		link, itemLink = pipeline.Link(s.Machine, s.Projects), pipeline.ItemLink(s.Projects)
	}
	s.Inbox = inbox.New(inbox.Config{Store: s.Store, Machine: s.Machine, Projects: s.Projects.List, Redactor: s.Redactor,
		Link: link, ItemLink: itemLink, Now: now, PollInterval: o.PollInterval, Logger: s.log})
	changed := func() {
		s.Inbox.Changed()
		select {
		case h.pokes <- struct{}{}:
		default:
		}
	}
	s.Notifier = &inbox.Notifier{Inbox: s.Inbox, Ring: s.ringCoordinator}
	// A new fault is an inbox entry, so it rings the coordinator (KTD31).
	s.Faults.Changed = changed
	s.Escalations = &escalation.Service{Store: s.Store, Machine: s.Machine, Redactor: s.Redactor, Changed: changed, Logger: s.log}
	s.Decisions = &decision.Service{Store: s.Store, Machine: s.Machine, Redactor: s.Redactor, Now: now, Changed: changed, Logger: s.log}
	for _, reg := range []func(*rpc.Server) error{s.Inbox.Register, s.Escalations.Register, s.Decisions.Register} {
		if err := reg(s.RPC); err != nil {
			return err
		}
	}
	if s.Sched == nil {
		return nil
	}
	if err := s.Sched.RegisterJob(decision.JobName, s.Decisions.Tick); err != nil {
		return err
	}
	harness, model := o.RoomHarness, o.RoomModel
	if harness == "" && s.opts.Sched.Coordinator != nil {
		harness = s.opts.Sched.Coordinator.Harness
	}
	checkouts := s.opts.Sched.Checkouts
	if checkouts == nil {
		agents := s.opts.Agents
		checkouts = func(string) sched.CheckoutHost { return agents }
	}
	rc := room.Config{Store: s.Store, Machine: s.Machine, Agents: s.opts.Agents, Checkouts: checkouts,
		Secrets: s.Secrets, Tokens: s.Tokens, Redactor: s.Redactor, ProjectConfig: s.projectSource(), Pack: s.opts.Sched.Pack,
		Harness: harness, Model: model, Release: cli.Version, Changed: changed, NewID: o.NewRoomID, Logger: s.log}
	if s.Admission != nil {
		rc.IntakeCap = s.Admission.IntakeCap
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

// ringCoordinator types one line into the coordinator's session. With no
// coordinator session it rings nobody, and the inbox stays pull-durable.
func (s *Server) ringCoordinator(ctx context.Context, text string) error {
	if s.Sched == nil || s.opts.Agents == nil {
		return inbox.ErrNobodyToRing
	}
	cs := s.Sched.Coordinator()
	if cs.Session == nil {
		return inbox.ErrNobodyToRing
	}
	return s.opts.Agents.Notify(ctx, cs.Session.ID, text)
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

// peek answers `loop work peek --stage`. For the fixer it adds each
// escalation that waits for its panel, oldest first, so the fixer knows
// what to judge and route (R20).
func (s *Server) peek(ctx context.Context, call *rpc.Call) cli.Result {
	res := s.workPeek(ctx, call)
	if call.Inv.Flags["stage"] != "fixer" || s.Escalations == nil || res.Refusal != nil || res.Exit != cli.ExitDone {
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
