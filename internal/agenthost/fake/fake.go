// Package fake is an in-memory agent host. It is deterministic: a Clock
// that only moves when a test moves it, identities numbered in start order,
// and a knob for every state the scheduler, the inbox and the acceptance
// suite must react to.
//
// The fake keeps the real host's rules. A stale handle reads Unknown, an
// incomplete inventory never reads Absent, the prompt reaches the "harness"
// only as a file path, and an ended session closes and keeps its checkout.
package fake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
)

// HostName is the host part of every RunID the fake issues.
const HostName = "fake:local"

// DroppedModel is the model a session records when the fake drops the
// model argument.
const DroppedModel = "harness-default"

// Clock is a controllable clock. It never moves on its own.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock starts a clock at t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Turn is what a session's transcript shows.
type Turn int

const (
	// Working: the turn has not ended. A new session starts here.
	Working Turn = iota
	// ToolRunning: a tool call is still running. Such a session never ends
	// by the quiet period.
	ToolRunning
	// TurnEnded: the last turn ended with nothing pending.
	TurnEnded
)

// Launch records one start: the harness argument vector and the spec.
type Launch struct {
	ID   agenthost.RunID
	Argv []string
	Spec agenthost.StartSpec
}

type session struct {
	s        agenthost.Session
	terminal string // the current handle; differs from s.ID.Terminal after Restart(false)
	// incarnation is the current incarnation; it differs from
	// s.ID.Incarnation after DaemonDeath.
	incarnation string
	stale       bool
	closed      bool
	exited      bool
	exitCode    *int
	turn        Turn
	lastWrite   time.Time
	refuse      bool
	notes       []string
	observed    string
}

// Host is the fake agent host. The zero value is not usable; call New.
type Host struct {
	mu         sync.Mutex
	clock      *Clock
	root       string
	quiet      time.Duration
	extra      map[agenthost.Harness][]string
	epoch      int
	next       int
	checkouts  map[string]bool
	sessions   []*session
	launches   []Launch
	incomplete bool
	neverReady bool
	dropModel  bool
	down       bool
	quota      map[agenthost.Harness]agenthost.Quota
}

// Option configures a Host.
type Option func(*Host)

// WithClock shares a clock with the test.
func WithClock(c *Clock) Option { return func(h *Host) { h.clock = c } }

// WithQuietPeriod overrides agenthost.DefaultQuietPeriod.
func WithQuietPeriod(d time.Duration) Option { return func(h *Host) { h.quiet = d } }

// WithHarnessArgs sets the extra arguments of one harness.
func WithHarnessArgs(hs agenthost.Harness, args ...string) Option {
	return func(h *Host) { h.extra[hs] = args }
}

// New makes a fake host whose checkouts live under root.
func New(root string, opts ...Option) *Host {
	h := &Host{
		clock:     NewClock(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)),
		root:      filepath.Clean(root),
		quiet:     agenthost.DefaultQuietPeriod,
		extra:     map[agenthost.Harness][]string{},
		epoch:     1,
		checkouts: map[string]bool{},
		quota:     map[agenthost.Harness]agenthost.Quota{},
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

var (
	_ agenthost.Host        = (*Host)(nil)
	_ agenthost.QuotaReader = (*Host)(nil)
)

// ---- Driving API ----

// Clock returns the host's clock.
func (h *Host) Clock() *Clock { return h.clock }

// Advance moves the host's clock. It serves agenthost.Advancer.
func (h *Host) Advance(d time.Duration) { h.clock.Advance(d) }

// Epoch is the host's current runtime identity.
func (h *Host) Epoch() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.epochLocked()
}

func (h *Host) epochLocked() string { return fmt.Sprintf("fake-epoch-%d", h.epoch) }

// AddCheckout registers an existing checkout path, as if the host made it.
func (h *Host) AddCheckout(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkouts[filepath.Clean(path)] = true
}

// CheckoutExists says whether the checkout is still there. It serves
// agenthost.CheckoutObserver.
func (h *Host) CheckoutExists(path string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.checkouts[filepath.Clean(path)]
}

