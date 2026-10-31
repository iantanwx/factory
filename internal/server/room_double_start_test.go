package server

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/room"
	"github.com/iantanwx/loop/internal/sched"
)

// raceRoomCheckout runs race once while the first room checkout is being
// made. A second checkout of the same name gets a -2 suffix, as the real
// host does.
type raceRoomCheckout struct {
	sched.CheckoutHost
	race  func(context.Context)
	raced *atomic.Bool
	made  map[string]bool
	mu    *sync.Mutex
}

func (c raceRoomCheckout) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	if strings.HasPrefix(spec.Name, "loop-alpha-review-room-") {
		if c.raced.CompareAndSwap(false, true) {
			c.race(ctx)
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

// raceRoomStart restarts w with a checkout host that runs race during the
// first room checkout. The flag it returns says whether race ran.
func raceRoomStart(w *humanWorld, race func(context.Context)) *atomic.Bool {
	orig := w.opts.Sched.Checkouts
	raced, mu, made := &atomic.Bool{}, &sync.Mutex{}, map[string]bool{}
	w.opts.Sched.Checkouts = func(p string) sched.CheckoutHost {
		return raceRoomCheckout{CheckoutHost: orig(p), race: race, raced: raced, made: made, mu: mu}
	}
	w.restart()
	return raced
}

func reviewArgs(item, reason string) []string {
	return []string{"room", "open", "--kind", "review", "--project", "alpha", "--item", item,
		"--reason", reason, "--score", "4"}
}

func openReview(w *humanWorld, item, reason string) (map[string]any, int) {
	return w.coordinator(reviewArgs(item, reason)...)
}

// startedOnce fails t unless alpha holds one room, live with one session
// and its .env, and the host started one room session.
func startedOnce(t *testing.T, w *humanWorld) {
	t.Helper()
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

// One room open starts the room once. A watch pass that starts the same
// opening room while the open still makes its checkout leaves the room
// live with one session and its .env, and the open succeeds.
func TestRoomStartsOnceWhenWatchRacesTheOpen(t *testing.T) {
	w := newRoomWorld(t)
	raced := raceRoomStart(w, func(ctx context.Context) {
		if err := w.srv.Rooms.Watch(ctx); err != nil {
			t.Errorf("watch: %v", err)
		}
	})
	item := todoTicket(t, w.bx)
	if env, code := openReview(w, item, "a design question"); code != 0 {
		t.Errorf("room open = %d %v", code, env)
	}
	if !raced.Load() {
		t.Fatal("no room checkout ran the watch pass")
	}
	startedOnce(t, w)
}

// A second room open that arrives while a start holds the room starts no
// second session. It reads the room while that start makes the checkout,
// so its read holds no checkout. It acts on the room as the start left
// it: live, and it answers with the live room.
func TestRoomStartsOnceWhenASecondOpenArrivesDuringTheStart(t *testing.T) {
	w := newRoomWorld(t)
	type result struct {
		out  string
		code int
	}
	second := make(chan result, 1)
	var item string
	raced := raceRoomStart(w, func(ctx context.Context) {
		// The goroutine never touches t, so a failure of the open comes
		// back as its result and never ends the goroutine early.
		go func() {
			out, code := w.call(w.coord, reviewArgs(item, "a second question")...)
			second <- result{out, code}
		}()
		// The second open writes its reason when it reads the room. Go on
		// once it has, so the first start ends after that read.
		deadline := time.Now().Add(10 * time.Second)
		for {
			rooms, err := w.srv.Rooms.Rooms(ctx, false)
			if err == nil && len(rooms) == 1 && rooms[0].Reason == "a second question" {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("the second open did not read the room: %+v %v", rooms, err)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	item = todoTicket(t, w.bx)
	if env, code := openReview(w, item, "a design question"); code != 0 {
		t.Errorf("room open = %d %v", code, env)
	}
	if !raced.Load() {
		t.Fatal("no room checkout started the second open")
	}
	select {
	case got := <-second:
		args := reviewArgs(item, "a second question")
		if env := w.envelope(args, got.out); got.code != 0 {
			t.Errorf("second room open = %d %v", got.code, env)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the second room open did not end")
	}
	startedOnce(t, w)
}
