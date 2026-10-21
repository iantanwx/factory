//go:build acceptance

package acceptance

import (
	"context"

	"github.com/iantanwx/loop/internal/beadsx"
)

// The hooks let a scenario act at one exact point of a write: after the
// server read an item from beads, after beads applied an update, or in the
// server's stop. Each hook fires once and then disarms. A hook runs on the
// server's own goroutine, so it may block that call.

type hookPoint int

const (
	hookGet hookPoint = iota
	hookUpdated
	hookStop
)

type armed struct {
	item string
	fn   func(ctx context.Context)
}

type beadsHooks struct {
	get, updated, stop *armed
}

// AfterGet runs fn once, after the server's next bead read of item and
// before that read returns. The read returns what it read before fn ran.
func (w *World) AfterGet(item string, fn func(ctx context.Context)) { w.arm(&w.hooks.get, item, fn) }

// AfterUpdate runs fn once, after beads applied the server's next update
// of item and before the update returns. ctx is the update's context.
func (w *World) AfterUpdate(item string, fn func(ctx context.Context)) {
	w.arm(&w.hooks.updated, item, fn)
}

// InStop runs fn once, in the server's stop steps: after its RPC server
// stopped and before its store closes.
func (w *World) InStop(fn func()) { w.arm(&w.hooks.stop, "", func(context.Context) { fn() }) }

func (w *World) arm(slot **armed, item string, fn func(ctx context.Context)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*slot = &armed{item: item, fn: fn}
}

func (w *World) fire(ctx context.Context, p hookPoint, item string) {
	w.mu.Lock()
	slot := map[hookPoint]**armed{hookGet: &w.hooks.get, hookUpdated: &w.hooks.updated, hookStop: &w.hooks.stop}[p]
	a := *slot
	if a == nil || a.item != item {
		w.mu.Unlock()
		return
	}
	*slot = nil
	w.mu.Unlock()
	a.fn(ctx)
}

// hooked is a beads client that fires the world's hooks.
type hooked struct {
	beadsx.Client
	w *World
}

func (h *hooked) Get(ctx context.Context, id string) (beadsx.Issue, error) {
	is, err := h.Client.Get(ctx, id)
	if err == nil {
		h.w.fire(ctx, hookGet, id)
	}
	return is, err
}

func (h *hooked) Update(ctx context.Context, id string, spec beadsx.UpdateSpec) (beadsx.Issue, error) {
	is, err := h.Client.Update(ctx, id, spec)
	if err == nil {
		h.w.fire(ctx, hookUpdated, id)
	}
	return is, err
}
