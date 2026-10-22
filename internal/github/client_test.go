package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// stalled is a GitHub that accepts each request and never answers.
func stalled(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	release := make(chan struct{})
	n := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv, n
}

// A stalled connection ends at the request timeout, even when the caller's
// context has no deadline. A read retries it, a write does not, and both
// read as GitHub unavailable.
func TestStalledGitHubTimesOut(t *testing.T) {
	srv, n := stalled(t)
	cl := &Client{Base: srv.URL, Cooldown: NewCooldown(time.Now), Timeout: 200 * time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil }}
	begin := time.Now()
	_, err := cl.Do(context.Background(), Call{Method: http.MethodGet, Path: "/repos/o/r"}, nil)
	var ua *UnavailableError
	if !errors.As(err, &ua) {
		t.Fatalf("read = %v, want UnavailableError", err)
	}
	if n.Load() != 3 {
		t.Fatalf("read made %d attempts, want 3", n.Load())
	}
	_, err = cl.Do(context.Background(), Call{Method: http.MethodPost, Path: "/repos/o/r/statuses/x", Body: map[string]string{}}, nil)
	if !errors.As(err, &ua) || n.Load() != 4 {
		t.Fatalf("write = %v after %d attempts, want UnavailableError after one more", err, n.Load())
	}
	if took := time.Since(begin); took > 10*time.Second {
		t.Fatalf("stalled calls ran %s", took)
	}
}

// The caller's own cancel is the caller's error, never GitHub's.
func TestCallerCancelIsNotUnavailable(t *testing.T) {
	srv, _ := stalled(t)
	cl := &Client{Base: srv.URL, Cooldown: NewCooldown(time.Now)}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := cl.Do(ctx, Call{Method: http.MethodGet, Path: "/repos/o/r"}, nil)
	var ua *UnavailableError
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ua) {
		t.Fatalf("read = %v, want the caller's deadline", err)
	}
}

func TestDefaultHTTPClientHasATimeout(t *testing.T) {
	if defaultHTTP.Timeout <= 0 || defaultHTTP == http.DefaultClient {
		t.Fatalf("default client timeout = %s", defaultHTTP.Timeout)
	}
}

// A hung git ends at its timeout, with its helpers, when the caller's
// context has no deadline.
func TestHungGitTimesOut(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 60 &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	g := &Git{Binary: bin, Home: t.TempDir(), Timeout: 200 * time.Millisecond, TransferTimeout: 300 * time.Millisecond}
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"fetch", "origin"}} {
		begin := time.Now()
		_, err := g.run(context.Background(), gitRun{dir: t.TempDir(), args: args})
		var ge *GitError
		if !errors.As(err, &ge) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("git %s = %v, want a GitError at the deadline", args[0], err)
		}
		if took := time.Since(begin); took > 5*time.Second {
			t.Fatalf("git %s ran %s", args[0], took)
		}
	}
}

func TestGitTimeoutByCommand(t *testing.T) {
	g := &Git{}
	if got := g.timeout([]string{"push"}); got != GitTransferTimeout {
		t.Fatalf("push timeout = %s", got)
	}
	if got := g.timeout([]string{"cat-file", "blob", "x"}); got != GitTimeout {
		t.Fatalf("cat-file timeout = %s", got)
	}
}
