// Package retro holds the retro stage: the queue of finished epics and
// closed bugs that a retro run learns from (factorio-1xq0).
package retro

import (
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// StatePending is a retro that waits for a run.
const StatePending = "pending"

// Enqueue queues one retro when ev lands an epic in done or closes a bug.
// A shaped bug closes when its ticket reaches done, so that ticket's event
// queues the retro of its bug. Every other event, and a second insert for
// the same item, does nothing. The machine calls it in the transaction of
// the terminal write, so the row and the move commit together.
func Enqueue(tx *store.Tx, ev machine.TerminalEvent) error {
	item, kind := ev.Item, ev.Kind
	switch {
	case ev.Kind == machine.KindEpic && ev.Status == machine.Done:
	case ev.Kind == machine.KindBug && machine.BugClosed(ev.Status, ""):
	// Only work shape makes a ticket with a bug fact, and it shapes the
	// bug in the same call.
	case ev.Kind == machine.KindTicket && ev.Bug != "" && machine.BugClosed(machine.Shaped, ev.Status):
		item, kind = ev.Bug, machine.KindBug
	default:
		return nil
	}
	_, err := tx.Exec(`INSERT INTO retros (project, item, kind, state, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (project, item) DO NOTHING`, ev.Project, item, string(kind), StatePending, tx.Now().UnixMilli())
	return err
}
