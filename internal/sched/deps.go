package sched

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
)

// DependencyAdvanceJob is the native job that moves a ticket or an epic
// out of backlog once every item it waits on landed (R24).
//
// The ticket table holds a blocked ticket in backlog: decompose creates a
// ticket with a blocker there, and only unblock moves it to todo. The epic
// table holds a blocked epic there too, and unblock sends it to the state
// its epic_entry fact names.
// Admission refuses a backlog ticket with DEPENDENCY_OPEN and never reads
// beads edges itself. So a merged blocker alone admits nothing, and this
// job makes the state change. The scheduler registers it when it can read
// beads, and the server registers nothing for it.
const DependencyAdvanceJob = "dependency-advance"

// advanceDependencies is one tick of dependency-advance for one project. It
// applies unblock, as the server, to each backlog ticket and epic whose
// every blocks edge leads to an item that landed (blockersLanded). A ready
// epic that enters on prd_needed first asks the brainstorm cap, and a full cap
// leaves it in backlog for the next tick: the cap bounds the PRD epics in
// flight (D6). An item that another writer moved meanwhile is left alone.
// It then routes each bug that waits for a build whose end it reads
// (routeWaitingBugs), and completes each epic in execution whose children
// are all terminal, because a child's terminal write and its epic's
// complete write are two writes (machine.CompleteEpics). Last, it moves
// tickets in or out of in_qa by the project's QA setting (followQASetting).
// Any other refusal or read error is reported, and the next tick tries
// again.
func (s *Scheduler) advanceDependencies(ctx context.Context, project string) error {
	bc, err := s.cfg.Beads(project)
	if err != nil {
		return fmt.Errorf("sched: beads of %s: %w", project, err)
	}
	pulls := s.openPulls(project)
	var errs []error
	for _, kind := range []machine.Kind{machine.KindTicket, machine.KindEpic} {
		held, ref := s.cfg.Machine.List(ctx, project, machine.ListFilter{Kind: kind, Status: machine.Backlog})
		if ref != nil {
			errs = append(errs, fmt.Errorf("sched: list the %s backlog of %s: %s: %s", kind, project, ref.Code, ref.Hint))
			continue
		}
		for _, it := range held {
			if err := s.unblockLanded(ctx, bc, project, it, pulls); err != nil {
				errs = append(errs, err)
			}
		}
	}
	errs = append(errs, s.routeWaitingBugs(ctx, project)...)
	if err := s.cfg.Machine.CompleteEpics(ctx, project); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, s.followQASetting(ctx, project)...)
	return errors.Join(errs...)
}

// followQASetting reads the project's QA setting and applies it by
// FollowQASetting. A project with no config runs no stage, so the stage is
// off. A config that cannot be read moves nothing.
func (s *Scheduler) followQASetting(ctx context.Context, project string) []error {
	on := false
	switch snap, err := s.cfg.ProjectConfig(ctx, project); {
	case err == nil:
		on = snap.Config.StageEnabled(config.StageQA)
	case !errors.Is(err, config.ErrConfigMissing):
		return []error{fmt.Errorf("sched: the QA setting of %s: %w", project, err)}
	}
	moved, errs := FollowQASetting(ctx, s.cfg.Machine, project, on)
	for _, id := range moved {
		s.log.InfoContext(ctx, "QA setting followed", "project", project, "item", id, "qa", on)
	}
	return errs
}

