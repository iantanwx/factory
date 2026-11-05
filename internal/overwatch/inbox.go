package overwatch

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/sched"
)

// inboxPart is the part inbox (C18): whether a ring reaches the
// coordinator. No live coordinator session in the agent host inventory is
// work, and so is a coordinator_ring row that holds refused. A live
// session and an accepted ring read code 0. No command repeats the read.
func inboxPart() Part {
	return Part{Name: "inbox", Read: readInbox}
}

// readInbox reads the coordinator the scheduler holds, the inventory when
// it holds a session, and the ring record.
func readInbox(ctx context.Context, w World) (Reading, error) {
	cs := w.Coordinator()
	var inv agenthost.Inventory
	if cs.Session != nil {
		var err error
		if inv, err = w.Inventory(ctx); err != nil {
			return Reading{}, err
		}
	}
	rings, err := w.LastRings(ctx)
	if err != nil {
		return Reading{}, err
	}
	return classifyInbox(cs, inv, rings), nil
}

// classifyInbox is the inbox part's rule. A session is live when the
// inventory lists its exact identity as connected. Each destination's last
// ring that was refused is work.
func classifyInbox(cs sched.CoordinatorState, inv agenthost.Inventory, rings map[string]RingOutcome) Reading {
	var r Reading
	noSession := Key("inbox", "no-session")
	switch {
	case cs.Session == nil:
		r.Findings = append(r.Findings, Finding{Key: noSession,
			Line: fmt.Sprintf("the scheduler holds no coordinator session, so no ring reaches the coordinator (%s)", noSession)})
	case !connected(inv, cs.Session.ID):
		r.Findings = append(r.Findings, Finding{Key: noSession,
			Line: fmt.Sprintf("the agent host lists no live session %s for the coordinator run %s (%s)", cs.Session.ID, cs.RunID, noSession)})
	default:
		r.Notes = append(r.Notes, fmt.Sprintf("the coordinator session %s of run %s runs", cs.Session.ID, cs.RunID))
	}
	if len(rings) == 0 {
		r.Notes = append(r.Notes, "the store holds no coordinator ring yet")
		return r
	}
	for _, dest := range slices.Sorted(maps.Keys(rings)) {
		ring := rings[dest]
		last, refused := "the last ring", Key("inbox", "refused")
		if dest != "" {
			last, refused = "the last ring for "+dest, Key("inbox", "refused", dest)
		}
		last += fmt.Sprintf(", from %s at %s,", ring.Source, ring.At.UTC().Format(time.RFC3339))
		switch ring.Result {
		case RingRefused:
			r.Findings = append(r.Findings, Finding{Key: refused, Line: fmt.Sprintf("%s was refused: %s (%s)", last, ring.Detail, refused)})
		case RingNobody:
			r.Notes = append(r.Notes, last+" found no coordinator session")
		default:
			r.Notes = append(r.Notes, last+" was "+ring.Result)
		}
	}
	return r
}

// connected reports whether the inventory lists id as a connected session.
func connected(inv agenthost.Inventory, id agenthost.RunID) bool {
	for _, row := range inv.Rows {
		if row.ID == id && row.Connected {
			return true
		}
	}
	return false
}
