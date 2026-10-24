package orca

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iantanwx/loop/internal/agenthost"
)

// Start launches the harness as an orca agent in the checkout, waits for
// orca to name the agent in its terminal, and, for an escalated role, waits
// for a verified model receipt. orca's agent launch starts the harness the
// way a new agent tab does, with orca's own flags for the agent, so orca
// tracks it as an agent and its permission prompts stay off for a run that
// no person attends. The launch takes no title, so the title follows.
// Ported from startAgent in alpha's scripts/sdlc/agent-start.ts and
// startArgs in apps/slothbot/src/supervise/child.ts.
func (h *Host) Start(ctx context.Context, spec agenthost.StartSpec) (agenthost.Session, error) {
	if err := spec.Validate(); err != nil {
		return agenthost.Session{}, err
	}
	if !agenthost.InsideRoot(h.cfg.CheckoutRoot, spec.Checkout) {
		return agenthost.Session{}, fmt.Errorf("%w: %s", agenthost.ErrOutsideRoot, spec.Checkout)
	}
	if fi, err := os.Stat(spec.PromptFile); err != nil || !fi.Mode().IsRegular() {
		return agenthost.Session{}, fmt.Errorf("%w: prompt file %s is not a readable file", agenthost.ErrBadSpec, spec.PromptFile)
	}
	checkout := filepath.Clean(spec.Checkout)
	title := agenthost.Label(spec.Title)
	if title == "" {
		title = "loop " + string(spec.Kind)
	}
	started := h.cfg.Now()
	env, err := h.call(ctx, LaunchArgs(spec, checkout)...)
	if err != nil {
		return agenthost.Session{}, err
	}
	var res struct {
		Outcome *struct {
			Kind   string `json:"kind"`
			Handle string `json:"handle"`
		} `json:"outcome"`
		Prompt *struct {
			Outcome string `json:"outcome"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil || res.Outcome == nil || res.Outcome.Handle == "" {
		return agenthost.Session{}, fmt.Errorf("%w: orca agent launch printed no handle", agenthost.ErrHostUnavailable)
	}
	if res.Outcome.Kind != "terminal" {
		return agenthost.Session{}, fmt.Errorf("%w: orca started the agent as a %s, not a terminal", agenthost.ErrHostUnavailable, res.Outcome.Kind)
	}
	t, err := h.showHandle(ctx, res.Outcome.Handle)
	if err != nil {
		return agenthost.Session{ID: agenthost.RunID{Terminal: res.Outcome.Handle}, Checkout: checkout}, err
	}
	s := agenthost.Session{
		ID: t.id(), Kind: spec.Kind, Checkout: checkout, Harness: spec.Harness,
		Model: spec.Model, Effort: spec.Effort, Pty: t.PtyID, Epoch: env.Meta.RuntimeID, StartedAt: started,
	}
	if !s.ID.Valid() {
		// The terminal carried no incarnation or host. The handle still
		// names a terminal that may run, so return what is known.
		return s, fmt.Errorf("%w: orca printed no full identity for %s", agenthost.ErrHostUnavailable, t.Handle)
	}
	if res.Prompt == nil || (res.Prompt.Outcome != "handed-to-terminal" && res.Prompt.Outcome != "journaled") {
		h.abandon(s.ID)
		return s, fmt.Errorf("%w: orca did not hand the prompt to %s", agenthost.ErrNotReady, s.ID)
	}
	_, _ = h.call(ctx, "terminal", "rename", "--terminal", t.Handle, "--title", title, "--json")
	if err := h.awaitReady(ctx, s); err != nil {
		h.abandon(s.ID)
		return s, err
	}
	if spec.RequireVerifiedModel {
		if err := h.awaitReceipt(ctx, s); err != nil {
			h.abandon(s.ID)
			return s, err
		}
	}
	return s, nil
}

// LaunchArgs is the orca agent launch of spec in checkout. The prompt is
// the session's first message: its title and the prompt file's name, so no
// secret reaches the process arguments.
func LaunchArgs(spec agenthost.StartSpec, checkout string) []string {
	args := []string{"agent", "launch", "--agent", string(spec.Harness), "--worktree", "path:" + checkout,
		"--model", spec.Model}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	return append(args, "--surface", "terminal", "--prompt", agenthost.FirstMessage(spec), "--json")
}

// showHandle reads the terminal a handle names now.
func (h *Host) showHandle(ctx context.Context, handle string) (terminal, error) {
	env, err := h.call(ctx, "terminal", "show", "--terminal", handle, "--json")
	if err != nil {
		return terminal{}, err
	}
	var res struct {
		Terminal *terminal `json:"terminal"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil || res.Terminal == nil {
		return terminal{}, fmt.Errorf("%w: orca terminal show printed no terminal", agenthost.ErrHostUnavailable)
	}
	return *res.Terminal, nil
}

// awaitReady polls `terminal show` until orca names the harness as the
// agent in this exact terminal. A shell that printed an error is still a
// running terminal, so the agent identity, not the status, is the answer.
func (h *Host) awaitReady(ctx context.Context, s agenthost.Session) error {
	deadline := h.cfg.Now().Add(h.cfg.ReadyWindow)
	why := "orca named no agent"
	for {
		t, err := h.show(ctx, s.ID)
		switch {
		case err == nil && t.AgentIdentity == string(s.Harness):
			return nil
		case err == nil && t.AgentIdentity != "":
			why = fmt.Sprintf("orca names agent %q, not %q", t.AgentIdentity, s.Harness)
		case err != nil:
			why = err.Error()
		}
		if !h.cfg.Now().Before(deadline) {
			return fmt.Errorf("%w: %s in %s after %s: %s", agenthost.ErrNotReady, s.ID, s.Checkout, h.cfg.ReadyWindow, why)
		}
		if err := h.cfg.Sleep(ctx, h.cfg.PollInterval); err != nil {
			return fmt.Errorf("%w: %w", agenthost.ErrNotReady, err)
		}
	}
}

// awaitReceipt polls the model receipt until it reads verified.
func (h *Host) awaitReceipt(ctx context.Context, s agenthost.Session) error {
	deadline := h.cfg.Now().Add(h.cfg.ReceiptWindow)
	for {
		rc, err := h.ModelReceipt(ctx, s)
		if err == nil && rc.Status == agenthost.Verified {
			return nil
		}
		if rc.Observed != "" && rc.Observed != s.Model {
			return fmt.Errorf("%w: requested %s, the harness records %s", agenthost.ErrModelUnverified, s.Model, rc.Observed)
		}
		if !h.cfg.Now().Before(deadline) {
			return fmt.Errorf("%w: requested %s: %s", agenthost.ErrModelUnverified, s.Model, rc.Reason)
		}
		if err := h.cfg.Sleep(ctx, h.cfg.PollInterval); err != nil {
			return fmt.Errorf("%w: %w", agenthost.ErrModelUnverified, err)
		}
	}
}

// abandon closes a session that did not start. It uses a fresh context, so
// a cancelled start still asks orca to close what it made. A failure is
// left for the inventory to find.
func (h *Host) abandon(id agenthost.RunID) {
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.ReadyWindow)
	defer cancel()
	_, _ = h.Stop(ctx, id)
}
