package overwatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
)

// decisionsPart prints one note for each pending decision in every project
// (C13). A decision that waits on its event holds no work that an act of
// the coordinator can end, so the part never holds a finding and never
// rings. The coordinator reads the notes on every wake, and fires a
// decision whose event happened. A decision that cannot wait names a `by`
// date, and decision-timers fires it on that day.
func decisionsPart() Part {
	return Part{Name: "decisions", Command: "loop work list --kind decision --status pending", Read: readDecisions}
}

func readDecisions(ctx context.Context, w World) (Reading, error) {
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read the projects: %w", err)
	}
	var pending []machine.Item
	for _, p := range projects {
		items, err := w.Items(ctx, p.ID, machine.ListFilter{Kind: machine.KindDecision, Status: machine.DecisionPending})
		if err != nil {
			return Reading{}, fmt.Errorf("read the pending decisions of %s: %w", p.ID, err)
		}
		pending = append(pending, items...)
	}
	return classifyDecisions(pending), nil
}

// classifyDecisions is the decisions part's rule: one note for each
// decision in pending, and no line for any other state.
func classifyDecisions(items []machine.Item) Reading {
	var r Reading
	for _, it := range items {
		if it.Kind != machine.KindDecision || it.Status != machine.DecisionPending {
			continue
		}
		by := "no by date, so only a wake reads the trigger"
		if d := strings.TrimSpace(it.Fact(inbox.FactBy)); d != "" {
			by = "by " + d + ", when decision-timers fires it"
		}
		r.Notes = append(r.Notes, fmt.Sprintf("decision %s waits on its trigger %q; %s. If the event happened, run `loop decision fire %s`",
			it.ID, it.Fact(inbox.FactTrigger), by, it.ID))
	}
	return r
}
