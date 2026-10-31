// Package inbox is the coordinator's one typed inbox (KTD21). It is a
// derived view: its entries come from beads states (escalated, sign-off
// waiting, fired decisions, asked bug reports, architecture epics that
// nothing works on) and from SQLite (alerts, questions, rooms, stage
// pauses, notices, escalated retro learnings and escalations that wait for
// the fixer's panel). Each entry says
// who it waits on, a person or the coordinator, and carries a link, so a
// coordinator act never reads as a wait on the operator.
//
// Text that an agent wrote sits between `<!--sdlc:agent run=<run-id>-->` and
// `<!--/sdlc:agent-->`. AgentText escapes any `<!--sdlc:` or `<!--/sdlc:`
// inside the text, so agent text cannot end the block or forge a loop tag.
package inbox

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/retro"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// Who an entry waits on.
const (
	AwaitsPerson      = "person"
	AwaitsCoordinator = "coordinator"
)

// Entry kinds.
const (
	KindEscalation   = "escalation"    // an item in escalated
	KindPanel        = "panel"         // an escalation that waits for the fixer's panel
	KindHold         = "hold"          // a fired decision that waits on the operator
	KindSignoff      = "signoff"       // a document that waits for the operator's approval
	KindQuestion     = "question"      // a question the coordinator prepared
	KindAsk          = "ask"           // a bug report whose reproducer asks its reporter
	KindArchitecture = "architecture"  // an architecture epic with no open child and no open room
	KindRoom         = "room"          // a room that is not closed
	KindAlert        = "alert"         // an open alert
	KindFault        = "fault"         // an open or acknowledged loop fault
	KindPause        = "pause"         // a stage pause of one project or of every project
	KindNotice       = "notice"        // a change the operator reads; nothing waits on it
	KindLearning     = retro.EntryKind // a retro learning that the route sent to a person
)

// Defaults.
const (
	// WaitBudget bounds one `inbox wait` call inside the CLI's wait budget
	// of 100 seconds.
	WaitBudget = 90 * time.Second
	// waitMargin is how long before the caller's deadline a wait answers.
	waitMargin          = 3 * time.Second
	DefaultPollInterval = time.Second
)

// Entry is one inbox entry.
type Entry struct {
	ID      string `json:"entry"`
	Kind    string `json:"kind"`
	Awaits  string `json:"awaits"`
	Project string `json:"project,omitempty"`
	Item    string `json:"item,omitempty"`
	Status  string `json:"status,omitempty"`
	Title   string `json:"title"`
	// Text is the entry's body. Agent text in it sits in agent tags.
	Text string `json:"text"`
	Link string `json:"link"`
	// Head is the document PR head a sign-off approves; the server merges
	// only that head.
	Head string `json:"head,omitempty"`
	// Next names the verb that settles the entry.
	Next string `json:"next"`
	// Found marks an open alert whose root cause `inbox note` recorded.
	// Its text holds the finding, and a repeat of it rings no one.
	Found bool `json:"found,omitempty"`
	// DeferReason is the --reason of the defer that ended, when the entry
	// returned from one.
	DeferReason string `json:"defer_reason,omitempty"`
	// At is when the entry entered the inbox, in Unix milliseconds. The
	// cursor of `inbox wait --since` is the At of the last entry seen.
	At     int64  `json:"at"`
	Cursor string `json:"cursor"`
}

// Config configures an Inbox.
type Config struct {
	Store   *store.Store
	Machine *machine.Machine
	// List lists the items of one project that the item entries read.
	// Machine.List when nil, so a reader with no machine, such as the
	// overwatch sweep's test world, can build the same entries.
	List     func(ctx context.Context, project string, f machine.ListFilter) ([]machine.Item, *cli.Refusal)
	Projects func() ([]config.Project, error)
	Redactor *secrets.Redactor
	// Link returns the link of a project's item. A `loop work show` line
	// when nil. The server links an epic to its current document PR. The
	// inbox calls it outside a store transaction.
	Link func(project, item string) string
	// ItemLink returns the link of an item the inbox already listed, so
	// the inbox reads the item once. Link of the item's ids when nil.
	ItemLink func(it machine.Item) string
	// GitHub merges a document on a sign-off answer. SetGitHub sets it
	// after the GitHub setup. A sign-off answer refuses while it is nil.
	GitHub DocGitHub
	// Now is the inbox clock. time.Now when nil.
	Now          func() time.Time
	PollInterval time.Duration
	Logger       *slog.Logger
}

