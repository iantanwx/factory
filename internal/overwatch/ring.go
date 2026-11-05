package overwatch

import (
	"context"
	"database/sql"
	"encoding/json"
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

// RingKind is the schedule row that holds the outcome of the last ring to
// one destination project, keyed by the project (spec factorio-1edo,
// Data). The row of "" holds the ring of the destination "".
const RingKind = "coordinator_ring"

// RefusalLimit is how many refused rings in a row to one stored
// coordinator session raise AlertRingRefused and start the backoff (F-32).
const RefusalLimit = 3

// The backoff after RefusalLimit refusals in a row: HoldBase after the
// RefusalLimit-th, doubled for each refusal after it, and at most HoldMax.
const (
	HoldBase = time.Minute
	HoldMax  = 30 * time.Minute
)

// AlertRingRefused names one stored coordinator session that refused
// RefusalLimit or more rings in a row. Its dedupe key is RingAlertKey of
// the session's handle, and an accepted ring closes it.
const AlertRingRefused = "coordinator_ring_refused"

// RingAlertKey is the dedupe key of the AlertRingRefused alert of handle.
func RingAlertKey(handle string) string { return AlertRingRefused + ":" + handle }

// RingOutcome is the outcome of the last ring that the loop sent. Only a
// refused ring carries Detail, the refusal's text, and Refusals.
type RingOutcome struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"` // SourceInbox or SourceOverwatch
	Result string    `json:"result"` // RingAccepted, RingRefused or RingNobody
	Detail string    `json:"detail,omitempty"`
	// Handle is the stored coordinator session the ring went to, empty for
	// RingNobody.
	Handle string `json:"handle,omitempty"`
	// Refusals counts the refused rings in a row to Handle, this one
	// included.
	Refusals int `json:"refusals,omitempty"`
	// Alerted is the handle whose AlertRingRefused alert is open. Each row
	// carries it until an accepted ring closes the alert.
	Alerted string `json:"alerted,omitempty"`
}

// Hold is how long the backoff holds the next ring to Handle after this
// one. It is zero below RefusalLimit refusals.
func (o RingOutcome) Hold() time.Duration {
	if o.Result != RingRefused || o.Refusals < RefusalLimit {
		return 0
	}
	d := HoldBase
	for i := RefusalLimit; i < o.Refusals && d < HoldMax; i++ {
		d *= 2
	}
	return min(d, HoldMax)
}

// Held reports whether the backoff of this ring holds a ring to handle at
// now. Another handle, a stored session that changed, is never held.
func (o RingOutcome) Held(handle string, now time.Time) bool {
	hold := o.Hold()
	return hold > 0 && handle != "" && o.Handle == handle && now.Before(o.At.Add(hold))
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
		if o.Refusals != 0 {
			return fmt.Errorf("overwatch: a ring %s counts no refusals", o.Result)
		}
	case RingRefused:
	default:
		return fmt.Errorf("overwatch: ring result %q is none of accepted, refused and nobody", o.Result)
	}
	return nil
}

// RecordRing writes o into the coordinator_ring row of each destination
// of dests. It writes each row anew, so Rings reads the rows in the order
// of their writes.
func RecordRing(ctx context.Context, st *store.Store, dests []string, o RingOutcome) error {
	if err := o.check(); err != nil {
		return err
	}
	payload, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return st.Write(ctx, func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		for _, dest := range dests {
			if _, err := tx.Exec(`DELETE FROM schedule WHERE kind = ? AND project = ?`, RingKind, dest); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO schedule (project, kind, payload, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
				dest, RingKind, string(payload), now, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// DestRing is the last ring to one destination.
type DestRing struct {
	Dest    string
	Outcome RingOutcome
}

// Rings reads every coordinator_ring row, the oldest write first.
func Rings(ctx context.Context, st *store.Store) ([]DestRing, error) {
	var out []DestRing
	err := st.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT project, payload FROM schedule WHERE kind = ? ORDER BY id`, RingKind)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var r DestRing
			var raw string
			if err := rs.Scan(&r.Dest, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(raw), &r.Outcome); err != nil {
				return fmt.Errorf("overwatch: the %s row of %q: %w", RingKind, r.Dest, err)
			}
			out = append(out, r)
		}
		return rs.Err()
	})
	return out, err
}

// LastRings reads the last ring to each destination. On a host with the
// loop project loop, no entry routes to "", so the row of "" is an earlier
// release's one ring record, and LastRings leaves it out.
func LastRings(ctx context.Context, st *store.Store, loop string) (map[string]RingOutcome, error) {
	rs, err := Rings(ctx, st)
	if err != nil {
		return nil, err
	}
	out := map[string]RingOutcome{}
	for _, r := range rs {
		if r.Dest != "" || loop == "" {
			out[r.Dest] = r.Outcome
		}
	}
	return out, nil
}
