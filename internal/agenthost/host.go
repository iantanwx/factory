// Package agenthost is the one interface through which the server starts,
// watches and stops agent sessions: stage runs, rooms and the coordinator
// (R25, R50, KTD21). The agent host stays the truth about a session. The
// server's own registry is a cache (KTD9).
//
// Three rules hold for every implementation:
//
//   - Every doubt reads as Unknown. A failed read, a stale handle, an
//     incomplete inventory and a host restart never read as Exited or Absent.
//   - The prompt reaches the agent only through a file. No process argument
//     carries prompt text (HarnessArgv). Only the session's one-line title
//     opens the first message (FirstMessage).
//   - An ended session closes and keeps its checkout (KTD8).
//   - Every stage run, room and coordinator runs in a terminal session whose
//     PTY the host's daemon owns. Such a session survived an orca-serve
//     restart (docs/evidence/orca-restart.md). A native chat session did not.
package agenthost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Liveness is what the host can prove about one session's process.
type Liveness string

const (
	// Live means the host shows the exact session identity and its process runs.
	Live Liveness = "live"
	// Exited means the host proved that the process ended.
	Exited Liveness = "exited"
	// Unknown means the host could not prove either. A stale handle, a failed
	// read and a host restart all read as Unknown, never as Exited.
	Unknown Liveness = "unknown"
)

// Presence is what one inventory says about one session.
type Presence string

const (
	// Present means an inventory, complete or not, lists the exact identity.
	Present Presence = "present"
	// Absent means a complete inventory proved the session gone. Inventory.Lookup
	// states the rule.
	Absent Presence = "absent"
	// PresenceUnknown is every other case: an incomplete inventory, another
	// epoch, or the same incarnation under another handle.
	PresenceUnknown Presence = "unknown"
)

// Harness names the agent CLI that runs a session.
type Harness string

const (
	Claude Harness = "claude"
	Codex  Harness = "codex"
)

// Kind is the kind of session the server starts through the host.
type Kind string

const (
	KindStage       Kind = "stage"
	KindRoom        Kind = "room"
	KindCoordinator Kind = "coordinator"
)

// ReceiptStatus is the model receipt of one session.
type ReceiptStatus string

const (
	// Verified means the harness's own record names the requested model.
	Verified ReceiptStatus = "verified"
	// Unverified means no record names it: the record names another model,
	// names none yet, or cannot be read.
	Unverified ReceiptStatus = "unverified"
)

// DefaultQuietPeriod is how long a checkout stays quiet after the last turn
// ended before the session reads as ended. Ported from QUIET_MINUTES in alpha's
// scripts/sdlc/finished-agents.ts, which states the measurement behind 30.
const DefaultQuietPeriod = 30 * time.Minute

// DefaultReadyWindow is how long a healthy agent may take to appear. Ported
// from APPEAR_MS in alpha's scripts/sdlc/agent-start.ts.
const DefaultReadyWindow = 60 * time.Second

// Sentinel errors. Implementations wrap them, so callers use errors.Is.
var (
	// ErrNotReady: the agent never appeared within the readiness window. The
	// run class is not-ready (KTD8), which never counts against the budget.
	ErrNotReady = errors.New("agent did not become ready")
	// ErrModelUnverified: a role that requires a verified model got an
	// unverified receipt, so the session was closed and did not start.
	ErrModelUnverified = errors.New("model receipt unverified")
	// ErrStaleHandle: the host no longer resolves the session's handle, or
	// resolves it to another incarnation.
	ErrStaleHandle = errors.New("session handle is stale")
	// ErrHostUnavailable: the host could not answer (missing CLI, non-zero
	// exit, malformed output, cancelled context).
	ErrHostUnavailable = errors.New("agent host unavailable")
	// ErrNotifyRefused: the host did not confirm that it accepted the input.
	ErrNotifyRefused = errors.New("notify refused")
	// ErrOutsideRoot: a checkout path is outside the configured checkout root.
	ErrOutsideRoot = errors.New("checkout outside the checkout root")
	// ErrExitUnproven: a stop could not prove that the process ended.
	ErrExitUnproven = errors.New("exit not proven")
	// ErrCheckoutBusy: a checkout still holds a session, or the host could not
	// prove that it holds none.
	ErrCheckoutBusy = errors.New("checkout may hold a live session")
	// ErrBadSpec: a start or checkout request is malformed.
	ErrBadSpec = errors.New("invalid agent host request")
	// ErrQuotaUnavailable: the host has no quota reading for that harness.
	ErrQuotaUnavailable = errors.New("quota unavailable")
)

