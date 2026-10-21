// Package beadsx is the one adapter between the loop server and beads.
//
// The server reads and writes beads only through Client. Two implementations
// exist: BD runs the pinned `bd` binary against a supervised Dolt sql-server,
// and the fake package keeps the same semantics in memory for the tests of
// every other unit. The contract suite runs against both.
//
// Metadata is a flat map of strings. BD writes it as a JSON object through
// `bd update --metadata`, which merges keys and keeps string values exact.
// `--set-metadata` is never used, because it turns "1" into the JSON number 1.
// An unset writes the key as JSON null in the same call, because bd refuses
// `--metadata` together with `--unset-metadata`. Every read drops null keys,
// so an unset key reads as absent.
package beadsx

import (
	"context"
	"strconv"
	"time"
)

// Pinned versions. The real client refuses any other binary.
const (
	PinnedBD   = "1.3.1"
	PinnedDolt = "2.2.0"
)

// Metadata keys the write order of KTD6 relies on. The intent sequence only
// increases, and an intent is applied when the bead's sequence is at or above
// the intent's own.
const (
	MetaIntentSeq = "loop.seq"
	MetaIdemKey   = "loop.key"
	MetaEpoch     = "loop.epoch"
)

// Client is every beads operation the server makes.
type Client interface {
	// Version returns the version string of the beads binary.
	Version(ctx context.Context) (string, error)
	// ConfigureStatuses replaces the workspace's custom statuses.
	ConfigureStatuses(ctx context.Context, defs []StatusDef) error
	// ConfigureTypes replaces the workspace's custom issue types.
	ConfigureTypes(ctx context.Context, types []string) error
	// Create makes one issue. Its status is always "open".
	Create(ctx context.Context, spec CreateSpec) (Issue, error)
	// Get reads one issue. An unknown id returns ErrNotFound.
	Get(ctx context.Context, id string) (Issue, error)
	// List returns the issues that match every field of the filter.
	List(ctx context.Context, f Filter) ([]Issue, error)
	// Update writes one issue in one conditional write. A failed guard writes
	// nothing and returns ErrCASMiss.
	Update(ctx context.Context, id string, spec UpdateSpec) (Issue, error)
	// Events returns the journal records of one issue in seq order.
	Events(ctx context.Context, id string) ([]Event, error)
	// Tail returns the journal records with a seq above since, in seq order.
	// A limit of 0 means no limit.
	Tail(ctx context.Context, since int64, limit int) ([]Event, error)
	// AddDependency records that issue `from` depends on issue `to`.
	AddDependency(ctx context.Context, from, to string, kind DepKind) error
}

// StatusCategory is the beads category of a custom status.
type StatusCategory string

// The four categories bd 1.3.1 accepts.
const (
	CategoryActive StatusCategory = "active"
	CategoryWIP    StatusCategory = "wip"
	CategoryDone   StatusCategory = "done"
	CategoryFrozen StatusCategory = "frozen"
)

// StatusDef names one custom status.
type StatusDef struct {
	Name     string
	Category StatusCategory
}

// DepKind is a beads dependency type.
type DepKind string

// The dependency kinds the server uses.
const (
	DepBlocks      DepKind = "blocks"
	DepParentChild DepKind = "parent-child"
	DepRelated     DepKind = "related"
)

// Issue is the part of a bead the server reads.
type Issue struct {
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

// IntentSeq parses the intent sequence from metadata. A missing key is 0.
func (i Issue) IntentSeq() (int64, error) {
	v, ok := i.Metadata[MetaIntentSeq]
	if !ok {
		return 0, nil
	}
	return strconv.ParseInt(v, 10, 64)
}

// CreateSpec describes a new issue.
type CreateSpec struct {
	Title       string
	Description string
	Type        string // empty means "task"
	Priority    int    // 0 (highest) to 4
	Metadata    map[string]string
}

// Filter selects issues. Empty fields select everything.
type Filter struct {
	Statuses []string
	Type     string
	Metadata map[string]string // exact matches, all required
}

// UpdateSpec is one conditional write.
//
// IfStatus is required. IfAssignee, when set, adds a second guard on the
// assignee field, checked in the same write. bd 1.3.1 has no guard on a
// metadata value, so the assignee guard is the only second fence available.
type UpdateSpec struct {
	IfStatus      string
	IfAssignee    *string
	Status        string  // empty keeps the status
	Assignee      *string // nil keeps the assignee
	SetMetadata   map[string]string
	UnsetMetadata []string
}

// Event is one record of the beads events journal (`bd events tail`).
// Seq is gapless and strictly increasing in commit order.
type Event struct {
	Seq     int64
	TS      time.Time
	Op      string // create, update, close, delete, dep_add, dep_remove, comment
	IssueID string
	Actor   string
	Issue   *Issue // state after the mutation; nil on delete
}

// Builtin statuses of bd 1.3.1. A custom status must not reuse these names.
var builtinStatuses = map[string]StatusCategory{
	"open": CategoryActive, "in_progress": CategoryWIP, "blocked": CategoryWIP,
	"deferred": CategoryFrozen, "closed": CategoryDone, "pinned": CategoryFrozen,
	"hooked": CategoryWIP,
}

// Builtin issue types of bd 1.3.1.
var builtinTypes = map[string]bool{
	"task": true, "bug": true, "feature": true, "chore": true, "epic": true,
	"decision": true, "spike": true, "story": true, "milestone": true,
}

// IsBuiltinStatus reports whether bd defines the status itself.
func IsBuiltinStatus(s string) bool { _, ok := builtinStatuses[s]; return ok }

// IsBuiltinType reports whether bd defines the issue type itself.
func IsBuiltinType(t string) bool { return builtinTypes[t] }

// CategoryOf returns the category of a built-in status.
func CategoryOf(s string) (StatusCategory, bool) { c, ok := builtinStatuses[s]; return c, ok }
