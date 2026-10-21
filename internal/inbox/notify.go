package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/iantanwx/loop/internal/store"
)

// NotifyKind is the schedule row that holds the notifier's cursor, so a
// restart rings for no entry twice.
const NotifyKind = "inbox_notify"

// ErrNobodyToRing is a Doorbell's answer when no coordinator session runs.
// The cursor stays, so the first ring after the session starts names every
// entry it missed.
var ErrNobodyToRing = errors.New("inbox: no coordinator session to ring")

// Doorbell types one line into the coordinator's session. The scheduler's
// agent host does it (agenthost.Host.Notify).
type Doorbell func(ctx context.Context, text string) error

// Notifier rings the coordinator for each new entry (KTD21). It is a
// best-effort doorbell: the inbox stays pull-durable, and a ring that
// fails leaves the cursor, so the next ring names the entry again. The
// text names each entry's kind, id, awaits and title, and never carries
// agent text.
type Notifier struct {
	Inbox *Inbox
	Ring  Doorbell

	mu sync.Mutex
}

// Poke rings once for every entry newer than the stored cursor.
func (n *Notifier) Poke(ctx context.Context) error {
	if n == nil || n.Ring == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.Inbox.cfg.Store
	var cursor sql.NullInt64
	err := st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT due_at FROM schedule WHERE kind = ?`, NotifyKind).Scan(&cursor)
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	since := ""
	if cursor.Valid {
		since = fmt.Sprint(cursor.Int64)
	}
	es, err := n.Inbox.Since(ctx, since)
	if err != nil || len(es) == 0 {
		return err
	}
	if err := n.Ring(ctx, RingText(es)); errors.Is(err, ErrNobodyToRing) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inbox: ring the coordinator: %w", err)
	}
	last := es[len(es)-1].At
	return st.Write(ctx, func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		r, err := tx.Exec(`UPDATE schedule SET due_at = ?, updated_at = ? WHERE kind = ?`, last, now, NotifyKind)
		if err != nil {
			return err
		}
		if k, _ := r.RowsAffected(); k > 0 {
			return nil
		}
		_, err = tx.Exec(`INSERT INTO schedule (project, kind, due_at, created_at, updated_at) VALUES ('', ?, ?, ?, ?)`,
			NotifyKind, last, now, now)
		return err
	})
}

// RingText is the one line a ring types: server text only.
func RingText(es []Entry) string {
	parts := make([]string, 0, len(es))
	for _, e := range es {
		parts = append(parts, fmt.Sprintf("%s (%s, awaits %s)", e.ID, e.Kind, e.Awaits))
	}
	return fmt.Sprintf("loop inbox: %d new: %s. Run `loop inbox list` to read them.", len(es), strings.Join(parts, "; "))
}
