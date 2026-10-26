// Package orca drives the orca CLI as the v1 agent host (R25).
//
// Every call shells out to the orca binary with --json and narrows the
// envelope orca prints: {"ok", "result", "error": {"code"}, "_meta":
// {"runtimeId"}}. Orca owns its field names and may change them, so each
// reader here checks the fields it needs, and a changed shape reads as
// Unknown, never as Exited or Absent.
//
// Every session starts as a terminal session through `orca agent launch
// --surface terminal`. The orca daemon owns that PTY, and it survived an
// orca-serve restart in docs/evidence/orca-restart.md. An orca native chat
// session did not, so this package never starts one.
package orca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
)

// Output is what one orca process printed, and how it exited.
type Output struct {
	Code   int
	Stdout []byte
	Stderr []byte
}

// Runner runs one orca command. A non-zero exit is an Output, not an error.
// An error means the process could not run or finish: a missing binary, a
// cancelled context.
type Runner interface {
	Run(ctx context.Context, args []string) (Output, error)
}

// DefaultCallTimeout bounds one orca call when the caller's context has no
// deadline. No caller of a tick sets one, so without it a hung orca holds
// the tick.
const DefaultCallTimeout = 30 * time.Second

// CheckoutCallTimeout bounds `worktree create` when the caller's context has
// no deadline. That call checks out a whole tree, which can take longer
// than any other call.
const CheckoutCallTimeout = 3 * time.Minute

// ExecRunner runs the orca binary at Bin.
type ExecRunner struct {
	Bin string
	// Timeout bounds one call when ctx has no deadline. Zero means
	// DefaultCallTimeout, or CheckoutCallTimeout for `worktree create`.
	Timeout time.Duration
	// WaitDelay bounds the wait for output after the context ends. Zero
	// means two seconds.
	WaitDelay time.Duration
}

// Run runs Bin with args. Cancelling ctx kills a hung call, and so does the
// end of the timeout when ctx has no deadline. Either reads as an error,
// which the host reports as agenthost.ErrHostUnavailable.
func (r ExecRunner) Run(ctx context.Context, args []string) (Output, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout(args))
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = r.WaitDelay
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 2 * time.Second
	}
	err := cmd.Run()
	out := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctx.Err() != nil {
		return out, fmt.Errorf("orca %s: %w", verb(args), ctx.Err())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		out.Code = exit.ExitCode()
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("orca %s: %w", verb(args), err)
	}
	return out, nil
}

func (r ExecRunner) timeout(args []string) time.Duration {
	switch {
	case r.Timeout > 0:
		return r.Timeout
	case verb(args) == "worktree create":
		return CheckoutCallTimeout
	}
	return DefaultCallTimeout
}