// RunID identifies one session: the host, the terminal and the incarnation of
// the process in that terminal. String renders it as the beads lease owner.
type RunID struct {
	Host        string `json:"host"`
	Terminal    string `json:"terminal"`
	Incarnation string `json:"incarnation"`
}

var idPart = regexp.MustCompile(`^[A-Za-z0-9._:@-]+$`)

// Valid reports whether every part is present and holds no separator.
func (r RunID) Valid() bool {
	return idPart.MatchString(r.Host) && idPart.MatchString(r.Terminal) &&
		idPart.MatchString(r.Incarnation)
}

// String renders host/terminal/incarnation. The value is stable for one
// session, so it serves as the beads lease owner.
func (r RunID) String() string {
	return r.Host + "/" + r.Terminal + "/" + r.Incarnation
}

// ParseRunID reverses String.
func ParseRunID(s string) (RunID, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return RunID{}, fmt.Errorf("%w: run id %q is not host/terminal/incarnation", ErrBadSpec, s)
	}
	id := RunID{Host: parts[0], Terminal: parts[1], Incarnation: parts[2]}
	if !id.Valid() {
		return RunID{}, fmt.Errorf("%w: run id %q has an invalid part", ErrBadSpec, s)
	}
	return id, nil
}

// MaxCheckoutName bounds a checkout name, in bytes. The orca host refuses
// a longer one.
const MaxCheckoutName = 128

// CheckoutSpec asks the host for a new checkout.
type CheckoutSpec struct {
	// Name is the worktree name. The host derives the path from it. It
	// holds at most MaxCheckoutName bytes.
	Name string `json:"name"`
	// BaseBranch is the ref the checkout starts from. Empty means the repo default.
	BaseBranch string `json:"base_branch,omitempty"`
	// Setup runs the repository's setup hook. The agent never starts in the
	// same call (alpha's agent-start.ts records why).
	Setup bool `json:"setup"`
}

// Checkout is one checkout the host made.
type Checkout struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	// Ref is the host's own id for the checkout.
	Ref string `json:"ref,omitempty"`
}

// StartSpec asks the host to start one session in an existing checkout.
type StartSpec struct {
	Kind     Kind    `json:"kind"`
	Checkout string  `json:"checkout"`
	Harness  Harness `json:"harness"`
	// Model is passed to the harness as stage config sets it.
	Model string `json:"model"`
	// Effort maps to --effort for Claude Code and to
	// -c model_reasoning_effort= for Codex. Empty omits the flag.
	Effort string `json:"effort,omitempty"`
	// PromptFile holds the prompt. Only its path reaches a process argument.
	PromptFile string `json:"prompt_file"`
	// Title labels the session in the host's UI and opens its first
	// message. It names the session's work; Label makes it one line.
	Title string `json:"title,omitempty"`
	// RequireVerifiedModel marks an escalated-model role. Such a session
	// does not start unless its model receipt reads Verified.
	RequireVerifiedModel bool `json:"require_verified_model,omitempty"`
}

// Session is what the server persists about one started session. Every
// read that needs more than the identity takes a Session.
type Session struct {
	ID       RunID   `json:"id"`
	Kind     Kind    `json:"kind"`
	Checkout string  `json:"checkout"`
	Harness  Harness `json:"harness"`
	Model    string  `json:"model"`
	Effort   string  `json:"effort,omitempty"`
	// Pty is the host's id for the session's pseudo-terminal, when it names one.
	Pty string `json:"pty,omitempty"`
	// Epoch is the host's runtime identity when the server last saw the
	// session in a complete inventory (Inventory.Readopted). It starts as the
	// epoch of the start. A changed epoch is no evidence of a lost run.
	Epoch     string    `json:"epoch"`
	StartedAt time.Time `json:"started_at"`
}

