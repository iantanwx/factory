package overwatch

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/inbox"
)

// JobName is the native job of the sweep (config.NativeJobs).
const JobName = "overwatch"

// Bell types text into the session id for the reports of the destinations
// dests. ok is false when no session serves them, and then it rings
// nobody. The server records the ring of each destination.
type Bell func(ctx context.Context, id agenthost.RunID, ok bool, dests []string, text string) error

// Job is the native job `overwatch`. Tick is a sched.InstanceJobFunc.
type Job struct {
	World World
	Parts []Part
	// Topology and Sessions name the session that serves each destination.
	// A nil Topology has no loop project, and a nil Sessions serves none.
	Topology func() coord.Topology
	Sessions inbox.Sessions
	Ring     Bell

	mu   sync.Mutex
	last *Report
}

// Tick sweeps once, and splits the sweep by destination (spec
// factorio-1edo, Approach, "The overwatch ring"). Each session that serves
// a destination with code 1 or 2 gets one ring, with the RingText of the
// destinations it serves. A refused ring is never typed again in its tick:
// the next tick sweeps again, and the inbox part reads the refusal. With
// no session, or a ring the backoff holds, the ring returns no error.
func (j *Job) Tick(ctx context.Context) error {
	rs := ReadAll(ctx, j.World, j.Parts)
	r := rs.Report()
	j.mu.Lock()
	j.last = &r
	j.mu.Unlock()
	var t coord.Topology
	if j.Topology != nil {
		t = j.Topology()
	}
	type session struct {
		id    agenthost.RunID
		ok    bool
		dests []string
	}
	var rings []*session
	split := Split(rs, t)
	for _, dest := range slices.Sorted(maps.Keys(split)) {
		if split[dest].Code == Quiet {
			continue
		}
		var s session
		if j.Sessions != nil {
			s.id, s.ok = j.Sessions(dest)
		}
		i := slices.IndexFunc(rings, func(o *session) bool { return o.id == s.id && o.ok == s.ok })
		if i < 0 {
			i, rings = len(rings), append(rings, &s)
		}
		rings[i].dests = append(rings[i].dests, dest)
	}
	var errs []error
	for _, s := range rings {
		text, _ := RingText(rs.For(t, func(d string) bool { return slices.Contains(s.dests, d) }).Report())
		if err := j.Ring(ctx, s.id, s.ok, s.dests, text); err != nil && !errors.Is(err, inbox.ErrNobodyToRing) && !errors.Is(err, inbox.ErrRingHeld) {
			errs = append(errs, fmt.Errorf("overwatch: ring the coordinator session %s: %w", s.id, err))
		}
	}
	return errors.Join(errs...)
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
