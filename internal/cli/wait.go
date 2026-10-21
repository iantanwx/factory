package cli

import (
	"context"
	"fmt"
	"os"
	"time"
)

// DefaultWaitBudget is how long the CLI waits for an unreachable or
// restarting server before it exits 4 (KTD13). It stays below Claude Code's
// default 120-second tool timeout, so the agent sees exit 4 and not a
// killed tool call.
const DefaultWaitBudget = 100 * time.Second

// WaitBudget is the budget the CLI uses. Tests shrink it.
var WaitBudget = DefaultWaitBudget

// WaitBudgetEnv shrinks the budget of one CLI process, for tests that drive
// the real binary. A value above DefaultWaitBudget is ignored.
const WaitBudgetEnv = "LOOP_WAIT_BUDGET"

// RetryAfterSeconds is what an exit 4 for an unreachable server tells the
// caller to wait before it runs the same command again.
const RetryAfterSeconds = 5

// effectiveBudget is WaitBudget, or a shorter LOOP_WAIT_BUDGET.
func effectiveBudget() time.Duration {
	b := WaitBudget
	if raw := os.Getenv(WaitBudgetEnv); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 && d < b {
			b = d
		}
	}
	if b > DefaultWaitBudget {
		b = DefaultWaitBudget
	}
	return b
}

// backoff yields the waits between attempts: 100 ms, doubling, at most 2 s.
type backoff struct{ next time.Duration }

func (b *backoff) wait() time.Duration {
	switch {
	case b.next == 0:
		b.next = 100 * time.Millisecond
	case b.next < 2*time.Second:
		b.next *= 2
		if b.next > 2*time.Second {
			b.next = 2 * time.Second
		}
	}
	return b.next
}

// sleepUntil waits d, or less when the deadline or ctx comes first. It
// reports whether another attempt fits before the deadline.
func sleepUntil(ctx context.Context, d time.Duration, deadline time.Time) bool {
	left := time.Until(deadline)
	if left <= 0 {
		return false
	}
	if d > left {
		d = left
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return time.Now().Before(deadline)
	}
}

// unavailable is the exit 4 answer after the wait budget runs out.
func unavailable(socket string, budget time.Duration, last error) Result {
	hint := fmt.Sprintf("no ready server answered on %s within %s; run the same command again", socket, budget)
	if last != nil {
		hint += fmt.Sprintf(" (last error: %v)", last)
	}
	return Refuse(Refusal{
		Code: CodeServerUnavailable, Rule: "server_unreachable", Hint: hint,
		RetryAfterSeconds: RetryAfterSeconds,
	})
}
