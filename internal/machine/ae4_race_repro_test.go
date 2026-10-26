package machine

import (
	"context"
	"sync"
	"testing"
	"time"
)

// lateCtx is a context whose cancel reaches its Done channel late, as it
// does when the scheduler runs the CLI's goroutine after the answer lands.
type lateCtx struct {
	context.Context
	once sync.Once
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func newLateCtx() *lateCtx { return &lateCtx{Context: context.Background(), done: make(chan struct{})} }

func (c *lateCtx) Done() <-chan struct{} { return c.done }

func (c *lateCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// kill cancels now, and the CLI sees the cancel after delay.
func (c *lateCtx) kill(delay time.Duration) {
	c.once.Do(func() {
		go func() {
			time.Sleep(delay)
			c.mu.Lock()
			c.err = context.Canceled
			c.mu.Unlock()
			close(c.done)
		}()
	})
}

// AE4 with the kill seen late: the CLI is killed after the server commits,
// and the CLI notices the kill only after the server's answer arrived. The
// killed CLI must never see an answer, whatever the scheduler does.
func TestAE4KilledAfterCommitReplaysWhenKillSeenLate(t *testing.T) {
	r := newRig(t)
	id := r.implementing("run-1")
	ctx := newLateCtx()
	r.w.m.hook = func(p string) bool {
		if p == pointMarked {
			ctx.kill(500 * time.Millisecond)
			return true
		}
		return false
	}
	if _, code := r.run(ctx, nil, "work", "note", id, "--body", "hello"); code == 0 {
		t.Fatal("the killed CLI saw an answer")
	}
	r.w.m.hook = nil
	env, code := r.run(context.Background(), nil, "work", "note", id, "--body", "hello")
	if code != 0 || env["replayed"] != true {
		t.Fatalf("the re-run: exit %d %v", code, env)
	}
	if _, history := r.w.verbCount(id, "note", "applied"); history != 1 {
		t.Fatalf("%d notes landed, want 1", history)
	}
}
