package room

import (
	"github.com/iantanwx/loop/internal/store"
)

// HoldKind is the holds row kind a room takes.
const HoldKind = "room"

// holderOf names the holds row of room id.
func holderOf(id string) string { return "room:" + id }

// takeHold blocks new admission on the room's item (KTD23). Admission reads
// the holds table on every admit, claim and peek. A running lease is never
// revoked: the room waits in opening until the lease is free.
func takeHold(tx *store.Tx, project, item, id string) error {
	if item == "" {
		return nil
	}
	_, err := tx.Exec(`INSERT OR IGNORE INTO holds (project, item, holder, kind, created_at) VALUES (?, ?, ?, ?, ?)`,
		project, item, holderOf(id), HoldKind, tx.Now().UnixMilli())
	return err
}

// dropHold lets the item go.
func dropHold(tx *store.Tx, project, item, id string) error {
	if item == "" {
		return nil
	}
	_, err := tx.Exec(`DELETE FROM holds WHERE project = ? AND item = ? AND holder = ?`, project, item, holderOf(id))
	return err
}
