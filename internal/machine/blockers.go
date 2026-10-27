package machine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

// pointBlockers is the kill point after a new ticket's blocks edges and
// before its create move. The ticket is in Initial there, in no queue.
const pointBlockers = "blockers_added"

// ParseBlockers reads --blocked-by: item ids separated by commas.
func ParseBlockers(v string) []string {
	var out []string
	for _, id := range strings.Split(v, ",") {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// CheckBlockers refuses a blocker list for ticket before any write: an
// unknown id, the ticket itself, an epic, a bug, a terminal item, or an id
// that would close a cycle. ticket is "" for a ticket not yet created.
// Only a ticket or a decision blocks, because dependency-advance frees a
// ticket when each blocker is done, and only those two kinds reach done.
func (m *Machine) CheckBlockers(ctx context.Context, project, ticket string, blockers []string) *cli.Refusal {
	for _, id := range blockers {
		if id == ticket {
			return blockerRefusal(id, "blocker_self", fmt.Sprintf("%s cannot wait on itself", id))
		}
		it, r := m.Read(ctx, project, id)
		if r != nil {
			return r
		}
		t, _ := TableFor(it.Kind)
		switch {
		case it.Kind == KindEpic:
			return blockerRefusal(id, "blocker_epic", fmt.Sprintf("%s is an epic; a ticket waits on a ticket or a decision", id))
		case it.Kind == KindBug && it.Fact(FactTicket) != "":
			return blockerRefusal(id, "blocker_bug", fmt.Sprintf("%s is a bug, which never reaches done; wait on %s, the ticket it was shaped into",
				id, it.Fact(FactTicket)))
		case it.Kind == KindBug:
			return blockerRefusal(id, "blocker_bug", fmt.Sprintf("%s is a bug, which never reaches done; a ticket waits on a ticket or a decision", id))
		case t.Terminal(it.Status):
			return blockerRefusal(id, "blocker_terminal", fmt.Sprintf("%s is %s already, so it holds nothing", id, it.Status))
		}
		if ticket == "" {
			continue
		}
		if cyc, r := m.leadsTo(ctx, project, id, ticket); r != nil {
			return r
		} else if cyc {
			return blockerRefusal(id, "blocker_cycle", fmt.Sprintf("%s already waits on %s through its blocks edges", id, ticket))
		}
	}
	return nil
}

func blockerRefusal(id, rule, hint string) *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeUsage, Rule: rule, Item: id, Hint: hint + "; nothing was written"}
}

// leadsTo walks the blocks edges forward from item, and says whether a
// chain of them reaches target.
func (m *Machine) leadsTo(ctx context.Context, project, item, target string) (bool, *cli.Refusal) {
	bc, err := m.cfg.Beads(project)
	if err != nil {
		return false, cli.InternalRefusal(err)
	}
	seen, next := map[string]bool{item: true}, []string{item}
	for len(next) > 0 {
		id := next[0]
		next = next[1:]
		deps, err := bc.Dependencies(ctx, id)
		if err != nil {
			return false, beadsRefusal(id, err)
		}
		for _, d := range deps {
			switch {
			case d.Kind != beadsx.DepBlocks || seen[d.ID]:
			case d.ID == target:
				return true, nil
			default:
				seen[d.ID] = true
				next = append(next, d.ID)
			}
		}
	}
	return false, nil
}

// addBlockers writes one blocks edge from a ticket in Initial to each
// blocker. The edges go in before the create move, as epic decompose
// writes them, so a ticket that left Initial holds all of its edges.
func (m *Machine) addBlockers(ctx context.Context, project, ticket string, blockers []string) *cli.Refusal {
	bc, err := m.cfg.Beads(project)
	if err != nil {
		return cli.InternalRefusal(err)
	}
	for _, b := range blockers {
		if err := bc.AddDependency(ctx, ticket, b, beadsx.DepBlocks); err != nil {
			return beadsRefusal(ticket, err)
		}
	}
	if m.stop(pointBlockers) {
		return cli.InternalRefusal(errStopped)
	}
	return nil
}
