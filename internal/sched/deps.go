package sched

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
)

// DependencyAdvanceJob is the native job that moves a ticket out of
// backlog once every ticket it waits on landed (R24).
//
// The ticket table holds a blocked ticket in backlog: decompose creates a
// ticket with a blocker there, and only unblock moves it to todo.
// Admission refuses a backlog ticket with DEPENDENCY_OPEN and never reads
// beads edges itself. So a merged blocker alone admits nothing, and this
// job makes the state change. The scheduler registers it when it can read
// beads, and the server registers nothing for it.
const DependencyAdvanceJob = "dependency-advance"

// advanceDependencies is one tick of dependency-advance for one project. It
// applies unblock, as the server, to each backlog ticket whose every
// blocks edge leads to a ticket that landed (blockersLanded). A ticket
// that another writer moved meanwhile is left alone. It then completes
// each epic in execution whose children are all terminal, because a
// child's terminal write and its epic's complete write are two writes
// (machine.CompleteEpics). Any other refusal or read error is reported,
// and the next tick tries again.
func (s *Scheduler) advanceDependencies(ctx context.Context, project string) error {
	bc, err := s.cfg.Beads(project)
	if err != nil {
		return fmt.Errorf("sched: beads of %s: %w", project, err)
	}
	held, ref := s.cfg.Machine.List(ctx, project, machine.ListFilter{Kind: machine.KindTicket, Status: machine.Backlog})
	if ref != nil {
		return fmt.Errorf("sched: list the backlog of %s: %s: %s", project, ref.Code, ref.Hint)
	}
	pulls := s.openPulls(project)
	var errs []error
	for _, it := range held {
		ready, err := blockersLanded(ctx, bc, it, pulls)
		if err != nil {
			errs = append(errs, fmt.Errorf("sched: read the dependencies of %s: %w", it.ID, err))
			continue
		}
		if !ready {
			continue
		}
		res := s.cfg.Machine.Apply(ctx, machine.Request{Project: project, Item: it.ID, Verb: machine.VerbUnblock,
			ExpectedStatus: machine.Backlog, Actor: machine.Server(DependencyAdvanceJob)})
		switch r := res.Refusal; {
		case r == nil:
			s.log.InfoContext(ctx, "dependencies done", "project", project, "item", it.ID)
		case r.Code == cli.CodeStatusChanged || r.Code == cli.CodeTransitionNotAllowed || r.Code == cli.CodeNotFound:
			// Another writer moved the ticket. Nothing waits.
		default:
			errs = append(errs, fmt.Errorf("sched: unblock %s: %s: %s", it.ID, r.Code, r.Hint))
		}
	}
	if err := s.cfg.Machine.CompleteEpics(ctx, project); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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
// while that item is open (spec factorio-9wh Decision 22). A done ticket
// landed.
// A canceled ticket landed when it holds no open pull request, as
// blocker-landed.ts in tickertown rules for every closed blocker: no code
// of it can reach the base any more, so nothing is left to wait for. A
// canceled ticket whose pull request is still open holds, because that
// pull request can still merge. With no reader, or after a failed read, a
// canceled ticket holds too: a gap never reads as "no pull request". Only
// a tick that finds a canceled blocker, and no blocker still open, reads.
//
// decompose adds every edge before the create that puts the ticket in
// backlog, so a backlog ticket with no blocker is a doubt, and a doubt
// holds the ticket. A person decides with `work unblock`. Edges of
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