// Row is one session as an inventory lists it.
type Row struct {
	ID       RunID  `json:"id"`
	Checkout string `json:"checkout"`
	Pty      string `json:"pty,omitempty"`
	// Agent is the host's own answer about the process, or "" when it names none.
	Agent     string `json:"agent,omitempty"`
	Connected bool   `json:"connected"`
}

// Inventory is one listing of every session the host holds.
type Inventory struct {
	Rows []Row `json:"rows"`
	// Complete is true only when the host proved the listing covers every
	// session on every host it serves.
	Complete bool `json:"complete"`
	// Epoch is the host's runtime identity at the time of the listing.
	Epoch string `json:"epoch"`
	// Why says what made the listing incomplete.
	Why string `json:"why,omitempty"`
}

// Lookup says whether this inventory lists the session.
//
// Present needs the exact identity. A host restart does not change it: the
// orca-serve restart measured in docs/evidence/orca-restart.md kept the
// handle, the PTY and the incarnation, so an exact match re-adopts the run
// whatever the epoch.
//
// Absent needs all four: a complete inventory, of the epoch in which the
// server last saw the session (Session.Epoch, which Readopted moves), with
// no row of the same incarnation and no row of the same PTY. A changed epoch
// alone proves nothing lost. It only withholds Absent until a complete
// inventory of the new epoch re-reads the identity. A host daemon restart is
// unmeasured, and a trace of the session under another handle keeps it
// Unknown.
func (inv Inventory) Lookup(s Session) Presence {
	for _, r := range inv.Rows {
		if r.ID == s.ID {
			return Present
		}
	}
	if !inv.Complete || s.Epoch == "" || inv.Epoch != s.Epoch {
		return PresenceUnknown
	}
	if len(inv.Traces(s)) > 0 {
		return PresenceUnknown
	}
	return Absent
}

// Untraced says whether a complete inventory, of any epoch, lists neither
// the session's exact identity nor a trace of it. Unlike Lookup it ignores
// the epoch, so it proves no absence on its own: the server reads it only
// for a run whose liveness read unknown past its age bound (KTD9).
func (inv Inventory) Untraced(s Session) bool {
	if !inv.Complete {
		return false
	}
	for _, r := range inv.Rows {
		if r.ID == s.ID {
			return false
		}
	}
	return len(inv.Traces(s)) == 0
}

// Readopted returns the session with its epoch moved to this inventory's,
// when a complete inventory lists the exact identity. The server persists
// the result, so a later absence in the new epoch can read Absent.
func (inv Inventory) Readopted(s Session) (Session, bool) {
	if !inv.Complete || inv.Epoch == "" {
		return s, false
	}
	for _, r := range inv.Rows {
		if r.ID == s.ID {
			s.Epoch = inv.Epoch
			return s, true
		}
	}
	return s, false
}

// Rebound returns the session moved to the one row that carries its
// incarnation and its checkout under another handle, with the epoch moved
// to this inventory's. It needs a complete inventory that lists no row of
// the exact identity, and exactly one such row. Any doubt rebinds nothing.
// Only the coordinator reads it: the operator decided on 2026-10-24
// (escalation 5, option A, loop fault F-17) that a live coordinator whose
// handle went stale keeps its session under the new handle. A stage run's
// trace still only keeps it Unknown. When no row carries the incarnation,
// ReboundToAgent is the coordinator's other way.
func (inv Inventory) Rebound(s Session) (Session, bool) {
	if !inv.Complete || inv.Epoch == "" || s.Checkout == "" {
		return s, false
	}
	var match []Row
	for _, r := range inv.Rows {
		if r.ID == s.ID {
			return s, false
		}
		if r.ID.Incarnation == s.ID.Incarnation && r.Checkout == s.Checkout {
			match = append(match, r)
		}
	}
	if len(match) != 1 {
		return s, false
	}
	s.ID, s.Epoch = match[0].ID, inv.Epoch
	if match[0].Pty != "" {
		s.Pty = match[0].Pty
	}
	return s, true
}

