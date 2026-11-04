package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
)

// starvedPart is tickertown's STARVED stall (alpha
// scripts/sdlc/stall-starved.ts). The stalls and backlog parts judge one
// item at a time, so an epic whose open tickets each wait on something
// reads as fine there while the implementer idles. This part judges the
// epic. An epic in execution starves when the implementer of its project
// has a free slot and takes no ticket, and none of the epic's open tickets
// moves. A ticket moves when no person holds it, and a live run holds its
// lease, a stage takes it now, or it waits on a dependency that moves.
func starvedPart() Part {
	return Part{Name: "starved", Command: "loop work list --kind epic --status execution", Read: readStarved}
}

// takers are the stages after the implementer whose admission a ticket
// waits for. merge-handoff takes the merge states, and admits no run.
var takers = []string{config.StageSimplify, config.StageCodeReviewer, config.StageQA}

func readStarved(ctx context.Context, w World) (Reading, error) {
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read the projects: %w", err)
	}
	var r Reading
	for _, p := range projects {
		epics, err := w.Items(ctx, p.ID, machine.ListFilter{Kind: machine.KindEpic, Status: machine.Execution})
		if err != nil {
			return Reading{}, fmt.Errorf("read the epics of %s in %s: %w", p.ID, machine.Execution, err)
		}
		if len(epics) == 0 {
			continue
		}
		tickets, err := w.Items(ctx, p.ID, machine.ListFilter{Kind: machine.KindTicket})
		if err != nil {
			return Reading{}, fmt.Errorf("read the tickets of %s: %w", p.ID, err)
		}
		byID := map[string]machine.Item{}
		open := map[string][]machine.Item{}
		for _, t := range tickets {
			byID[t.ID] = t
			if t.Status != machine.Done && t.Status != machine.Canceled {
				epic := t.Fact(machine.FactEpic)
				open[epic] = append(open[epic], t)
			}
		}
		if !slices.ContainsFunc(epics, func(e machine.Item) bool { return len(open[e.ID]) > 0 }) {
			continue
		}
		busy, err := implementerBusy(ctx, w.WorkQuery(), p.ID)
		if err != nil {
			return Reading{}, err
		}
		if busy != "" {
			r.Notes = append(r.Notes, busy)
			continue
		}
		j := &judge{ctx: ctx, w: w, project: p.ID, tickets: byID, seen: map[string]verdict{}}
		for _, e := range epics {
			f, note, err := j.epic(e, open[e.ID])
			switch {
			case err != nil:
				return Reading{}, err
			case f != nil:
				r.Findings = append(r.Findings, *f)
			case note != "":
				r.Notes = append(r.Notes, note)
			}
		}
	}
	return r, nil
}

// implementerBusy says why the implementer of project is busy: it has no
// free slot, or it takes a ticket now. Empty when it has a free slot and
// takes no ticket.
func implementerBusy(ctx context.Context, q WorkQuery, project string) (string, error) {
	if q == nil {
		return "", errors.New("the server holds no admission to ask whether the implementer takes a ticket")
	}
	stage := config.StageImplementer
	if ref := q.Room(ctx, project, stage); ref != nil {
		if ref.Code == cli.CodeInternal {
			return "", fmt.Errorf("read the free slots of %s in %s: %s", stage, project, ref.Hint)
		}
		return fmt.Sprintf("%s: the %s has no free slot, so no epic starves: %s", project, stage, ref.Hint), nil
	}
	c, ref := q.Peek(ctx, project, stage)
	switch {
	case ref != nil:
		return "", fmt.Errorf("peek the %s of %s: %s: %s", stage, project, ref.Code, ref.Hint)
	case c != nil:
		return fmt.Sprintf("%s: the %s takes %s now, so no epic starves", project, stage, c.Item), nil
	}
	return "", nil
}

// verdict is whether one ticket moves, and why.
type verdict struct {
	moves bool
	why   string
}

// judge judges the tickets of one project whose implementer is idle. Each
// ticket is judged once.
type judge struct {
	ctx     context.Context
	w       World
	project string
	tickets map[string]machine.Item
	seen    map[string]verdict
}

