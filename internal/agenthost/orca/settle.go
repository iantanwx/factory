package orca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iantanwx/loop/internal/agenthost"
)

// CodeTimeout is orca's error code when `terminal wait` times out.
const CodeTimeout = "timeout"

// Settle reads one session's liveness.
//
//  1. `terminal show` must resolve the handle to the same incarnation on
//     the same host. A stale handle, another incarnation and a failed read
//     settle Unknown, never Exited.
//  2. `terminal wait --for exit --timeout-ms 1` then asks whether the
//     process exited. A success is the proof of exit. orca's timeout means
//     the process still runs.
func (h *Host) Settle(ctx context.Context, id agenthost.RunID) (agenthost.Settlement, error) {
	t, err := h.show(ctx, id)
	if err != nil {
		return unknown(err), err
	}
	st, err := h.exitProbe(ctx, id)
	if err != nil {
		return st, err
	}
	if st.Liveness == agenthost.Live && !t.Connected {
		err := fmt.Errorf("%w: orca shows %s disconnected", agenthost.ErrHostUnavailable, id.Terminal)
		return unknown(err), err
	}
	return st, nil
}

func unknown(err error) agenthost.Settlement {
	return agenthost.Settlement{Liveness: agenthost.Unknown, Reason: err.Error()}
}

// exitProbe runs a 1 ms `terminal wait --for exit`.
func (h *Host) exitProbe(ctx context.Context, id agenthost.RunID) (agenthost.Settlement, error) {
	env, err := h.call(ctx, "terminal", "wait", "--terminal", id.Terminal, "--for", "exit", "--timeout-ms", "1", "--json")
	var oe *Error
	if errors.As(err, &oe) && oe.Code == CodeTimeout {
		return agenthost.Settlement{Liveness: agenthost.Live}, nil
	}
	if err != nil {
		return unknown(err), err
	}
	return agenthost.Settlement{
		Liveness: agenthost.Exited, ExitCode: exitCode(env.Result), Reason: "orca reported the process exit",
	}, nil
}

// exitCode finds the first numeric "exitCode" anywhere in a result. Orca
// documents no path for it, so this searches, and an absent code stays nil.
func exitCode(raw json.RawMessage) *int {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var find func(any) *int
	find = func(v any) *int {
		switch x := v.(type) {
		case map[string]any:
			if n, ok := x["exitCode"].(float64); ok {
				c := int(n)
				return &c
			}
			for _, e := range x {
				if c := find(e); c != nil {
					return c
				}
			}
		case []any:
			for _, e := range x {
				if c := find(e); c != nil {
					return c
				}
			}
		}
		return nil
	}
	return find(v)
}

// Stop ends the session and reports Exited only on proven exit. It closes
// only the session's own incarnation: a handle that resolves to anything
// else is never closed. The proof is orca's own: an exit the wait reports,
// or a close orca confirmed and after which the handle no longer resolves
// to a running process.
func (h *Host) Stop(ctx context.Context, id agenthost.RunID) (agenthost.Settlement, error) {
	if _, err := h.show(ctx, id); err != nil {
		return unknown(err), fmt.Errorf("%w: %w", agenthost.ErrExitUnproven, err)
	}
	if st, err := h.exitProbe(ctx, id); err == nil && st.Liveness == agenthost.Exited {
		return st, nil
	}
	if _, err := h.call(ctx, "terminal", "close", "--terminal", id.Terminal, "--json"); err != nil {
		return unknown(err), fmt.Errorf("%w: %w", agenthost.ErrExitUnproven, err)
	}
	t, err := h.show(ctx, id)
	if errors.Is(err, agenthost.ErrStaleHandle) {
		return agenthost.Settlement{Liveness: agenthost.Exited, Reason: "orca confirmed the close"}, nil
	}
	if err == nil {
		if st, perr := h.exitProbe(ctx, id); perr == nil && st.Liveness == agenthost.Exited {
			return st, nil
		}
		err = fmt.Errorf("orca still lists %s after the close (connected=%v)", id.Terminal, t.Connected)
	}
	return unknown(err), fmt.Errorf("%w: %w", agenthost.ErrExitUnproven, err)
}

// Close ends the session and keeps its checkout. `terminal close` stops one
// terminal and never touches the worktree.
func (h *Host) Close(ctx context.Context, id agenthost.RunID) error {
	_, err := h.Stop(ctx, id)
	return err
}