// ReboundToAgent returns the session moved to the one live row in its
// checkout whose agent is the session's harness, with that row's PTY and
// this inventory's epoch. It needs a complete inventory that lists no row
// of the exact identity and no row of the session's incarnation, so it
// serves only where Rebound finds nothing. A row that names no agent, such
// as the shell orca opens in each worktree or the fresh shell a daemon
// death leaves (docs/evidence/orca-restart.md), never counts. Two such
// agent rows, or the one row disconnected, rebind nothing. Only the
// coordinator reads it: one coordinator runs in its checkout, so the one
// agent there is the coordinator (loop fault F-30).
func (inv Inventory) ReboundToAgent(s Session) (Session, bool) {
	if !inv.Complete || inv.Epoch == "" || s.Checkout == "" || s.Harness == "" {
		return s, false
	}
	var match []Row
	for _, r := range inv.Rows {
		if r.ID == s.ID || r.ID.Incarnation == s.ID.Incarnation {
			return s, false
		}
		if r.Checkout == s.Checkout && r.Agent == string(s.Harness) {
			match = append(match, r)
		}
	}
	if len(match) != 1 || !match[0].Connected {
		return s, false
	}
	s.ID, s.Pty, s.Epoch = match[0].ID, match[0].Pty, inv.Epoch
	return s, true
}

// Traces names each row, other than the exact identity, that carries the
// session's incarnation or its PTY. Such a row may be the same process
// under a new handle. That re-adoption is unmeasured, so a trace only keeps
// the session Unknown.
func (inv Inventory) Traces(s Session) []Row {
	var out []Row
	for _, r := range inv.Rows {
		if r.ID == s.ID {
			continue
		}
		if r.ID.Incarnation == s.ID.Incarnation || (s.Pty != "" && r.Pty == s.Pty) {
			out = append(out, r)
		}
	}
	return out
}

