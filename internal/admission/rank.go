package admission

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/machine"
)

// rankClass is the first class of D10 that fits a ticket. A lower class
// goes first.
type rankClass int

const (
	classPriority1 rankClass = iota
	classRepair
	classOverdue
	classResume
	classRework
	classRest
)

// ranked is one candidate with the keys of its rank.
type ranked struct {
	item    machine.Item
	class   rankClass
	blocked int       // distinct backlog tickets that wait on it, at any depth
	created time.Time // the create move; zero when no row exists
}

// rankImplementer orders the implementer's candidates by D10: by class,
// then by blocked count from most to fewest, then by the create move,
// oldest first, then by id. A ticket with no create move sorts as the
// oldest. It makes no claim and no write.
//
// It reads the edges of each backlog ticket once, and of no other item,
// and the transitions of the candidates in one SQLite read. With fewer than
// two candidates it reads nothing.
func (a *Admission) rankImplementer(ctx context.Context, project string, candidates []machine.Item, now time.Time) ([]machine.Item, error) {
	if len(candidates) < 2 {
		return candidates, nil
	}
	cfg, err := a.cfg.Host.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("admission: load host config: %w", err)
	}
	waiters, err := a.backlogWaiters(ctx, project)
	if err != nil {
		return nil, err
	}
	moves, err := a.rankMoves(ctx, project, candidates)
	if err != nil {
		return nil, err
	}
	rs := make([]ranked, 0, len(candidates))
	for _, it := range candidates {
		m := moves[it.ID]
		rs = append(rs, ranked{item: it, class: classOf(it, m.intoTodo, now, cfg.Overdue()),
			blocked: blockedCount(waiters, it.ID), created: m.created})
	}
	slices.SortFunc(rs, func(x, y ranked) int {
		return cmp.Or(cmp.Compare(x.class, y.class), cmp.Compare(y.blocked, x.blocked),
			x.created.Compare(y.created), cmp.Compare(x.item.ID, y.item.ID))
	})
	out := make([]machine.Item, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.item)
	}
	return out, nil
}

// classOf returns the first class that fits the ticket. A ticket in todo is
// overdue from the moment it has waited the overdue wait since its latest
// move into todo. With no such move, it is never overdue.
func classOf(it machine.Item, intoTodo, now time.Time, overdue time.Duration) rankClass {
	switch {
	case it.Fact(machine.FactPriority) == "1":
		return classPriority1
	case it.Status == machine.Repair:
		return classRepair
	case it.Status == machine.Todo && !intoTodo.IsZero() && now.Sub(intoTodo) >= overdue:
		return classOverdue
	case it.Fact(machine.FactEntry) == machine.EntryResume:
		return classResume
	case it.Fact(machine.FactEntry) == machine.EntryRework:
		return classRework
	}
	return classRest
}

// backlogWaiters reads the blocks edges of each backlog ticket, and returns
// for each item the backlog tickets that wait on it directly.
func (a *Admission) backlogWaiters(ctx context.Context, project string) (map[string][]string, error) {
	backlog, ref := a.items.List(ctx, project, machine.ListFilter{Kind: machine.KindTicket, Status: machine.Backlog})
	if ref != nil {
		return nil, refusalErr{ref}
	}
	bc, err := a.cfg.Beads(project)
	if err != nil {
		return nil, err
	}
	waiters := map[string][]string{}
	for _, b := range backlog {
		deps, err := bc.Dependencies(ctx, b.ID)
		if err != nil {
			return nil, fmt.Errorf("admission: read the edges of %s: %w", b.ID, err)
		}
		for _, d := range deps {
			if d.Kind == beadsx.DepBlocks {
				waiters[d.ID] = append(waiters[d.ID], b.ID)
			}
		}
	}
	return waiters, nil
}

// blockedCount counts the distinct backlog tickets that reach item through
// blocks edges, at any depth.
func blockedCount(waiters map[string][]string, item string) int {
	seen := map[string]bool{item: true}
	queue := []string{item}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, w := range waiters[next] {
			if !seen[w] {
				seen[w] = true
				queue = append(queue, w)
			}
		}
	}
	return len(seen) - 1
}

// moves holds the two transitions the rank reads for one ticket.
type moves struct {
	created  time.Time // the create move
	intoTodo time.Time // the latest move into todo
}

// rankMoves reads the create move and the latest move into todo of each
// candidate, in one read of transitions. A candidate with no row is absent.
func (a *Admission) rankMoves(ctx context.Context, project string, candidates []machine.Item) (map[string]moves, error) {
	ids := make([]string, 0, len(candidates))
	for _, it := range candidates {
		ids = append(ids, it.ID)
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	out := map[string]moves{}
	err = a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.item,
			max(CASE WHEN t.verb = ? THEN t.at END),
			max(CASE WHEN t.to_status = ? AND t.from_status <> t.to_status THEN t.at END)
			FROM transitions t JOIN json_each(?) j ON j.value = t.item
			WHERE t.project = ? GROUP BY t.item`, machine.VerbCreate, machine.Todo, string(raw), project)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				item              string
				created, intoTodo sql.NullInt64
			)
			if err := rows.Scan(&item, &created, &intoTodo); err != nil {
				return err
			}
			var m moves
			if created.Valid {
				m.created = time.UnixMilli(created.Int64)
			}
			if intoTodo.Valid {
				m.intoTodo = time.UnixMilli(intoTodo.Int64)
			}
			out[item] = m
		}
		return rows.Err()
	})
	return out, err
}
