package machine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// factPrefix starts every metadata key the machine owns.
const factPrefix = "loop."

// BeadsResolver returns the beads client of one project's workspace.
type BeadsResolver func(project string) (beadsx.Client, error)

// ClaimGuard is the admission hook. The machine calls it inside the
// item's lock, after the table allows the claim and before the intent is
// recorded. A non-nil refusal stops the claim and writes nothing. It must
// make no outside read: admission reads its snapshot before the lock and
// only revalidates here.
type ClaimGuard interface {
	AdmitClaim(ctx context.Context, c ClaimCheck) *cli.Refusal
}

// ClaimCheck is what a ClaimGuard decides on.
type ClaimCheck struct {
	Project string
	Item    string
	Kind    Kind
	Status  string
	Stage   string
	RunID   string
	Facts   map[string]string
}

// Config configures a Machine.
type Config struct {
	Store *store.Store
	Beads BeadsResolver
	// Redactor refuses a beads write whose facts hold a live secret
	// (KTD27). An empty redactor when nil.
	Redactor *secrets.Redactor
	// Claims is the admission hook. Every claim the table allows passes
	// when nil.
	Claims ClaimGuard
	// Terminal runs inside the transaction that marks a write applied,
	// when the write lands its item in a terminal state. An error rolls
	// the mark back, so a retry of the intent runs it again. Nothing runs
	// when nil.
	Terminal func(tx *store.Tx, ev TerminalEvent) error
	// Registry seals the answers the machine stores under an intent.
	// cli.NewRegistry() when nil; it must know every refusal code the
	// machine answers.
	Registry *cli.Registry
	Logger   *slog.Logger
}

// TerminalEvent is a write that landed its item in a terminal state.
type TerminalEvent struct {
	Project, Item string
	Kind          Kind
	Status        string
	// Bug is the bug a shaped ticket came from, or empty. The hook runs in
	// a SQLite transaction and cannot read the bead, so the intent carries
	// it.
	Bug string
}

// Machine applies intent verbs to work items.
type Machine struct {
	cfg Config
	log *slog.Logger

	mu    sync.Mutex
	locks map[string]*itemLock

	// hook lets a test stop the write at a kill point. It returns true to
	// stop, as a crash would.
	hook func(point string) bool
	// reviewComment posts the body of `review submit`. Nil posts nothing.
	reviewComment ReviewCommenter
	// prHead reads the live head of an item's pull request, which
	// `review submit --head` must name. Nil reads nothing.
	prHead PRHeadReader
	// passGate reads CI at the head a code review passes. Nil reads
	// nothing: a server with no GitHub has no CI and posts no status.
	passGate PassGate
	// supersedeCheck reads the evidence of `work supersede` back from
	// GitHub. Nil refuses every supersede.
	supersedeCheck SupersedeChecker
	// wait reads why an item waits, for work show. Nil reads nothing.
	wait WaitReader
}

type itemLock struct {
	mu   sync.Mutex
	refs int
}

// Kill points of the write order (KTD6). A test stops a write at one.
const (
	pointRecorded = "recorded" // after the intent commit
	pointWritten  = "written"  // after the beads write
	pointMarked   = "marked"   // after the applied mark
	// pointChildTerminal is after a child ticket's terminal write and
	// before its epic's complete write: the two are separate writes.
	pointChildTerminal = "child_terminal"
)

// errStopped is what a write stopped at a kill point returns.
var errStopped = errors.New("machine: stopped at a kill point")

// New returns a machine.
func New(cfg Config) (*Machine, error) {
	if cfg.Store == nil || cfg.Beads == nil {
		return nil, errors.New("machine: a machine needs a store and a beads resolver")
	}
	if cfg.Redactor == nil {
		cfg.Redactor = secrets.NewRedactor()
	}
	if cfg.Registry == nil {
		cfg.Registry = cli.NewRegistry()
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Machine{cfg: cfg, log: log, locks: map[string]*itemLock{}}, nil
}

// lock holds the item's lock until the returned func runs (KTD6): an item
// has at most one write in flight.
func (m *Machine) lock(project, item string) func() {
	k := project + "\x00" + item
	m.mu.Lock()
	l, ok := m.locks[k]
	if !ok {
		l = &itemLock{}
		m.locks[k] = l
	}
	l.refs++
	m.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		m.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(m.locks, k)
		}
		m.mu.Unlock()
	}
}