// FollowQASetting moves the tickets that a change of the QA setting left in
// the wrong state, as the server (spec factorio-nhb5 C15), and answers the
// tickets it moved. With the stage off, it applies qa_off to each in_qa
// ticket, and the machine refuses a ticket that a run holds: that run
// settles it. With the stage on, it applies qa_due to each ready_to_merge
// or queued ticket, and the machine refuses a ticket whose qa_head equals
// its reviewed_head. The merge hand-off then withdraws the pull request of
// each ticket that left queued.
func FollowQASetting(ctx context.Context, m *machine.Machine, project string, on bool) (moved []string, errs []error) {
	verb, from := machine.VerbQAOff, []string{machine.InQA}
	if on {
		verb, from = machine.VerbQADue, []string{machine.ReadyToMerge, machine.Queued}
	}
	for _, status := range from {
		tickets, ref := m.List(ctx, project, machine.ListFilter{Kind: machine.KindTicket, Status: status})
		if ref != nil {
			errs = append(errs, fmt.Errorf("sched: list the %s tickets of %s: %s: %s", status, project, ref.Code, ref.Hint))
			continue
		}
		for _, t := range tickets {
			res := m.Apply(ctx, machine.Request{Project: project, Item: t.ID, Verb: verb,
				ExpectedStatus: status, Actor: machine.Server(DependencyAdvanceJob)})
			switch r := res.Refusal; {
			case r == nil:
				moved = append(moved, t.ID)
			case r.Code == cli.CodeLeaseHeld || r.Code == cli.CodeStatusChanged || r.Code == cli.CodeTransitionNotAllowed || r.Code == cli.CodeNotFound:
				// A run holds the ticket, QA passed its reviewed head, or
				// another writer moved it. Nothing waits.
			default:
				errs = append(errs, fmt.Errorf("sched: %s %s: %s: %s", verb, t.ID, r.Code, r.Hint))
			}
		}
	}
	return moved, errs
}

// unblockLanded applies unblock to one backlog item once every blocker
// landed, and once the brainstorm cap admits a PRD epic.
func (s *Scheduler) unblockLanded(ctx context.Context, bc beadsx.Client, project string, it machine.Item, pulls openHeads) error {
	ready, err := blockersLanded(ctx, bc, it, pulls)
	if err != nil {
		return fmt.Errorf("sched: read the dependencies of %s: %w", it.ID, err)
	}
	if !ready {
		return nil
	}
	if it.Kind == machine.KindEpic && it.Fact(machine.FactEpicEntry) == machine.PRDNeeded {
		switch r := s.cfg.Admission.BrainstormCap(ctx, project); {
		case r == nil:
		case r.Code == cli.CodeCapFull:
			return nil
		default:
			return fmt.Errorf("sched: the brainstorm cap for %s: %s: %s", it.ID, r.Code, r.Hint)
		}
	}
	res := s.cfg.Machine.Apply(ctx, machine.Request{Project: project, Item: it.ID, Verb: machine.VerbUnblock,
		ExpectedStatus: machine.Backlog, Actor: machine.Server(DependencyAdvanceJob)})
	switch r := res.Refusal; {
	case r == nil:
		s.log.InfoContext(ctx, "dependencies done", "project", project, "item", it.ID)
	case r.Code == cli.CodeStatusChanged || r.Code == cli.CodeTransitionNotAllowed || r.Code == cli.CodeNotFound:
		// Another writer moved the item. Nothing waits.
	default:
		return fmt.Errorf("sched: unblock %s: %s: %s", it.ID, r.Code, r.Hint)
	}
	return nil
}

// routeWaitingBugs reads the build of each bug in awaiting_build (D9). A
// done build applies build_landed, which closes the bug as its duplicate.
// A canceled build applies build_canceled, which returns the bug to
// reported. A build in any other state leaves the bug waiting. A bug that
// another writer moved meanwhile is left alone.
func (s *Scheduler) routeWaitingBugs(ctx context.Context, project string) []error {
	waiting, ref := s.cfg.Machine.List(ctx, project, machine.ListFilter{Kind: machine.KindBug, Status: machine.AwaitingBuild})
	if ref != nil {
		return []error{fmt.Errorf("sched: list the waiting bugs of %s: %s: %s", project, ref.Code, ref.Hint)}
	}
	var errs []error
	for _, bug := range waiting {
		build, ref := s.cfg.Machine.Read(ctx, project, bug.Fact(machine.FactBuiltBy))
		if ref != nil {
			errs = append(errs, fmt.Errorf("sched: read the build of %s: %s: %s", bug.ID, ref.Code, ref.Hint))
			continue
		}
		var verb string
		switch build.Status {
		case machine.Done:
			verb = machine.VerbBuildLanded
		case machine.Canceled:
			verb = machine.VerbBuildCanceled
		default:
			continue
		}
		res := s.cfg.Machine.Apply(ctx, machine.Request{Project: project, Item: bug.ID, Verb: verb,
			ExpectedStatus: machine.AwaitingBuild, Actor: machine.Server(DependencyAdvanceJob)})
		switch r := res.Refusal; {
		case r == nil:
			s.log.InfoContext(ctx, "build ended", "project", project, "item", bug.ID, "build", build.ID, "status", build.Status)
		case r.Code == cli.CodeStatusChanged || r.Code == cli.CodeTransitionNotAllowed || r.Code == cli.CodeNotFound:
			// Another writer routed the bug. Nothing waits.
		default:
			errs = append(errs, fmt.Errorf("sched: %s %s: %s: %s", verb, bug.ID, r.Code, r.Hint))
		}
	}
	return errs
}

