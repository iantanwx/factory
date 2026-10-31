package room

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// sessionPrefix starts the run id of each session of room id.
func sessionPrefix(id string) string { return id + "-s" }

// Reads reports whether the room session run may read item, which it does
// not hold (factorio-eki2). Its room's reason names item, or a session of
// the room created item: the item's first move ran under that session, as
// prd submit's does. A write never asks (KTD12).
func Reads(ctx context.Context, st *store.Store, run, project, item string) (bool, error) {
	if run == "" || item == "" {
		return false, nil
	}
	var ok bool
	err := st.Read(ctx, func(tx *sql.Tx) error {
		var id, reason string
		err := tx.QueryRowContext(ctx, `SELECT id, reason FROM rooms WHERE run_id = ? AND project = ?`, run, project).
			Scan(&id, &reason)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if names(reason, item) {
			ok = true
			return nil
		}
		prefix := sessionPrefix(id)
		return tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM transitions
			WHERE project = ? AND item = ? AND from_status = ? AND substr(run_id, 1, ?) = ?)`,
			project, item, machine.Initial, len(prefix), prefix).Scan(&ok)
	})
	return ok, err
}

// names reports whether text names item as a whole id: tk-1 does not name
// tk-10, and epic-a does not name its child epic-a.1.
func names(text, item string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], item)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(item)
		if (start == 0 || !idChar(text[start-1])) && (end == len(text) || !continuesID(text[end:])) {
			return true
		}
		i = start + 1
	}
}

// idChar reports whether c can be part of an item id.
func idChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}

// continuesID reports whether rest, the text after a match, carries the
// id on: an id character, except a dot that ends a sentence.
func continuesID(rest string) bool {
	if rest[0] != '.' {
		return idChar(rest[0])
	}
	return len(rest) > 1 && idChar(rest[1]) && rest[1] != '.'
}
