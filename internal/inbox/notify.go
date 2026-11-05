package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/store"
)

// NotifyKind is the schedule row that held the one cursor of an earlier
// release. A destination with no HomeNotifyKind row starts at its value,
// so the upgrade rings for no entry twice.
const NotifyKind = "inbox_notify"

// HomeNotifyKind is the schedule row that holds the cursor of one
// destination project, keyed by the project, so a restart rings for no
// entry twice (spec factorio-1edo, Data).
const HomeNotifyKind = "home_notify"

// ErrNobodyToRing is a Doorbell's answer when no coordinator session runs.
// The cursor stays, so the first ring after the session starts names every
// entry it missed.
var ErrNobodyToRing = errors.New("inbox: no coordinator session to ring")

// ErrRingHeld is a Doorbell's answer when it rang no one because the
// session refused ring after ring and the backoff holds it (F-32). The
// cursor stays, as with ErrNobodyToRing.
var ErrRingHeld = errors.New("inbox: the coordinator ring is held after repeated refusals")

// Doorbell types one line into the coordinator's session. The scheduler's
// agent host does it (agenthost.Host.Notify).
type Doorbell func(ctx context.Context, text string) error

// SessionBell types one line into the session id for the entries of the
// destinations dests. The server records the ring of each destination.
type SessionBell func(ctx context.Context, id agenthost.RunID, dests []string, text string) error

// Notifier rings the coordinator for each new entry (KTD21). It is a
// best-effort doorbell: the inbox stays pull-durable, and a ring that
// fails leaves the cursor, so the next ring names the entry again. The
// text names each entry's kind, id, awaits and title, and never carries
// agent text.
//
// The topology and the sessions are the inbox's own, so a ring names the
// entries that `inbox list` shows the session it rings (C10).
type Notifier struct {
	Inbox *Inbox
	Ring  SessionBell

	mu sync.Mutex
}

// Poke rings each session once, for the entries newer than the cursor of
// each destination that the session serves. It moves the cursors of each
// accepted ring. A destination with no session keeps its cursor, and so
// does each destination of a ring that is held, refused or finds nobody.
// When a cursor row changed since the poke read it, the poke reads the
// rows again and rings for that destination only.
func (n *Notifier) Poke(ctx context.Context) error {
	if n == nil || n.Ring == nil || n.Inbox.cfg.Sessions == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	var errs []error
	var only map[string]bool
	for {
		c, err := n.cursors(ctx)
		if err != nil {
			return err
		}
		es, err := n.Inbox.Entries(ctx)
		if err != nil {
			return err
		}
		changed := map[string]bool{}
		for _, r := range n.group(es, c, only) {
			err := n.Ring(ctx, r.id, slices.Sorted(maps.Keys(r.last)), RingText(r.es))
			if errors.Is(err, ErrNobodyToRing) || errors.Is(err, ErrRingHeld) {
				continue
			}
			if err == nil {
				err = n.move(ctx, c, r.last, changed)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("inbox: ring the coordinator session %s: %w", r.id, err))
			}
		}
		if len(changed) == 0 {
			return errors.Join(errs...)
		}
		only = changed
	}
}

// cursors are the cursor rows a poke read.
type cursors struct {
	old  sql.NullInt64    // the NotifyKind value
	rows map[string]int64 // the HomeNotifyKind row of each destination
}

// at is the cursor of dest: its own row, then the old cursor. With
// neither, every entry is after it.
func (c cursors) at(dest string) int64 {
	if at, ok := c.rows[dest]; ok {
		return at
	}
	if c.old.Valid {
		return c.old.Int64
	}
	return -1
}

func (n *Notifier) cursors(ctx context.Context) (cursors, error) {
	c := cursors{rows: map[string]int64{}}
	err := n.Inbox.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT due_at FROM schedule WHERE kind = ?`, NotifyKind).Scan(&c.old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rs, err := tx.QueryContext(ctx, `SELECT project, due_at FROM schedule WHERE kind = ?`, HomeNotifyKind)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var dest string
			var at int64
			if err := rs.Scan(&dest, &at); err != nil {
				return err
			}
			c.rows[dest] = at
		}
		return rs.Err()
	})
	return c, err
}

// sessionRing is the one ring of a poke to one session.
type sessionRing struct {
	id   agenthost.RunID
	es   []Entry
	last map[string]int64 // the newest entry of each destination
}

// group sorts the entries after each destination's cursor by the session
// that serves the destination, in the order of each session's first entry.
// A non-nil only keeps the destinations it holds.
func (n *Notifier) group(es []Entry, c cursors, only map[string]bool) []*sessionRing {
	var t coord.Topology
	if n.Inbox.cfg.Topology != nil {
		t = n.Inbox.cfg.Topology()
	}
	var out []*sessionRing
	by := map[agenthost.RunID]*sessionRing{}
	for _, e := range es {
		dest := Route(e, t)
		if e.At <= c.at(dest) || only != nil && !only[dest] {
			continue
		}
		id, ok := n.Inbox.cfg.Sessions(dest)
		if !ok {
			continue
		}
		r := by[id]
		if r == nil {
			r = &sessionRing{id: id, last: map[string]int64{}}
			by[id] = r
			out = append(out, r)
		}
		r.es = append(r.es, e)
		r.last[dest] = max(r.last[dest], e.At)
	}
	return out
}

// move moves the cursor of each destination to its newest rung entry,
// only from the value the poke read. A row that changed since that read
// stays as it is, and move adds its destination to changed.
func (n *Notifier) move(ctx context.Context, c cursors, to map[string]int64, changed map[string]bool) error {
	return n.Inbox.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		for dest, at := range to {
			var res sql.Result
			var err error
			if from, ok := c.rows[dest]; ok {
				res, err = tx.Exec(`UPDATE schedule SET due_at = ?, updated_at = ? WHERE kind = ? AND project = ? AND due_at = ?`,
					at, now, HomeNotifyKind, dest, from)
			} else {
				res, err = tx.Exec(`INSERT INTO schedule (project, kind, due_at, created_at, updated_at) SELECT ?, ?, ?, ?, ?
					WHERE NOT EXISTS (SELECT 1 FROM schedule WHERE kind = ? AND project = ?)`,
					dest, HomeNotifyKind, at, now, now, HomeNotifyKind, dest)
			}
			if err != nil {
				return err
			}
			if moved, err := res.RowsAffected(); err != nil {
				return err
			} else if moved == 0 {
				changed[dest] = true
			}
		}
		return nil
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
