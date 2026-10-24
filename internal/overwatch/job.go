package overwatch

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/iantanwx/loop/internal/inbox"
)

// JobName is the native job of the sweep (config.NativeJobs).
const JobName = "overwatch"

// Job is the native job `overwatch`. Tick is a sched.InstanceJobFunc.
type Job struct {
	World World
	Parts []Part
	Ring  inbox.Doorbell // the server's recordedRing("overwatch")

	mu   sync.Mutex
	last *Report
}

// Tick sweeps once. A report with code 0 rings nobody. A report with code 1
// or 2 rings once with RingText. A refused ring is never typed again in its
// tick: the next tick sweeps again, and the inbox part reads the refusal.
// With no coordinator session, or a ring the backoff holds, the tick
// returns no error.
func (j *Job) Tick(ctx context.Context) error {
	r := Sweep(ctx, j.World, j.Parts)
	j.mu.Lock()
	j.last = &r
	j.mu.Unlock()
	text, ok := RingText(r)
	if !ok {
		return nil
	}
	if err := j.Ring(ctx, text); err != nil && !errors.Is(err, inbox.ErrNobodyToRing) && !errors.Is(err, inbox.ErrRingHeld) {
		return fmt.Errorf("overwatch: ring the coordinator: %w", err)
	}
	return nil
}

// Last returns the report of the last tick.
func (j *Job) Last() (Report, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.last == nil {
		return Report{}, false
	}
	return *j.last, true
}