// Settlement is the host's answer about one session's process.
type Settlement struct {
	Liveness Liveness `json:"liveness"`
	// ExitCode is set only when the host reported one.
	ExitCode *int   `json:"exit_code,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// EndCause names why a session reads as ended.
type EndCause string

const (
	// EndExited: the harness process exited.
	EndExited EndCause = "exited"
	// EndQuiet: the transcript shows the last turn ended, and the checkout
	// stayed quiet for the quiet period.
	EndQuiet EndCause = "quiet"
	// EndLimit: the agent wrote only a usage limit and no work of its own.
	// The session ends at once, with no quiet period.
	EndLimit EndCause = "limit"
	// EndAPIError: the last turn ended on a transient model API error, and
	// the checkout stayed quiet for the quiet period. The server resumes
	// such a session before it settles it (F-29).
	EndAPIError EndCause = "api_error"
)

// Limit is the usage limit a session's agent wrote when it did no work.
type Limit struct {
	// Message is the text the agent wrote, as the harness wrote it.
	Message string `json:"message"`
	// At is when the agent wrote it.
	At time.Time `json:"at,omitzero"`
	// ResetAt is when the limit resets. Zero means the record names none.
	ResetAt time.Time `json:"reset_at,omitzero"`
}

// APIError is the transient model API error a session's last turn ended
// on: a 5xx status, 529, or a server-owned error type such as
// overloaded_error. The harness does not retry it after the turn ended, so
// the session idles at its prompt until something types into it.
type APIError struct {
	// Message is the text the agent wrote, as the harness wrote it.
	Message string `json:"message"`
	// Status is the HTTP status, or zero when the record states none.
	Status int `json:"status,omitempty"`
	// At is when the agent wrote the newest error.
	At time.Time `json:"at,omitzero"`
	// First is when the agent wrote the first error after its last work.
	// A resume that fails again keeps it, so it names one outage as the
	// session saw it.
	First time.Time `json:"first,omitzero"`
}

// EndReport answers whether a session ended (KTD8).
type EndReport struct {
	Ended bool     `json:"ended"`
	Cause EndCause `json:"cause,omitempty"`
	// Since is when the session went quiet or exited, when the host knows.
	Since time.Time `json:"since,omitzero"`
	// Reason says what keeps the session, or the evidence that ended it.
	Reason string `json:"reason"`
	// Limit is set when the cause is EndLimit.
	Limit *Limit `json:"limit,omitempty"`
	// APIError is set when the last turn ended on a transient model API
	// error, whether or not the quiet period has passed.
	APIError *APIError `json:"api_error,omitempty"`
	// Settlement is the liveness read the answer rests on, when the host
	// made one, so the caller need not read it again. A failed read is
	// Unknown.
	Settlement *Settlement `json:"settlement,omitempty"`
}

// ModelReceipt compares the requested model with the harness's own record.
type ModelReceipt struct {
	Status    ReceiptStatus `json:"status"`
	Requested string        `json:"requested"`
	Observed  string        `json:"observed,omitempty"`
	Reason    string        `json:"reason,omitempty"`
}

// QuotaWindow is one usage window of one harness.
type QuotaWindow struct {
	Name        string  `json:"name"`
	UsedPercent float64 `json:"used_percent"`
}

// Quota is the host's reading of one harness's usage.
type Quota struct {
	Harness   Harness       `json:"harness"`
	OK        bool          `json:"ok"`
	Windows   []QuotaWindow `json:"windows"`
	UpdatedAt time.Time     `json:"updated_at,omitzero"`
	Note      string        `json:"note,omitempty"`
}

// Host is the agent-host interface. Every method that reads the host
// answers Unknown, PresenceUnknown or an incomplete Inventory on a doubt.
type Host interface {
	// CreateCheckout makes a new checkout. It starts no agent.
	CreateCheckout(ctx context.Context, spec CheckoutSpec) (Checkout, error)
	// Start starts one session from a prompt file in an existing checkout and
	// waits for the agent to appear. A session that never appears returns
	// ErrNotReady. A role with RequireVerifiedModel whose receipt reads
	// Unverified returns ErrModelUnverified. On both errors the returned
	// Session names the identity, if the host made one, and the host has
	// tried to close it.
	Start(ctx context.Context, spec StartSpec) (Session, error)
	// Inventory lists every session. Complete is false when the host could
	// not list everything, and the error then says why when a read failed.
	Inventory(ctx context.Context) (Inventory, error)
	// Ended reports whether the session ended (KTD8). A session with a tool
	// call still running, or with Unknown liveness, has not ended. The
	// report carries the liveness read it made.
	Ended(ctx context.Context, s Session) (EndReport, error)
	// Settle reads the session's liveness. A stale handle reads Unknown.
	Settle(ctx context.Context, id RunID) (Settlement, error)
	// Stop ends the session and reports Exited only on proven exit.
	// Otherwise it reports Unknown and an error wrapping ErrExitUnproven.
	Stop(ctx context.Context, id RunID) (Settlement, error)
	// Close ends the session and keeps its checkout.
	Close(ctx context.Context, id RunID) error
	// RemoveCheckout deletes a checkout. It refuses a path outside the root,
	// and a checkout that may still hold a session.
	RemoveCheckout(ctx context.Context, path string) error
	// Notify types text into the session and submits it. A refused or
	// unconfirmed injection returns an error wrapping ErrNotifyRefused.
	Notify(ctx context.Context, id RunID, text string) error
	// ModelReceipt reads the model the harness itself recorded.
	ModelReceipt(ctx context.Context, s Session) (ModelReceipt, error)
}

// QuotaReader is the optional quota read. A host that implements it answers
// ErrQuotaUnavailable when it has no reading for that harness.
type QuotaReader interface {
	Quota(ctx context.Context, h Harness) (Quota, error)
}

var (
	modelPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/\[\]-]*$`)
	effortPattern = regexp.MustCompile(`^[a-z]+$`)
)

