package orca

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/agenthost"
)

// StartCommand is the text `orca terminal create --command` types into the
// login shell orca starts. It names the prompt file and holds no prompt
// text, so neither orca's argv, nor the shell, nor the harness's argv sees
// the prompt. Alpha's startCommand ran `claude "$(cat file)"`, which put
// the prompt into the harness's argv. This one does not.
//
// `exec` replaces the shell with the harness, so the PTY ends when the
// harness ends. ADR 0025 (alpha) measured that without exec orca reads a
// dead run as live, because its shell lives on.
func StartCommand(spec agenthost.StartSpec, extra []string) (string, error) {
	argv, err := agenthost.HarnessArgv(spec, extra)
	if err != nil {
		return "", err
	}
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = shellWord(a)
	}
	return "exec " + strings.Join(words, " "), nil
}

// shellWord single-quotes one word so every character arrives.
func shellWord(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Start creates a terminal in the checkout that runs the harness, waits for
// orca to name the agent in it, and, for an escalated role, waits for a
// verified model receipt. Ported from startAgent and awaitAgent in alpha's
// scripts/sdlc/agent-start.ts.
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
	command, err := StartCommand(spec, h.cfg.HarnessArgs[spec.Harness])
	if err != nil {
		return agenthost.Session{}, err
	}
	checkout := filepath.Clean(spec.Checkout)
	title := spec.Title
	if title == "" {
		title = "loop " + string(spec.Kind)
	}
	started := h.cfg.Now()
	env, err := h.call(ctx, "terminal", "create", "--worktree", "path:"+checkout,
		"--title", title, "--command", command, "--json")
	if err != nil {
		return agenthost.Session{}, err
	}
	var res struct {
		Terminal *terminal `json:"terminal"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil || res.Terminal == nil || res.Terminal.Handle == "" {
		return agenthost.Session{}, fmt.Errorf("%w: orca terminal create printed no handle", agenthost.ErrHostUnavailable)
	}
	t := *res.Terminal
	s := agenthost.Session{
		ID: t.id(), Kind: spec.Kind, Checkout: checkout, Harness: spec.Harness,
		Model: spec.Model, Effort: spec.Effort, Pty: t.PtyID, Epoch: env.Meta.RuntimeID, StartedAt: started,
	}
	if !s.ID.Valid() {
		// The create receipt carried no incarnation or host. The handle
		// still names a terminal that may run, so return what is known.
		return s, fmt.Errorf("%w: orca terminal create printed no full identity for %s", agenthost.ErrHostUnavailable, t.Handle)
	}
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
