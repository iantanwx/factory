// Package inbox is the coordinator's one typed inbox (U12, KTD21). It is a
// derived view: its entries come from beads states (escalated, sign-off
// waiting, fired decisions) and from SQLite (alerts, questions, rooms and
// escalations that wait for the fixer's panel). Each entry says who it
// waits on, a person or the coordinator, and carries a link, so a
// coordinator act never reads as a wait on the operator.
//
// Text that an agent wrote sits between `<!--sdlc:agent run=<run-id>-->` and
// `<!--/sdlc:agent-->`. AgentText escapes any `<!--sdlc:` or `<!--/sdlc:`
// inside the text, so agent text cannot end the block or forge a loop tag.
package inbox

import (
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
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
	KindEscalation = "escalation" // an item in escalated
	KindPanel      = "panel"      // an escalation that waits for the fixer's panel
	KindDecision   = "decision"   // a fired decision
	KindSignoff    = "signoff"    // a document that waits for the operator's merge
	KindQuestion   = "question"   // a question the coordinator prepared
	KindRoom       = "room"       // a room that is not closed
	KindAlert      = "alert"      // an open alert
	KindFault      = "fault"      // an open or acknowledged loop fault
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
	// Next names the verb that settles the entry.
	Next string `json:"next"`
	// At is when the entry entered the inbox, in Unix milliseconds. The
	// cursor of `inbox wait --since` is the At of the last entry seen.
	At     int64  `json:"at"`
	Cursor string `json:"cursor"`
}

// Config configures an Inbox.
type Config struct {
	Store    *store.Store
	Machine  *machine.Machine
	Projects func() ([]config.Project, error)
	Redactor *secrets.Redactor
	// Link returns the link of a project's item. A `loop work show` line
	// when nil. U13 may link a sign-off to its pull request.
	Link func(project, item string) string
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
	if cfg.Link == nil {
		cfg.Link = func(project, item string) string { return "loop work show " + item + " --project " + project }
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
	} {
		if err := srv.Handle(path, h); err != nil {
			return err
		}
	}
	return nil
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