// Validate refuses a malformed start request before the host sees it.
func (s StartSpec) Validate() error {
	switch s.Kind {
	case KindStage, KindRoom, KindCoordinator:
	default:
		return fmt.Errorf("%w: unknown session kind %q", ErrBadSpec, s.Kind)
	}
	switch s.Harness {
	case Claude, Codex:
	default:
		return fmt.Errorf("%w: unknown harness %q", ErrBadSpec, s.Harness)
	}
	if !modelPattern.MatchString(s.Model) {
		return fmt.Errorf("%w: model %q", ErrBadSpec, s.Model)
	}
	if s.Effort != "" && !effortPattern.MatchString(s.Effort) {
		return fmt.Errorf("%w: effort %q", ErrBadSpec, s.Effort)
	}
	if s.Checkout == "" || !filepath.IsAbs(s.Checkout) {
		return fmt.Errorf("%w: checkout %q is not an absolute path", ErrBadSpec, s.Checkout)
	}
	if s.PromptFile == "" || !filepath.IsAbs(s.PromptFile) {
		return fmt.Errorf("%w: prompt file %q is not an absolute path", ErrBadSpec, s.PromptFile)
	}
	return nil
}

// PromptInstruction is the only text a process argument carries for the
// prompt: an instruction that names the file. The prompt text itself stays
// in the file, so no shell, no process table and no host log can see it.
func PromptInstruction(promptFile string) string {
	return "Read the file " + promptFile +
		" and follow the instructions in it. That file is your whole prompt for this session."
}

// MaxLabel bounds a session label, in runes.
const MaxLabel = 160

// Label makes s one line that a terminal title and a first message can
// carry: each run of spaces and control characters becomes one space, and
// a label longer than MaxLabel runes is cut and ends with an ellipsis.
func Label(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if r := []rune(s); len(r) > MaxLabel {
		s = strings.TrimSpace(string(r[:MaxLabel-1])) + "…"
	}
	return s
}

// FirstMessage is the one message a session starts with. It opens with
// the session's title, so the host's list names the work, and then names
// the prompt file. It is one line, so a host that types it into a terminal
// submits it once. It never holds prompt text.
func FirstMessage(s StartSpec) string {
	if t := Label(s.Title); t != "" {
		return t + " — " + PromptInstruction(s.PromptFile)
	}
	return PromptInstruction(s.PromptFile)
}

// HarnessArgv builds the argument vector that starts the harness. extra
// comes from host config and goes before the first message. The vector
// names the prompt file and never holds prompt text.
func HarnessArgv(s StartSpec, extra []string) ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	argv := []string{string(s.Harness), "--model", s.Model}
	if s.Effort != "" {
		switch s.Harness {
		case Claude:
			argv = append(argv, "--effort", s.Effort)
		case Codex:
			argv = append(argv, "-c", "model_reasoning_effort="+s.Effort)
		}
	}
	argv = append(argv, extra...)
	return append(argv, FirstMessage(s)), nil
}

// CheckoutPrefix starts the name of every checkout the loop makes:
// loop-<project>-<stage>-<run id> for a run, loop-<room id> for a room.
// The checkout root also holds the operator's own worktrees, and no
// listing reads one.
const CheckoutPrefix = "loop-"

// ListCheckoutDirs lists each directory named loop-* one level under a
// directory of root, as absolute paths, in order. It opens no directory
// one level under root whose name does not start loop-, so an operator's
// own worktree is never read.
func ListCheckoutDirs(root string) ([]string, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("the checkout root %q is not absolute", root)
	}
	repos, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, repo := range repos {
		if !repo.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, repo.Name()))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), CheckoutPrefix) {
				out = append(out, filepath.Join(root, repo.Name(), e.Name()))
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// InsideRoot reports whether path is strictly inside root, after both are
// cleaned. Symbolic links resolve when they exist, so a link out of the
// root is refused.
func InsideRoot(root, path string) bool {
	if root == "" || !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	r, p := resolve(root), resolve(path)
	rel, err := filepath.Rel(r, p)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// resolve cleans a path and resolves links in the longest prefix that exists.
func resolve(path string) string {
	clean := filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		return real
	}
	dir, base := filepath.Dir(clean), filepath.Base(clean)
	if dir == clean {
		return clean
	}
	return filepath.Join(resolve(dir), base)
}
