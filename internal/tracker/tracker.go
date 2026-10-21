// Package tracker projects the loop's record of work onto an outside issue
// tracker (R28, R29, R30, KTD25).
//
// A tracker is optional for each project. A project with no adapter runs on
// beads alone. An Adapter has exactly two operations: Apply takes a batch of
// loop events from the beads journal, and Reconcile takes the current state
// of every item. Both must be idempotent, because delivery is at least once:
// the server replays a batch whose checkpoint it did not record.
//
// The server owns delivery (Delivery in delivery.go). It reads the beads
// events journal from one checkpoint for each adapter, and it advances the
// checkpoint only after Apply succeeds. v1 ships the interface and an
// in-memory fake (package fake), and no real tracker.
package tracker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
)

// Adapter projects loop events onto one tracker. Implementations must be
// idempotent and safe for concurrent use.
//
// A nil error means the tracker holds the result. Any other result is
// unknown: the server keeps the checkpoint and delivers the same events
// again. An adapter that the tracker throttles returns a RateLimitError, or
// an error that wraps ErrRateLimited.
type Adapter interface {
	// Apply applies one batch of events of one project, in batch order. The
	// batch is in journal order (Seq strictly increasing), so the events of
	// one item arrive in the order beads committed them. A replayed batch
	// can hold events the adapter already applied.
	Apply(ctx context.Context, project string, batch []Event) error
	// Reconcile makes the tracker match the current state of every item of
	// one project. The server calls it when the adapter's checkpoint fell
	// below the journal's retention, so some events are lost.
	Reconcile(ctx context.Context, project string, items []Item) error
}

// Event is one record of the beads events journal, as an adapter sees it.
type Event struct {
	// Seq is the journal sequence. It is gapless and strictly increasing in
	// commit order inside one project's workspace, and never reused.
	Seq   int64
	At    time.Time
	Op    string // create, update, close, delete, dep_add, dep_remove, comment
	Item  string // the beads issue id
	Actor string
	// State is the item after the mutation. It is nil on delete.
	State *Item
}

// Item is the state of one beads issue.
type Item struct {
	ID          string
	Title       string
	Description string
	Status      string
	Type        string
	Priority    int
	Assignee    string
	Metadata    map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ItemFromIssue converts a beads issue. The metadata map is copied.
func ItemFromIssue(is beadsx.Issue) Item {
	md := maps.Clone(is.Metadata)
	if md == nil {
		md = map[string]string{}
	}
	return Item{
		ID: is.ID, Title: is.Title, Description: is.Description, Status: is.Status,
		Type: is.Type, Priority: is.Priority, Assignee: is.Assignee, Metadata: md,
		CreatedAt: is.CreatedAt, UpdatedAt: is.UpdatedAt,
	}
}

// EventFromJournal converts one beads journal record.
func EventFromJournal(e beadsx.Event) Event {
	out := Event{Seq: e.Seq, At: e.TS, Op: e.Op, Item: e.IssueID, Actor: e.Actor}
	if e.Issue != nil {
		it := ItemFromIssue(*e.Issue)
		out.State = &it
	}
	return out
}

// Error kinds. Test them with errors.Is.
var (
	// ErrRateLimited means the tracker throttled the adapter. The server
	// starts the adapter's shared cooldown and keeps the checkpoint.
	ErrRateLimited = errors.New("tracker: adapter rate limited")
	// ErrCoolingDown means the server made no call, because the adapter's
	// shared cooldown is still running. The checkpoint did not move.
	ErrCoolingDown = errors.New("tracker: adapter cooling down")
	// ErrUnknownProject means no project with the id is registered.
	ErrUnknownProject = errors.New("tracker: unknown project")
	// ErrJournalOrder means the journal returned records that are not in
	// strictly increasing seq order above the checkpoint. Nothing is applied.
	ErrJournalOrder = errors.New("tracker: journal records out of order")
)

// RateLimitError is a throttled call. RetryAfter is how long the tracker
// asked the adapter to wait. Zero means the tracker gave no time, and the
// server uses its default cooldown.
type RateLimitError struct {
	RetryAfter time.Duration
	Err        error // the tracker's own error, or nil
}

// RateLimited returns a RateLimitError that asks for a wait of after.
func RateLimited(after time.Duration) error { return &RateLimitError{RetryAfter: after} }

func (e *RateLimitError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%v: retry after %s: %v", ErrRateLimited, e.RetryAfter, e.Err)
	}
	return fmt.Sprintf("%v: retry after %s", ErrRateLimited, e.RetryAfter)
}

// Is makes errors.Is(err, ErrRateLimited) true.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// Unwrap returns the tracker's own error.
func (e *RateLimitError) Unwrap() error { return e.Err }