// Exit makes the session's harness process exit with code.
func (h *Host) Exit(id agenthost.RunID, code int) {
	h.with(id, func(s *session) {
		s.exited = true
		s.exitCode = &code
		s.lastWrite = h.clock.Now()
	})
}

// EndTurn makes the transcript show the last turn ended now, with nothing
// pending. It serves agenthost.TurnDriver.
func (h *Host) EndTurn(id agenthost.RunID) { h.setTurn(id, TurnEnded) }

// StartTool makes the transcript show a tool call still running. It serves
// agenthost.TurnDriver.
func (h *Host) StartTool(id agenthost.RunID) { h.setTurn(id, ToolRunning) }

// Work makes the transcript show a turn in progress.
func (h *Host) Work(id agenthost.RunID) { h.setTurn(id, Working) }

// Write records activity in the checkout now. It restarts the quiet period
// and leaves the turn state alone.
func (h *Host) Write(id agenthost.RunID) {
	h.with(id, func(s *session) { s.lastWrite = h.clock.Now() })
}

func (h *Host) setTurn(id agenthost.RunID, t Turn) {
	h.with(id, func(s *session) {
		s.turn = t
		s.lastWrite = h.clock.Now()
	})
}

// MakeStale makes the session's handle stale. It serves agenthost.Staler.
func (h *Host) MakeStale(id agenthost.RunID) {
	h.with(id, func(s *session) { s.stale = true })
}

// RefuseNotify makes the host refuse input to one session. It serves
// agenthost.NotifyRefuser.
func (h *Host) RefuseNotify(id agenthost.RunID, on bool) {
	h.with(id, func(s *session) { s.refuse = on })
}

// SetInventoryIncomplete makes every inventory incomplete. It serves
// agenthost.InventoryBreaker.
func (h *Host) SetInventoryIncomplete(on bool) { h.set(func() { h.incomplete = on }) }

// SetNeverReady makes every later start never become ready. It serves
// agenthost.ReadinessBreaker.
func (h *Host) SetNeverReady(on bool) { h.set(func() { h.neverReady = on }) }

// SetDropModel makes the host drop the model argument of later starts. It
// serves agenthost.ModelDropper.
func (h *Host) SetDropModel(on bool) { h.set(func() { h.dropModel = on }) }

// SetDown makes every call fail with agenthost.ErrHostUnavailable.
func (h *Host) SetDown(on bool) { h.set(func() { h.down = on }) }

// SetQuota sets the quota reading of one harness.
func (h *Host) SetQuota(q agenthost.Quota) { h.set(func() { h.quota[q.Harness] = q }) }

// Restart changes the host epoch. Live sessions keep running. With
// keepHandles, each keeps its identity, which is what the orca-serve
// restart measurement showed. Without it, each gets a new handle with the
// same incarnation and PTY, and the old handle reads stale: the unmeasured
// daemon-restart case.
func (h *Host) Restart(keepHandles bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.epoch++
	if keepHandles {
		return
	}
	for _, s := range h.sessions {
		if s.closed {
			continue
		}
		h.next++
		s.terminal = fmt.Sprintf("term_fake_%04d", h.next)
	}
}

// DaemonDeath replays the measured death of the daemon that owns the PTYs
// (docs/evidence/orca-restart.md): each open session's process dies, and
// the daemon comes back with the same handle and PTY on a fresh shell. The
// fresh shell gets a new incarnation, the unmeasured case the real host
// must never take as the run. The epoch stays, because orca-serve did not
// restart.
func (h *Host) DaemonDeath() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		if s.closed {
			continue
		}
		h.next++
		s.incarnation = fmt.Sprintf("inc_fake_%04d", h.next)
		s.exited, s.exitCode = false, nil
	}
}

// Notifications lists the text each Notify delivered to the session. It
// serves agenthost.NotifyObserver.
func (h *Host) Notifications(id agenthost.RunID) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		if s.s.ID == id {
			return slices.Clone(s.notes)
		}
	}
	return nil
}