func (m *Machine) stop(point string) bool { return m.hook != nil && m.hook(point) }

// Actor is who asks for a write. History records Name.
type Actor struct {
	Name  string // for example "run:r-1", "coordinator:c-1" or "server:reactor"
	RunID string
	// Fenced makes the write check Epoch against the item's lease epoch:
	// a stale epoch is LEASE_NOT_HELD. A run's and a room's write on the
	// item it holds is fenced. The server's own writes are not.
	Fenced bool
	Epoch  int64
}

// Server is the actor of a write the server makes on its own.
func Server(component string) Actor { return Actor{Name: "server:" + component} }

// Key links a write to the caller's idempotency key (KTD13), so the answer
// that settles the intent is the answer a retry replays.
type Key struct {
	Key         string
	Fingerprint store.Fingerprint
}

// Request is one intent verb on one item.
type Request struct {
	Project string
	Item    string
	Verb    string
	// Params are the verb's inputs, for example "head", "body" or "stage".
	Params map[string]string
	Actor  Actor
	// ExpectedStatus is the status the caller read. A different current
	// status is STATUS_CHANGED. Empty means the current status.
	ExpectedStatus string
	Key            *Key
	// Command is the CLI verb that asked. A row whose Command differs
	// refuses. Empty means the server asks.
	Command   string
	RequestID string
	// Before runs once every check of the write passed, under the item's
	// lock and before the intent is recorded. It reads the item as the
	// write found it. A refusal stops the write. A replay of a recorded
	// intent never runs it. review submit reads CI and posts its comment
	// here, so a pass at red CI, or a comment GitHub refuses, records no
	// verdict.
	Before func(ctx context.Context, it Item) *cli.Refusal
}

// Item is one work item as the machine reads it: beads holds the status
// and the facts, and SQLite holds the lease owner.
type Item struct {
	Project string `json:"project"`
	ID      string `json:"item"`
	Kind    Kind   `json:"kind"`
	Title   string `json:"title"`
	// Body is the item's task text, as TaskText reads it. work show
	// answers it; List leaves it empty.
	Body   string            `json:"body,omitempty"`
	Status string            `json:"status"`
	Seq    int64             `json:"seq"`
	Epoch  int64             `json:"epoch"`
	Owner  string            `json:"owner"`
	Facts  map[string]string `json:"facts"`
	// AllowedNext names the verbs the table allows from Status.
	AllowedNext []string `json:"allowed_next"`
	// Updated is when beads last wrote the item. The work query dates an
	// item's entry into Status by it when no transition records that entry.
	Updated time.Time `json:"-"`
	// Wait is why the item waits, and WaitError why that read failed. Only
	// work show sets them.
	Wait      *Wait  `json:"wait,omitempty"`
	WaitError string `json:"wait_error,omitempty"`
}

// Wait is the wait of one item: the lease, the holds, the counted failures
// with the limit, and the open alerts that name the item.
type Wait struct {
	Lease    *WaitLease    `json:"lease"`    // nil when no run holds the item
	Holds    []WaitHold    `json:"holds"`    // [] when none
	Failures []WaitFailure `json:"failures"` // counted rows only, oldest first
	Limit    int           `json:"failure_limit"`
	Alerts   []WaitAlert   `json:"alerts"` // open alerts that name the item
}

// WaitLease is the run that holds the item's lease.
type WaitLease struct {
	Run     string    `json:"run"`
	Epoch   int64     `json:"epoch"`
	Granted time.Time `json:"granted_at"`
	Renewed time.Time `json:"renewed_at"`
}

// WaitHold is a room or an escalation that holds the item.
type WaitHold struct {
	Holder string    `json:"holder"`
	Kind   string    `json:"kind"`
	Since  time.Time `json:"since"`
}

// WaitFailure is one counted failure of the item.
type WaitFailure struct {
	Run    string    `json:"run"`
	Stage  string    `json:"stage"`
	Class  string    `json:"class"`
	Inputs string    `json:"inputs"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"recorded_at"`
}

