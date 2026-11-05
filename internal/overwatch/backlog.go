package overwatch

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/machine"
)

// backlogPart reads each ticket in backlog and its beads edges (C14). A
// ticket that dependency-advance cannot free is work: one with a canceled
// blocker, or with no blocks edge, waits for `loop work unblock`. A ticket
// whose every blocker is done moves on dependency-advance's next fire, and
// one with an open blocker waits on that blocker's chain. Both print a
// line with code 0. Edges of another kind block nothing.
func backlogPart() Part {
	return Part{Name: "backlog", Command: "loop work list --kind ticket --status backlog", Read: readBacklog}
}

func readBacklog(ctx context.Context, w World) (Reading, error) {
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read the projects: %w", err)
	}
	var r Reading
	for _, p := range projects {
		tickets, err := w.Items(ctx, p.ID, machine.ListFilter{Kind: machine.KindTicket, Status: machine.Backlog})
		if err != nil {
			return Reading{}, fmt.Errorf("read the backlog of %s: %w", p.ID, err)
		}
		edges := map[string][]beadsx.Dependency{}
		for _, t := range tickets {
			if err := readChain(ctx, w, p.ID, t.ID, edges); err != nil {
				return Reading{}, err
			}
		}
		for _, t := range tickets {
			f, note := classifyBacklog(t.ID, edges)
			if f != nil {
				f.Project = p.ID
				r.Findings = append(r.Findings, *f)
			} else {
				r.Notes = append(r.Notes, note)
			}
		}
	}
	return r, nil
}

// readChain reads item's edges into edges, then the edges of each open
// blocker that also stands in backlog, so classifyBacklog can name the
// open root. Each item is read once.
func readChain(ctx context.Context, w World, project, item string, edges map[string][]beadsx.Dependency) error {
	if _, ok := edges[item]; ok {
		return nil
	}
	deps, err := w.Dependencies(ctx, project, item)
	if err != nil {
		return fmt.Errorf("read the dependencies of %s: %w", item, err)
	}
	if deps == nil {
		deps = []beadsx.Dependency{}
	}
	edges[item] = deps
	for _, d := range deps {
		if d.Kind == beadsx.DepBlocks && d.Status == machine.Backlog {
			if err := readChain(ctx, w, project, d.ID, edges); err != nil {
				return err
			}
		}
	}
	return nil
}

// classifyBacklog is the backlog part's rule for one ticket. edges holds
// the ticket's edges, and the edges of each open blocker in backlog that
// its chain reaches. It returns a finding, or the note of a ticket that
// waits on something else.
func classifyBacklog(ticket string, edges map[string][]beadsx.Dependency) (*Finding, string) {
	var all, open, canceled []string
	for _, d := range edges[ticket] {
		if d.Kind != beadsx.DepBlocks {
			continue
		}
		all = append(all, d.ID+" "+d.Status)
		switch d.Status {
		case machine.Done:
		case machine.Canceled:
			canceled = append(canceled, d.ID)
		default:
			open = append(open, d.ID)
		}
	}
	key := Key("backlog", ticket)
	switch {
	case len(open) > 0:
		return nil, fmt.Sprintf("%s waits in backlog on an open blocker (%s); the open root: %s",
			ticket, strings.Join(all, ", "), strings.Join(openRoots(ticket, edges), ", "))
	case len(canceled) > 0:
		return &Finding{Key: key, Line: fmt.Sprintf("%s waits in backlog on a canceled blocker %s (%s): run `loop work unblock %s`, or name a new blocker (%s)",
			ticket, strings.Join(canceled, ", "), strings.Join(all, ", "), ticket, key)}, ""
	case len(all) == 0:
		return &Finding{Key: key, Line: fmt.Sprintf("%s waits in backlog with no blocks edge, so dependency-advance never frees it: run `loop work unblock %s` (%s)",
			ticket, ticket, key)}, ""
	}
	return nil, fmt.Sprintf("%s waits in backlog, and every blocker is done (%s): dependency-advance moves it on its next fire",
		ticket, strings.Join(all, ", "))
}

// openRoots follows ticket's open blocks edges through each blocker that
// stands in backlog, and returns the first open item off each chain, with
// its status. A backlog blocker with no open blocker of its own is a root
// too, and its own line says why it waits.
func openRoots(ticket string, edges map[string][]beadsx.Dependency) []string {
	var roots []string
	seen := map[string]bool{ticket: true}
	var walk func(item string)
	walk = func(item string) {
		for _, d := range edges[item] {
			if d.Kind != beadsx.DepBlocks || d.Status == machine.Done || d.Status == machine.Canceled || seen[d.ID] {
				continue
			}
			seen[d.ID] = true
			if d.Status == machine.Backlog && hasOpenBlocker(edges[d.ID]) {
				walk(d.ID)
				continue
			}
			roots = append(roots, d.ID+" "+d.Status)
		}
	}
	walk(ticket)
	if len(roots) == 0 {
		return []string{"none, because the chain of backlog blockers loops back"}
	}
	slices.Sort(roots)
	return roots
}

func hasOpenBlocker(deps []beadsx.Dependency) bool {
	return slices.ContainsFunc(deps, func(d beadsx.Dependency) bool {
		return d.Kind == beadsx.DepBlocks && d.Status != machine.Done && d.Status != machine.Canceled
	})
}
