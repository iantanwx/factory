package sched

import (
	"context"
	"errors"
	"fmt"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// DependencyAdvanceJob is the native job that moves a ticket out of
// backlog once every ticket it waits on is done (R24).
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
// blocks edge leads to a done ticket. A ticket that another writer moved
// meanwhile is left alone. It then completes each epic in execution whose
// children are all terminal, because a child's terminal write and its
// epic's complete write are two writes (machine.CompleteEpics). Any other
// refusal or read error is reported, and the next tick tries again.
func (s *Scheduler) advanceDependencies(ctx context.Context, project string) error {
	bc, err := s.cfg.Beads(project)
	if err != nil {
		return fmt.Errorf("sched: beads of %s: %w", project, err)
	}
	held, ref := s.cfg.Machine.List(ctx, project, machine.ListFilter{Kind: machine.KindTicket, Status: machine.Backlog})
	if ref != nil {
		return fmt.Errorf("sched: list the backlog of %s: %s: %s", project, ref.Code, ref.Hint)
	}
	var errs []error
	for _, it := range held {
		ready, err := blockersDone(ctx, bc, it.ID)
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

// blockersDone says whether the item has at least one blocks edge and
// every blocks edge leads to a done ticket. decompose adds every edge
// before the create that puts the ticket in backlog, so a backlog ticket
// with no blocks edge is a doubt, and a doubt holds the ticket. A canceled
// blocker never landed, so it holds the ticket too. A person decides
// either case with `work unblock`. Edges of another kind block nothing.
func blockersDone(ctx context.Context, bc beadsx.Client, item string) (bool, error) {
	deps, err := bc.Dependencies(ctx, item)
	if err != nil {
		return false, err
	}
	blockers := 0
	for _, d := range deps {
		if d.Kind != beadsx.DepBlocks {
			continue
		}
		blockers++
		if d.Status != machine.Done {
			return false, nil
		}
	}
	return blockers > 0, nil
}