// Inbox serves the inbox verbs.
type Inbox struct {
	cfg Config
	log *slog.Logger

	mu   sync.Mutex
	wake chan struct{}
}

// New returns an Inbox.
func New(cfg Config) *Inbox {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.List == nil && cfg.Machine != nil {
		cfg.List = cfg.Machine.List
	}
	if cfg.Link == nil {
		cfg.Link = func(project, item string) string { return "loop work show " + item + " --project " + project }
	}
	if cfg.ItemLink == nil {
		link := cfg.Link
		cfg.ItemLink = func(it machine.Item) string { return link(it.Project, it.ID) }
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Inbox{cfg: cfg, log: log, wake: make(chan struct{})}
}

// Register sets the handlers of the inbox verbs.
func (in *Inbox) Register(srv *rpc.Server) error {
	for path, h := range map[string]rpc.Handler{
		"inbox list":    in.list,
		"inbox wait":    in.waitVerb,
		"inbox answer":  in.answer,
		"inbox defer":   in.deferVerb,
		"inbox prepare": in.prepare,
		"inbox note":    in.note,
	} {
		if err := srv.Handle(path, h); err != nil {
			return err
		}
	}
	return srv.GuardReplay("inbox answer", in.answerAtOtherHead)
}

// Changed wakes every `inbox wait` call, so a write the server made shows
// at once. A write the server did not make shows at the next poll.
func (in *Inbox) Changed() {
	in.mu.Lock()
	close(in.wake)
	in.wake = make(chan struct{})
	in.mu.Unlock()
}

func (in *Inbox) woken() <-chan struct{} {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.wake
}

// Notice is a change the operator reads, such as a settings change that
// merged. Its entry awaits a person and asks nothing. An answer dismisses
// it, and a defer refuses.
type Notice struct {
	Project string
	Subject string // such as "factorio: stages.implementer.cap 8 (host) to 3"
	Body    string // the change; agent text in it sits in agent tags
	Source  string // what put it, such as "settings_change"
	Ref     string // the source's id of the change
}

// PutNotice puts n in the inbox and returns its row. A second put of the
// same source and ref puts nothing and returns the first row.
func PutNotice(tx *store.Tx, n Notice) (int64, error) {
	if _, err := tx.Exec(`INSERT INTO notices (project, source, ref, subject, body, at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, ref) DO NOTHING`, n.Project, n.Source, n.Ref, n.Subject, n.Body, tx.Now().UnixMilli()); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRow(`SELECT id FROM notices WHERE source = ? AND ref = ?`, n.Source, n.Ref).Scan(&id)
	return id, err
}

// The opening and closing agent tags, and the pattern AgentText escapes.
const (
	agentOpen  = "<!--sdlc:agent run=%s-->"
	agentClose = "<!--/sdlc:agent-->"
)

var (
	loopTag   = regexp.MustCompile(`(?i)<!--(\s*/?\s*sdlc:)`)
	runIDSafe = regexp.MustCompile(`[^A-Za-z0-9._:/@-]`)
	dashes    = regexp.MustCompile(`-{2,}`)
)

// AgentText wraps text an agent wrote in agent tags. Each `<!--sdlc:` or
// `<!--/sdlc:` inside it, with any spacing or case, comes out as
// `&lt;!--sdlc:`, so the text can neither end the block nor forge a loop
// tag.
func AgentText(run, text string) string {
	// A run id holds no `>` and no `--`, so it cannot end the opening tag.
	run = dashes.ReplaceAllString(runIDSafe.ReplaceAllString(run, "_"), "-")
	if run == "" {
		run = "unknown"
	}
	return strings.Replace(agentOpen, "%s", run, 1) + "\n" + loopTag.ReplaceAllString(text, "&lt;!--$1") + "\n" + agentClose
}
