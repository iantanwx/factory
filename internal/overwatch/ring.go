package overwatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/store"
)

// RingText is the one line a ring types: fixed server text that names each
// part with work and each part with a failed read, in PartNames order, and
// asks for the sweep. It names no item, no entry, no key and no line of a
// part (KTD21). A failed records read adds records to the unread list. ok
// is false for a report with code 0.
func RingText(r Report) (text string, ok bool) {
	if r.Code == Quiet {
		return "", false
	}
	var work, unread []string
	for _, p := range r.Parts {
		switch p.Code {
		case Work:
			work = append(work, p.Name)
		case Unread:
			unread = append(unread, p.Name)
		}
	}
	if r.Records != "" {
		unread = append(unread, "records")
	}
	var lists []string
	if len(work) > 0 {
		lists = append(lists, "work in "+strings.Join(work, ", "))
	}
	if len(unread) > 0 {
		lists = append(lists, "unread "+strings.Join(unread, ", "))
	}
	return "loop overwatch: " + strings.Join(lists, "; ") + ". Run `loop overwatch sweep`, then follow section 1 of your runbook.", true
}

// The two doorbells that write the ring record. `loop coordinator send`
// writes none.
const (
	SourceInbox     = "inbox"
	SourceOverwatch = "overwatch"
)

// The results of a ring.
const (
	RingAccepted = "accepted"
	RingRefused  = "refused"
	RingNobody   = "nobody"
)

// RingKind is the one schedule row that holds the last ring's outcome.
const RingKind = "coordinator_ring"

// RingOutcome is the outcome of the last ring that the loop sent. Only a
// refused ring carries Detail, the refusal's text.
type RingOutcome struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"` // SourceInbox or SourceOverwatch
	Result string    `json:"result"` // RingAccepted, RingRefused or RingNobody
	Detail string    `json:"detail,omitempty"`
}

func (o RingOutcome) check() error {
	if o.Source != SourceInbox && o.Source != SourceOverwatch {
		return fmt.Errorf("overwatch: ring source %q is neither %s nor %s", o.Source, SourceInbox, SourceOverwatch)
	}
	switch o.Result {
	case RingAccepted, RingNobody:
		if o.Detail != "" {
			return fmt.Errorf("overwatch: a ring %s carries no text", o.Result)
		}
	case RingRefused:
	default:
		return fmt.Errorf("overwatch: ring result %q is none of accepted, refused and nobody", o.Result)
	}
	return nil
}

// RecordRing writes the one coordinator_ring row.
func RecordRing(ctx context.Context, st *store.Store, o RingOutcome) error {
	if err := o.check(); err != nil {
		return err
	}
	payload, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return st.Write(ctx, func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		r, err := tx.Exec(`UPDATE schedule SET payload = ?, updated_at = ? WHERE kind = ? AND project = ''`, string(payload), now, RingKind)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			return nil
		}
		_, err = tx.Exec(`INSERT INTO schedule (project, kind, payload, created_at, updated_at) VALUES ('', ?, ?, ?, ?)`,
			RingKind, string(payload), now, now)
		return err
	})
}

// LastRing reads the coordinator_ring row. ok is false when no ring was
// recorded yet.
func LastRing(ctx context.Context, st *store.Store) (RingOutcome, bool, error) {
	var o RingOutcome
	var raw string
	err := st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT payload FROM schedule WHERE kind = ? AND project = ''`, RingKind).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	if err != nil {
		return o, false, err
	}
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		return o, false, fmt.Errorf("overwatch: the %s row: %w", RingKind, err)
	}
	return o, true, nil
}
