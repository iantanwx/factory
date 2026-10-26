package orca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/iantanwx/loop/internal/agenthost"
)

var checkoutName = regexp.MustCompile(fmt.Sprintf(`^[A-Za-z0-9][A-Za-z0-9._-]{0,%d}$`, agenthost.MaxCheckoutName-1))

// CreateCheckout runs `orca worktree create` with no --agent and no
// --prompt. Alpha's scripts/sdlc/agent-start.ts records why: orca joins
// --setup run and an agent launch into one shell line, and a `#` comment in
// it swallows the launch. So the checkout comes first, and Start starts the
// agent in its own call.
func (h *Host) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	if !checkoutName.MatchString(spec.Name) {
		return agenthost.Checkout{}, fmt.Errorf("%w: checkout name %q", agenthost.ErrBadSpec, spec.Name)
	}
	args := []string{"worktree", "create", "--name", spec.Name}
	if h.cfg.Repo != "" {
		args = append(args, "--repo", h.cfg.Repo)
	}
	if h.cfg.ParentWorktree != "" {
		args = append(args, "--parent-worktree", h.cfg.ParentWorktree)
	} else {
		args = append(args, "--no-parent")
	}
	if spec.BaseBranch != "" {
		args = append(args, "--base-branch", spec.BaseBranch)
	}
	setup := "skip"
	if spec.Setup {
		setup = "run"
	}
	args = append(args, "--setup", setup, "--json")
	env, err := h.call(ctx, args...)
	if err != nil {
		return agenthost.Checkout{}, err
	}
	var res struct {
		Worktree *struct {
			ID     string `json:"id"`
			Path   string `json:"path"`
			Branch string `json:"branch"`
		} `json:"worktree"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil || res.Worktree == nil || res.Worktree.Path == "" {
		return agenthost.Checkout{}, fmt.Errorf("%w: orca worktree create printed no path", agenthost.ErrHostUnavailable)
	}
	co := agenthost.Checkout{Path: filepath.Clean(res.Worktree.Path), Branch: res.Worktree.Branch, Ref: res.Worktree.ID}
	if !agenthost.InsideRoot(h.cfg.CheckoutRoot, co.Path) {
		// The checkout exists now. The caller decides; this never deletes
		// a path outside the root.
		return co, fmt.Errorf("%w: orca made %s, outside %s", agenthost.ErrOutsideRoot, co.Path, h.cfg.CheckoutRoot)
	}
	return co, nil
}

// RemoveCheckout runs `orca worktree rm --force`. It refuses a path outside
// the root without asking orca, and a checkout that a complete inventory
// does not prove empty, because `worktree rm` also stops its terminals. A
// path orca lists no worktree for is agenthost.ErrUnknownCheckout.
func (h *Host) RemoveCheckout(ctx context.Context, path string) error {
	if !agenthost.InsideRoot(h.cfg.CheckoutRoot, path) {
		return fmt.Errorf("%w: %s", agenthost.ErrOutsideRoot, path)
	}
	clean := filepath.Clean(path)
	inv, err := h.Inventory(ctx)
	if err != nil || !inv.Complete {
		return fmt.Errorf("%w: cannot prove %s empty: %s", agenthost.ErrCheckoutBusy, clean, inv.Why)
	}
	shells := false
	for _, r := range inv.Rows {
		if filepath.Clean(r.Checkout) != clean {
			continue
		}
		if r.Agent != "" {
			return fmt.Errorf("%w: %s holds agent terminal %s", agenthost.ErrCheckoutBusy, clean, r.ID.Terminal)
		}
		shells = true
	}
	// orca opens a shell in each new worktree. A shell that runs no agent
	// holds no work, so it closes with the checkout.
	if shells {
		if _, err := h.call(ctx, "terminal", "close", "--worktree", "path:"+clean, "--all", "--json"); err != nil {
			return fmt.Errorf("%w: close the shells in %s: %w", agenthost.ErrCheckoutBusy, clean, err)
		}
	}
	_, err = h.call(ctx, "worktree", "rm", "--worktree", "path:"+clean, "--force", "--json")
	if oe := (*Error)(nil); errors.As(err, &oe) && oe.Code == CodeNotFound {
		return fmt.Errorf("%w: orca lists no worktree at %s", agenthost.ErrUnknownCheckout, clean)
	}
	return err
}