// epic returns the finding of an epic none of whose open tickets moves, or
// the note that names a ticket that moves.
func (j *judge) epic(e machine.Item, open []machine.Item) (*Finding, string, error) {
	if len(open) == 0 {
		return nil, "", nil
	}
	var held []string
	for _, t := range open {
		v, err := j.moves(t)
		if err != nil {
			return nil, "", err
		}
		if v.moves {
			return nil, fmt.Sprintf("epic %s of %s does not starve: %s %s moves, since %s", e.ID, j.project, t.ID, t.Status, v.why), nil
		}
		held = append(held, fmt.Sprintf("%s %s (%s)", t.ID, t.Status, v.why))
	}
	key := Key("starved", j.project, e.ID)
	return &Finding{Key: key, Line: fmt.Sprintf("epic %s (%q) of %s starves: the implementer has a free slot and takes no ticket, and none of its open tickets moves: %s (%s)",
		e.ID, e.Title, j.project, strings.Join(held, "; "), key)}, "", nil
}

// moves judges one ticket. A ticket on a loop of dependencies that reaches
// it again does not move by that loop.
func (j *judge) moves(it machine.Item) (verdict, error) {
	if v, ok := j.seen[it.ID]; ok {
		return v, nil
	}
	j.seen[it.ID] = verdict{why: "its dependencies loop back to it"}
	v, err := j.judge(it)
	if err != nil {
		return verdict{}, err
	}
	j.seen[it.ID] = v
	return v, nil
}

func (j *judge) judge(it machine.Item) (verdict, error) {
	var lease, hold, person, alert string
	err := j.w.Store().Read(j.ctx, func(tx *sql.Tx) error {
		var err error
		if lease, err = leaseOf(j.ctx, tx, it); err != nil {
			return err
		}
		if hold, err = holdOf(j.ctx, tx, it); err != nil {
			return err
		}
		if person, err = personOf(j.ctx, tx, it); err != nil {
			return err
		}
		alert, err = alertOf(j.ctx, tx, it)
		return err
	})
	if err != nil {
		return verdict{}, fmt.Errorf("read what holds %s: %w", it.ID, err)
	}
	switch {
	case person != "":
		return verdict{why: person}, nil
	case hold != "":
		return verdict{why: hold + " holds it"}, nil
	case it.Status == machine.Escalated:
		return verdict{why: "it waits for a person in " + machine.Escalated}, nil
	case lease != "":
		return verdict{moves: true, why: lease + " holds its lease"}, nil
	case it.Status == machine.ReadyToMerge || it.Status == machine.Queued:
		return verdict{moves: true, why: "merge-handoff takes it"}, nil
	}
	var why []string
	switch it.Status {
	case machine.Todo, machine.Repair:
		why = append(why, "the implementer does not take it")
	case machine.InProgress:
		why = append(why, "no live run holds its lease")
	}
	for _, stage := range takers {
		if !admission.Takes(stage, it) {
			continue
		}
		ok, err := j.w.WorkQuery().Admissible(j.ctx, stage, it)
		if err != nil {
			return verdict{}, fmt.Errorf("ask whether %s takes %s: %w", stage, it.ID, err)
		}
		if ok {
			return verdict{moves: true, why: stage + " takes it now"}, nil
		}
		why = append(why, stage+" does not take it now")
	}
	if alert != "" {
		why = append(why, alert+" is open on it")
	}
	deps, err := j.w.Dependencies(j.ctx, j.project, it.ID)
	if err != nil {
		return verdict{}, fmt.Errorf("read the dependencies of %s: %w", it.ID, err)
	}
	var waits []string
	done := 0
	for _, d := range deps {
		if d.Kind != beadsx.DepBlocks {
			continue
		}
		waits = append(waits, d.ID+" "+d.Status)
		switch d.Status {
		case machine.Done:
			done++
			continue
		case machine.Canceled:
			continue
		}
		t, ok := j.tickets[d.ID]
		if !ok {
			continue
		}
		v, err := j.moves(t)
		if err != nil {
			return verdict{}, err
		}
		if v.moves {
			return verdict{moves: true, why: fmt.Sprintf("it waits on %s, which moves, since %s", d.ID, v.why)}, nil
		}
	}
	switch {
	case it.Status == machine.Backlog && len(waits) > 0 && done == len(waits):
		return verdict{moves: true, why: "every blocker is done, so dependency-advance frees it"}, nil
	case len(waits) > 0:
		why = append(why, "it waits on "+strings.Join(waits, ", "))
	case it.Status == machine.Backlog:
		why = append(why, "it waits in backlog with no blocks edge")
	}
	return verdict{why: strings.Join(why, ", ")}, nil
}

// personOf names the oldest escalation of the item that waits for a person.
// Empty when none does.
func personOf(ctx context.Context, tx *sql.Tx, it machine.Item) (string, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM escalations WHERE project = ? AND item = ? AND state = ? ORDER BY id LIMIT 1`,
		it.Project, it.ID, escalation.StatePerson).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return fmt.Sprintf("escalation %d waits for a person", id), err
}