// Config configures the orca host.
type Config struct {
	// Bin is the orca CLI. On the devbox it is
	// /home/operator/.config/orca/cli/bin/orca. A bare "orca" on Linux can
	// name the GNOME screen reader, so Bin has no default.
	Bin string
	// Runner replaces the exec layer. Tests set it. When set, Bin is unused.
	Runner Runner
	// CheckoutRoot holds every checkout. A path outside it is refused.
	CheckoutRoot string
	// Repo selects the repo for `worktree create --repo`, for example
	// "path:/home/operator/projects/loop". Empty lets orca infer it.
	Repo string
	// ParentWorktree selects the parent for `worktree create`. Empty
	// passes --no-parent.
	ParentWorktree string
	// ReadyWindow bounds the wait for the agent to appear.
	ReadyWindow time.Duration
	// ReceiptWindow bounds the wait for a verified model receipt when a
	// role requires one.
	ReceiptWindow time.Duration
	// PollInterval is the gap between two readiness or receipt reads.
	PollInterval time.Duration
	// QuietPeriod is the KTD8 quiet period.
	QuietPeriod time.Duration
	// ClaudeRoot is Claude Code's transcript store, ~/.claude/projects.
	ClaudeRoot string
	// CodexRoot is Codex's rollout store, ~/.codex/sessions.
	CodexRoot string
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Sleep waits between polls. Nil means a timer that ctx cancels.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Host is the orca agent host.
type Host struct {
	cfg Config
	run Runner
	// wall is the filesystem's clock, which dates a rollout's change time.
	// cfg.Now is the scheduler's clock, and a test moves it apart from
	// the files. Tests set wall through export_test.go.
	wall func() time.Time
	// heads caches the head of each Codex rollout (rollouthead.go).
	heads rolloutHeads
	// headReads counts the rollout heads read from disk.
	headReads atomic.Int64
}

var (
	_ agenthost.Host        = (*Host)(nil)
	_ agenthost.QuotaReader = (*Host)(nil)
)

// New checks cfg and fills its defaults.
func New(cfg Config) (*Host, error) {
	if cfg.Runner == nil {
		if cfg.Bin == "" {
			return nil, fmt.Errorf("%w: orca config names no binary", agenthost.ErrBadSpec)
		}
		cfg.Runner = ExecRunner{Bin: cfg.Bin}
	}
	if cfg.CheckoutRoot == "" || !filepath.IsAbs(cfg.CheckoutRoot) {
		return nil, fmt.Errorf("%w: checkout root %q is not absolute", agenthost.ErrBadSpec, cfg.CheckoutRoot)
	}
	cfg.CheckoutRoot = filepath.Clean(cfg.CheckoutRoot)
	if cfg.ReadyWindow == 0 {
		cfg.ReadyWindow = agenthost.DefaultReadyWindow
	}
	if cfg.ReceiptWindow == 0 {
		cfg.ReceiptWindow = 3 * time.Minute
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.QuietPeriod == 0 {
		cfg.QuietPeriod = agenthost.DefaultQuietPeriod
	}
	home, _ := os.UserHomeDir()
	if cfg.ClaudeRoot == "" {
		cfg.ClaudeRoot = filepath.Join(home, ".claude", "projects")
	}
	if cfg.CodexRoot == "" {
		cfg.CodexRoot = filepath.Join(home, ".codex", "sessions")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	return &Host{cfg: cfg, run: cfg.Runner, wall: time.Now}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// hostPrefix makes orca's executionHostId into the RunID host part.
const hostPrefix = "orca:"

// envelope is orca's JSON answer.
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Meta struct {
		RuntimeID string `json:"runtimeId"`
	} `json:"_meta"`
}

// Orca's error codes: CodeStale for a handle it does not resolve,
// CodeNotFound for a selector that matches nothing.
const (
	CodeStale    = "terminal_handle_stale"
	CodeNotFound = "selector_not_found"
)

// Error is a refusal orca reported in its envelope.
type Error struct {
	Verb string
	Code string
	Exit int
}

var safeCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,95}$`)

func (e *Error) Error() string {
	code := e.Code
	if !safeCode.MatchString(code) {
		code = "unrecognized"
	}
	return fmt.Sprintf("orca %s refused: %s (exit %d)", e.Verb, code, e.Exit)
}

// Is maps a stale handle to agenthost.ErrStaleHandle, and every other
// refusal to agenthost.ErrHostUnavailable.
func (e *Error) Is(target error) bool {
	if e.Code == CodeStale {
		return target == agenthost.ErrStaleHandle
	}
	return target == agenthost.ErrHostUnavailable
}

// call runs one orca command. It returns the envelope whenever orca printed
// one, even with an error, because orca prints a refusal's receipt on stdout.
func (h *Host) call(ctx context.Context, args ...string) (envelope, error) {
	var env envelope
	out, err := h.run.Run(ctx, args)
	if err != nil {
		return env, fmt.Errorf("%w: %w", agenthost.ErrHostUnavailable, err)
	}
	if jerr := json.Unmarshal(out.Stdout, &env); jerr != nil {
		return envelope{}, fmt.Errorf("%w: orca %s printed no valid JSON (exit %d)",
			agenthost.ErrHostUnavailable, verb(args), out.Code)
	}
	if !env.OK {
		code := ""
		if env.Error != nil {
			code = env.Error.Code
		}
		return env, &Error{Verb: verb(args), Code: code, Exit: out.Code}
	}
	if out.Code != 0 {
		return env, fmt.Errorf("%w: orca %s answered ok with exit %d",
			agenthost.ErrHostUnavailable, verb(args), out.Code)
	}
	return env, nil
}

// verb names the command, never its arguments, which may hold text.
func verb(args []string) string {
	n := min(2, len(args))
	return strings.Join(args[:n], " ")
}

// terminal is one terminal as `terminal show` and `terminal list` print it.
type terminal struct {
	Handle          string `json:"handle"`
	PtyID           string `json:"ptyId"`
	IncarnationID   string `json:"incarnationId"`
	WorktreePath    string `json:"worktreePath"`
	ExecutionHostID string `json:"executionHostId"`
	AgentIdentity   string `json:"agentIdentity"`
	Connected       bool   `json:"connected"`
	Orphaned        bool   `json:"orphaned"`
}

func (t terminal) id() agenthost.RunID {
	return agenthost.RunID{Host: hostPrefix + t.ExecutionHostID, Terminal: t.Handle, Incarnation: t.IncarnationID}
}

// show reads one terminal and checks that it is the session's own
// incarnation on the session's host. Anything else is a stale handle.
func (h *Host) show(ctx context.Context, id agenthost.RunID) (terminal, error) {
	env, err := h.call(ctx, "terminal", "show", "--terminal", id.Terminal, "--json")
	if err != nil {
		return terminal{}, err
	}
	var res struct {
		Terminal *terminal `json:"terminal"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil || res.Terminal == nil {
		return terminal{}, fmt.Errorf("%w: orca terminal show printed no terminal", agenthost.ErrHostUnavailable)
	}
	if res.Terminal.id() != id {
		return *res.Terminal, fmt.Errorf("%w: handle %s now names %s", agenthost.ErrStaleHandle, id.Terminal, res.Terminal.id())
	}
	return *res.Terminal, nil
}