// Launches lists every start in order.
func (h *Host) Launches() []Launch {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.launches)
}

// ProcessArgs lists the harness argument vector of every start. It serves
// agenthost.ArgvReader.
func (h *Host) ProcessArgs() ([][]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([][]string, 0, len(h.launches))
	for _, l := range h.launches {
		out = append(out, slices.Clone(l.Argv))
	}
	return out, nil
}

// Sessions lists every session the fake started, closed ones included.
func (h *Host) Sessions() []agenthost.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]agenthost.Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		out = append(out, s.s)
	}
	return out
}

// CurrentID is the identity the host lists for the session now. It differs
// from the start identity only after Restart(false) or DaemonDeath.
func (h *Host) CurrentID(id agenthost.RunID) (agenthost.RunID, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		if s.s.ID == id {
			return h.current(s), true
		}
	}
	return agenthost.RunID{}, false
}

func (h *Host) set(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f()
}

// with runs f on the session that started with id. An unknown id is a test
// bug, so it panics.
func (h *Host) with(id agenthost.RunID, f func(*session)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		if s.s.ID == id {
			f(s)
			return
		}
	}
	panic(fmt.Sprintf("fake: no session %s", id))
}

func (h *Host) current(s *session) agenthost.RunID {
	id := s.s.ID
	id.Terminal = s.terminal
	id.Incarnation = s.incarnation
	return id
}

