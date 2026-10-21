package orca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iantanwx/loop/internal/agenthost"
)

// Notify types text into the session and presses Enter, through
// `orca terminal send --enter`. Ported from wake in alpha's
// supervisor-wake.ts.
//
// It first proves the handle still names the session's own incarnation, so
// a doorbell never reaches a replacement terminal. Success needs orca's
// receipt with result.send.accepted == true. Orca exits 1 on a refusal and
// still prints the receipt on stdout. A refusal, a held input (a mobile
// client drives the terminal) and an unreadable receipt all return an error
// wrapping agenthost.ErrNotifyRefused. Accepted proves input acceptance,
// not a started turn. The inbox stays pull-durable (KTD21).
func (h *Host) Notify(ctx context.Context, id agenthost.RunID, text string) error {
	if text == "" {
		return fmt.Errorf("%w: %w: empty notify text", agenthost.ErrNotifyRefused, agenthost.ErrBadSpec)
	}
	if _, err := h.show(ctx, id); err != nil {
		return fmt.Errorf("%w: %w", agenthost.ErrNotifyRefused, err)
	}
	env, err := h.call(ctx, "terminal", "send", "--terminal", id.Terminal, "--text", text, "--enter", "--json")
	var oe *Error
	if err != nil && !errors.As(err, &oe) {
		return fmt.Errorf("%w: %w", agenthost.ErrNotifyRefused, err)
	}
	var res struct {
		Send *struct {
			Accepted      *bool  `json:"accepted"`
			BytesWritten  *int   `json:"bytesWritten"`
			RefusedReason string `json:"refusedReason"`
		} `json:"send"`
	}
	if env.Result != nil {
		_ = json.Unmarshal(env.Result, &res)
	}
	if err == nil && res.Send != nil && res.Send.Accepted != nil && *res.Send.Accepted {
		return nil
	}
	why := "orca did not confirm input acceptance"
	switch {
	case oe != nil:
		why = oe.Error()
	case res.Send != nil && res.Send.Accepted != nil && res.Send.BytesWritten != nil && *res.Send.BytesWritten == 0 && res.Send.RefusedReason == "":
		why = "orca held the input before any write; a mobile client may drive the terminal"
	case res.Send != nil && safeCode.MatchString(res.Send.RefusedReason):
		why = "orca refused the input: " + res.Send.RefusedReason
	}
	return fmt.Errorf("%w: %s: %s", agenthost.ErrNotifyRefused, id.Terminal, why)
}
