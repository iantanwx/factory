// Package fake is an in-memory tracker.Adapter for tests.
//
// It keeps one projected state for each project and dedupes events by
// journal Seq, so a replayed batch applies once, as a real idempotent
// adapter must. Knobs make the next calls fail, lose their result, return a
// rate limit, or hang until their context ends.
package fake

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/tracker"
)

// ErrLostResult is what a call returns after LoseResultNext applied it.
var ErrLostResult = errors.New("fake: connection dropped after the apply")

// Adapter is the fake. The zero value is not usable; call New.
type Adapter struct {
	mu         sync.Mutex
	projects   map[string]*project
	calls      int
	fail       []error
	lose       int
	hang       bool
	reconciles map[string]int
}

type project struct {
	seen    map[int64]bool
	applied []tracker.Event
	state   map[string]tracker.Item
}

var _ tracker.Adapter = (*Adapter)(nil)

// New returns an empty fake.
func New() *Adapter {
	return &Adapter{projects: map[string]*project{}, reconciles: map[string]int{}}
}

func (a *Adapter) project(id string) *project {
	p, ok := a.projects[id]
	if !ok {
		p = &project{seen: map[int64]bool{}, state: map[string]tracker.Item{}}
		a.projects[id] = p
	}
	return p
}

// enter counts the call and returns the error a knob injects. A hung call
// returns the context's error after the context ends.
func (a *Adapter) enter(ctx context.Context) (lose bool, err error) {
	a.mu.Lock()
	a.calls++
	hang := a.hang
	if len(a.fail) > 0 {
		err = a.fail[0]
		a.fail = a.fail[1:]
	} else if a.lose > 0 {
		a.lose--
		lose = true
	}
	a.mu.Unlock()
	if err != nil {
		return false, err
	}
	if hang {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return lose, ctx.Err()
}

// Apply implements tracker.Adapter. An event whose Seq the project already
// applied is skipped.
func (a *Adapter) Apply(ctx context.Context, project string, batch []tracker.Event) error {
	lose, err := a.enter(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	p := a.project(project)
	for _, e := range batch {
		if p.seen[e.Seq] {
			continue
		}
		p.seen[e.Seq] = true
		p.applied = append(p.applied, copyEvent(e))
		if e.State == nil {
			delete(p.state, e.Item)
		} else {
			p.state[e.Item] = copyItem(*e.State)
		}
	}
	a.mu.Unlock()
	if lose {
		return ErrLostResult
	}
	return nil
}

// Reconcile implements tracker.Adapter. The project's state becomes items.
func (a *Adapter) Reconcile(ctx context.Context, project string, items []tracker.Item) error {
	lose, err := a.enter(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	p := a.project(project)
	p.state = make(map[string]tracker.Item, len(items))
	for _, it := range items {
		p.state[it.ID] = copyItem(it)
	}
	a.reconciles[project]++
	a.mu.Unlock()
	if lose {
		return ErrLostResult
	}
	return nil
}

// FailNext makes the next n calls return err and apply nothing.
func (a *Adapter) FailNext(n int, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for range n {
		a.fail = append(a.fail, err)
	}
}

// LoseResultNext makes the next n calls apply and then return
// ErrLostResult, as a connection that drops after the tracker committed.
func (a *Adapter) LoseResultNext(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lose = n
}

// RateLimitNext makes the next call return tracker.RateLimited(after) and
// apply nothing.
func (a *Adapter) RateLimitNext(after time.Duration) { a.FailNext(1, tracker.RateLimited(after)) }

// SetHang makes every call block until its context ends.
func (a *Adapter) SetHang(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hang = on
}

// Applied returns the events applied for project, once each, in apply order.
func (a *Adapter) Applied(project string) []tracker.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.projects[project]
	if !ok {
		return nil
	}
	out := make([]tracker.Event, 0, len(p.applied))
	for _, e := range p.applied {
		out = append(out, copyEvent(e))
	}
	return out
}

// State returns the projected item state of project.
func (a *Adapter) State(project string) map[string]tracker.Item {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]tracker.Item{}
	if p, ok := a.projects[project]; ok {
		for id, it := range p.state {
			out[id] = copyItem(it)
		}
	}
	return out
}

// Calls returns how many calls reached the adapter, failed ones included.
func (a *Adapter) Calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// Reconciles returns how many reconcile calls applied for project.
func (a *Adapter) Reconciles(project string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reconciles[project]
}

func copyItem(it tracker.Item) tracker.Item {
	it.Metadata = maps.Clone(it.Metadata)
	return it
}

func copyEvent(e tracker.Event) tracker.Event {
	if e.State != nil {
		it := copyItem(*e.State)
		e.State = &it
	}
	return e
}
