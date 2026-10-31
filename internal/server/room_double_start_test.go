package server

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/room"
	"github.com/iantanwx/loop/internal/sched"
)

// watchDuringRoomCheckout runs one Watch pass while the first room
// checkout is being made, as the server's watch loop can. A second
// checkout of the same name gets a -2 suffix, as the real host does.
type watchDuringRoomCheckout struct {
	sched.CheckoutHost
	w     *humanWorld
	raced *atomic.Bool
	made  map[string]bool
	mu    *sync.Mutex
}

func (c watchDuringRoomCheckout) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	if strings.HasPrefix(spec.Name, "loop-room-") {
		if c.raced.CompareAndSwap(false, true) {
			if err := c.w.srv.Rooms.Watch(ctx); err != nil {
				c.w.t.Errorf("watch: %v", err)
			}
		}
		c.mu.Lock()
		if c.made[spec.Name] {
			spec.Name += "-2"
		}
		c.made[spec.Name] = true
		c.mu.Unlock()
	}
	return c.CheckoutHost.CreateCheckout(ctx, spec)
}

// One room open starts the room once. A watch pass that starts the same
// opening room while the open still makes its checkout leaves the room
// live with one session and its .env, and the open succeeds.
func TestRoomStartsOnceWhenWatchRacesTheOpen(t *testing.T) {
	w := newRoomWorld(t)
	orig := w.opts.Sched.Checkouts
	raced, mu, made := &atomic.Bool{}, &sync.Mutex{}, map[string]bool{}
	w.opts.Sched.Checkouts = func(p string) sched.CheckoutHost {
		return watchDuringRoomCheckout{CheckoutHost: orig(p), w: w, raced: raced, made: made, mu: mu}
	}
	w.restart()
	item := todoTicket(t, w.bx)
	env, code := w.coordinator("room", "open", "--kind", "review", "--project", "alpha", "--item", item,
		"--reason", "a design question", "--score", "4")
	if code != 0 {
		t.Errorf("room open = %d %v", code, env)
	}
	rooms, err := w.srv.Rooms.Rooms(context.Background(), false)
	if err != nil || len(rooms) != 1 {
		t.Fatalf("rooms = %+v %v", rooms, err)
	}
	got := rooms[0]
	if got.State != room.Live || got.Sessions != 1 || got.Checkout == "" || !hasEnv(t, got.Checkout) {
		t.Fatalf("room = %+v, want live with one session and its .env", got)
	}
	starts := 0
	for _, l := range w.host.Launches() {
		if l.Spec.Kind == agenthost.KindRoom {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("room sessions started = %d, want 1", starts)
	}
}