// openHeads answers the head branches of a project's open pull requests.
// known is false when no reader exists, and then no answer proves that a
// pull request is closed.
type openHeads func(ctx context.Context) (open map[string]bool, known bool, err error)

// openPulls reads the open pull requests of a project at most once, on the
// first call, and answers every later call of the tick from that read.
func (s *Scheduler) openPulls(project string) openHeads {
	var (
		read bool
		open map[string]bool
		err  error
	)
	return func(ctx context.Context) (map[string]bool, bool, error) {
		if s.cfg.Pulls == nil {
			return nil, false, nil
		}
		if !read {
			read = true
			var heads []admission.PullHead
			heads, err = s.cfg.Pulls.OpenPulls(ctx, project)
			if err != nil {
				err = fmt.Errorf("read the open pull requests of %s: %w", project, err)
			}
			open = make(map[string]bool, len(heads))
			for _, h := range heads {
				open[h.Branch] = true
			}
		}
		return open, true, err
	}
}

// blockersLanded says whether the item has at least one blocker and every
// blocker landed. A blocker is the target of a blocks edge, or the item
// that pending_block names: work block writes that fact in its move,
// before its edge, so a crash between the two still holds the ticket
// while that item is open (spec factorio-9wh Decision 22). A blocker is a
// ticket, an epic or a decision. A done item landed. A moot or dropped
// decision is canceled, and it landed too: the question no longer applies,
// and with no branch it holds no pull request, so the read below never
// holds it.
// A canceled ticket landed when it holds no open pull request, as
// blocker-landed.ts in tickertown rules for every closed blocker: no code
// of it can reach the base any more, so nothing is left to wait for. A
// canceled ticket whose pull request is still open holds, because that
// pull request can still merge. With no reader, or after a failed read, a
// canceled ticket holds too: a gap never reads as "no pull request". Only
// a tick that finds a canceled blocker, and no blocker still open, reads.
//
// decompose and epic create add every edge before the create that puts
// the item in backlog, so a backlog item with no blocker is a doubt, and
// a doubt holds the item. A person decides with `work unblock`. Edges of
// another kind block nothing.
func blockersLanded(ctx context.Context, bc beadsx.Client, it machine.Item, pulls openHeads) (bool, error) {
	deps, err := bc.Dependencies(ctx, it.ID)
	if err != nil {
		return false, err
	}
	deps = slices.DeleteFunc(deps, func(d beadsx.Dependency) bool { return d.Kind != beadsx.DepBlocks })
	if p := it.Fact(machine.FactPendingBlock); p != "" && !slices.ContainsFunc(deps, func(d beadsx.Dependency) bool { return d.ID == p }) {
		is, err := bc.Get(ctx, p)
		if err != nil {
			return false, err
		}
		deps = append(deps, beadsx.Dependency{ID: p, Kind: beadsx.DepBlocks, Status: is.Status})
	}
	var canceled []string
	for _, d := range deps {
		switch d.Status {
		case machine.Done:
		case machine.Canceled:
			canceled = append(canceled, d.ID)
		default:
			return false, nil
		}
	}
	if len(canceled) > 0 {
		open, known, err := pulls(ctx)
		if err != nil || !known {
			return false, err
		}
		for _, id := range canceled {
			if open[github.BranchFor(id)] {
				return false, nil
			}
		}
	}
	return len(deps) > 0, nil
}