// resolve finds the session the host lists under id now, or explains why
// the handle is stale.
func (h *Host) resolve(id agenthost.RunID) (*session, error) {
	for _, s := range h.sessions {
		if h.current(s) == id && !s.stale && !s.closed {
			return s, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", agenthost.ErrStaleHandle, id)
}

func (h *Host) unavailable() error {
	if h.down {
		return fmt.Errorf("%w: fake host is down", agenthost.ErrHostUnavailable)
	}
	return nil
}

// ---- agenthost.Host ----

// CreateCheckout registers root/<name>.
func (h *Host) CreateCheckout(_ context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.unavailable(); err != nil {
		return agenthost.Checkout{}, err
	}
	if spec.Name == "" || filepath.Base(spec.Name) != spec.Name {
		return agenthost.Checkout{}, fmt.Errorf("%w: checkout name %q", agenthost.ErrBadSpec, spec.Name)
	}
	path := filepath.Join(h.root, spec.Name)
	if h.checkouts[path] {
		return agenthost.Checkout{}, fmt.Errorf("%w: checkout %s exists", agenthost.ErrBadSpec, path)
	}
	h.checkouts[path] = true
	return agenthost.Checkout{Path: path, Branch: spec.Name, Ref: "fake::" + path}, nil
}

// Start starts a session. The prompt file must exist and be a regular file.
func (h *Host) Start(_ context.Context, spec agenthost.StartSpec) (agenthost.Session, error) {
	if err := spec.Validate(); err != nil {
		return agenthost.Session{}, err
	}
	if !agenthost.InsideRoot(h.root, spec.Checkout) {
		return agenthost.Session{}, fmt.Errorf("%w: %s", agenthost.ErrOutsideRoot, spec.Checkout)
	}
	if fi, err := os.Stat(spec.PromptFile); err != nil || !fi.Mode().IsRegular() {
		return agenthost.Session{}, fmt.Errorf("%w: prompt file %s is not a readable file", agenthost.ErrBadSpec, spec.PromptFile)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.unavailable(); err != nil {
		return agenthost.Session{}, err
	}
	if !h.checkouts[filepath.Clean(spec.Checkout)] {
		return agenthost.Session{}, fmt.Errorf("%w: no checkout %s", agenthost.ErrBadSpec, spec.Checkout)
	}
	argv, err := agenthost.HarnessArgv(spec, h.extra[spec.Harness])
	if err != nil {
		return agenthost.Session{}, err
	}
	h.next++
	id := agenthost.RunID{
		Host:        HostName,
		Terminal:    fmt.Sprintf("term_fake_%04d", h.next),
		Incarnation: fmt.Sprintf("inc_fake_%04d", h.next),
	}
	now := h.clock.Now()
	s := &session{
		s: agenthost.Session{
			ID: id, Kind: spec.Kind, Checkout: filepath.Clean(spec.Checkout), Harness: spec.Harness,
			Model: spec.Model, Effort: spec.Effort, Pty: fmt.Sprintf("pty_fake_%04d", h.next),
			Epoch: h.epochLocked(), StartedAt: now,
		},
		terminal:    id.Terminal,
		incarnation: id.Incarnation,
		lastWrite:   now,
		observed:    spec.Model,
	}
	if h.dropModel {
		s.observed = DroppedModel
	}
	h.sessions = append(h.sessions, s)
	h.launches = append(h.launches, Launch{ID: id, Argv: argv, Spec: spec})
	if h.neverReady {
		s.closed = true
		return s.s, fmt.Errorf("%w: %s never appeared", agenthost.ErrNotReady, id)
	}
	if spec.RequireVerifiedModel && s.observed != spec.Model {
		s.closed = true
		return s.s, fmt.Errorf("%w: requested %s, harness records %s", agenthost.ErrModelUnverified, spec.Model, s.observed)
	}
	return s.s, nil
}

// Inventory lists every session whose terminal is still open.
func (h *Host) Inventory(_ context.Context) (agenthost.Inventory, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.unavailable(); err != nil {
		return agenthost.Inventory{Why: err.Error()}, err
	}
	inv := agenthost.Inventory{Complete: !h.incomplete, Epoch: h.epochLocked()}
	if h.incomplete {
		inv.Why = "fake inventory is set incomplete"
	}
	for _, s := range h.sessions {
		if s.closed || s.exited {
			continue
		}
		inv.Rows = append(inv.Rows, agenthost.Row{
			ID: h.current(s), Checkout: s.s.Checkout, Pty: s.s.Pty,
			Agent: string(s.s.Harness), Connected: true,
		})
	}
	return inv, nil
}

// Settle reads the session's liveness.
func (h *Host) Settle(_ context.Context, id agenthost.RunID) (agenthost.Settlement, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.settleLocked(id)
}

func (h *Host) settleLocked(id agenthost.RunID) (agenthost.Settlement, error) {
	if err := h.unavailable(); err != nil {
		return agenthost.Settlement{Liveness: agenthost.Unknown, Reason: err.Error()}, err
	}
	s, err := h.resolve(id)
	if err != nil {
		return agenthost.Settlement{Liveness: agenthost.Unknown, Reason: err.Error()}, err
	}
	if s.exited {
		return agenthost.Settlement{Liveness: agenthost.Exited, ExitCode: s.exitCode, Reason: "harness process exited"}, nil
	}
	return agenthost.Settlement{Liveness: agenthost.Live}, nil
}

// Ended applies KTD8 with the fake clock.
func (h *Host) Ended(_ context.Context, sess agenthost.Session) (agenthost.EndReport, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := h.settleLocked(sess.ID)
	if err != nil {
		return agenthost.EndReport{Reason: "liveness unknown: " + err.Error()}, err
	}
	s, _ := h.resolve(sess.ID)
	if st.Liveness == agenthost.Exited {
		return agenthost.EndReport{Ended: true, Cause: agenthost.EndExited, Since: s.lastWrite, Reason: "harness process exited"}, nil
	}
	switch s.turn {
	case ToolRunning:
		return agenthost.EndReport{Reason: "a tool call is still running"}, nil
	case Working:
		return agenthost.EndReport{Reason: "its last turn has not ended"}, nil
	}
	idle := h.clock.Now().Sub(s.lastWrite)
	if idle < h.quiet {
		return agenthost.EndReport{Reason: fmt.Sprintf("quiet for %s of %s", idle, h.quiet)}, nil
	}
	return agenthost.EndReport{
		Ended: true, Cause: agenthost.EndQuiet, Since: s.lastWrite,
		Reason: fmt.Sprintf("last turn ended and the checkout stayed quiet for %s", idle),
	}, nil
}

// Stop closes the session and reports Exited, which the fake always proves.
// A stale handle reports Unknown.
func (h *Host) Stop(_ context.Context, id agenthost.RunID) (agenthost.Settlement, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.unavailable(); err != nil {
		return agenthost.Settlement{Liveness: agenthost.Unknown, Reason: err.Error()},
			fmt.Errorf("%w: %w", agenthost.ErrExitUnproven, err)
	}
	s, err := h.resolve(id)
	if err != nil {
		return agenthost.Settlement{Liveness: agenthost.Unknown, Reason: err.Error()},
			fmt.Errorf("%w: %w", agenthost.ErrExitUnproven, err)
	}
	s.closed = true
	return agenthost.Settlement{Liveness: agenthost.Exited, ExitCode: s.exitCode, Reason: "host confirmed the close"}, nil
}

// Close ends the session and keeps its checkout.
func (h *Host) Close(ctx context.Context, id agenthost.RunID) error {
	_, err := h.Stop(ctx, id)
	return err
}

// RemoveCheckout forgets a checkout that holds no open session.
func (h *Host) RemoveCheckout(_ context.Context, path string) error {
	if !agenthost.InsideRoot(h.root, path) {
		return fmt.Errorf("%w: %s", agenthost.ErrOutsideRoot, path)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.unavailable(); err != nil {
		return err
	}
	clean := filepath.Clean(path)
	if h.incomplete {
		return fmt.Errorf("%w: inventory incomplete, %s", agenthost.ErrCheckoutBusy, clean)
	}
	for _, s := range h.sessions {
		if s.s.Checkout == clean && !s.closed && !s.exited {
			return fmt.Errorf("%w: %s holds %s", agenthost.ErrCheckoutBusy, clean, s.s.ID)
		}
	}
	delete(h.checkouts, clean)
	return nil
}

// Notify records text for the session, or refuses.
func (h *Host) Notify(_ context.Context, id agenthost.RunID, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.unavailable(); err != nil {
		return fmt.Errorf("%w: %w", agenthost.ErrNotifyRefused, err)
	}
	s, err := h.resolve(id)
	if err != nil {
		return fmt.Errorf("%w: %w", agenthost.ErrNotifyRefused, err)
	}
	if s.exited {
		return fmt.Errorf("%w: %s exited", agenthost.ErrNotifyRefused, id)
	}
	if s.refuse {
		return fmt.Errorf("%w: host refused input to %s", agenthost.ErrNotifyRefused, id)
	}
	s.notes = append(s.notes, text)
	return nil
}

// ModelReceipt compares the requested model with the one the fake recorded.
func (h *Host) ModelReceipt(_ context.Context, sess agenthost.Session) (agenthost.ModelReceipt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rc := agenthost.ModelReceipt{Status: agenthost.Unverified, Requested: sess.Model}
	if err := h.unavailable(); err != nil {
		rc.Reason = err.Error()
		return rc, err
	}
	for _, s := range h.sessions {
		if s.s.ID == sess.ID {
			rc.Observed = s.observed
			if s.observed == sess.Model {
				rc.Status = agenthost.Verified
			} else {
				rc.Reason = "the harness records another model"
			}
			return rc, nil
		}
	}
	rc.Reason = "no record of this session"
	return rc, nil
}

// Quota returns the reading SetQuota stored.
func (h *Host) Quota(_ context.Context, hs agenthost.Harness) (agenthost.Quota, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.unavailable(); err != nil {
		return agenthost.Quota{Harness: hs}, err
	}
	q, ok := h.quota[hs]
	if !ok {
		return agenthost.Quota{Harness: hs}, fmt.Errorf("%w: %s", agenthost.ErrQuotaUnavailable, hs)
	}
	return q, nil
}