// WaitAlert is one open alert that names the item.
type WaitAlert struct {
	Entry   string `json:"entry"` // alert/<id>, as the inbox names it
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
}

// Fact returns one fact by name.
func (i Item) Fact(name string) string { return i.Facts[name] }

// view reads an item from its bead. It does not read the lease.
func view(project string, is beadsx.Issue) (Item, *Table, error) {
	t, ok := kindOfType(is.Type)
	if !ok {
		return Item{}, nil, fmt.Errorf("machine: %s has bead type %q, which no kind uses", is.ID, is.Type)
	}
	seq, err := is.IntentSeq()
	if err != nil {
		return Item{}, nil, fmt.Errorf("machine: %s: %s: %w", is.ID, beadsx.MetaIntentSeq, err)
	}
	epoch, err := beadEpoch(is)
	if err != nil {
		return Item{}, nil, err
	}
	title, body := TaskText(is)
	it := Item{
		Project: project, ID: is.ID, Kind: t.Kind, Title: title, Body: body, Status: is.Status,
		Seq: seq, Epoch: epoch, Facts: map[string]string{}, AllowedNext: t.AllowedNext(is.Status),
		Updated: is.UpdatedAt,
	}
	for k, v := range is.Metadata {
		switch k {
		case beadsx.MetaIntentSeq, beadsx.MetaIdemKey, beadsx.MetaEpoch:
			continue
		}
		if name, ok := strings.CutPrefix(k, factPrefix); ok {
			it.Facts[name] = v
		}
	}
	return it, t, nil
}

func beadEpoch(is beadsx.Issue) (int64, error) {
	v, ok := is.Metadata[beadsx.MetaEpoch]
	if !ok {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("machine: %s: %s: %w", is.ID, beadsx.MetaEpoch, err)
	}
	return n, nil
}

// fence is the bead assignee that carries the lease epoch. bd 1.3.1 guards
// no metadata value, so the assignee guard is the only way beads checks
// the epoch inside the conditional write (KTD6 step 2). Epoch 0 is the
// empty assignee of a new bead.
func fence(epoch int64) string {
	if epoch == 0 {
		return ""
	}
	return "loop:e" + strconv.FormatInt(epoch, 10)
}

// change is one write in the making.
type change struct {
	ctx   context.Context
	m     *Machine
	req   *Request
	table *Table
	row   *Row
	item  Item
	to    string
	set   map[string]string
	unset []string
	epoch int64   // the epoch after the write
	owner *string // the lease owner after the write; nil keeps it
}

func (c *change) param(name string) string {
	if c.req == nil {
		return ""
	}
	return c.req.Params[name]
}

func (c *change) fact(name string) string { return c.item.Facts[name] }

func (c *change) setFact(name, value string) {
	c.set[factPrefix+name] = value
}

func (c *change) unsetFact(names ...string) {
	for _, n := range names {
		if _, ok := c.item.Facts[n]; ok {
			c.unset = append(c.unset, factPrefix+n)
		}
	}
}

// needParam refuses a write that lacks a required parameter.
func (c *change) needParam(name string) *cli.Refusal {
	if c.param(name) != "" {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeUsage, Rule: "missing_" + name, Item: c.item.ID,
		Hint: fmt.Sprintf("%s on a %s needs %s", c.req.Verb, c.item.Kind, name)}
}

// facts is the set and unset maps as stored on the intent.
type facts struct {
	Set     map[string]string `json:"set,omitempty"`
	Unset   []string          `json:"unset,omitempty"`
	Epoch   int64             `json:"epoch"`
	Owner   *string           `json:"owner,omitempty"`
	Kind    Kind              `json:"kind"`
	Command string            `json:"command,omitempty"`
	Request string            `json:"request_id,omitempty"`
	Says    string            `json:"says,omitempty"`
	// Bug is the item's bug fact: the bug a shaped ticket came from.
	Bug string `json:"bug,omitempty"`
	// Fence is the bead assignee the write expects: the epoch fence.
	Fence string `json:"fence"`
}

func cloneSet(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return maps.Clone(m)
}
