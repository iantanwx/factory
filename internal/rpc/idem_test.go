package rpc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	loopv1 "github.com/iantanwx/loop/gen/loop/v1"
	"github.com/iantanwx/loop/internal/cli"
)

// itemState is the fake `work show` the client reads for a key.
type itemState struct {
	mu     sync.Mutex
	status string
	seq    int64
}

func (s *itemState) set(status string, seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.seq = status, seq
}

func (s *itemState) handler(context.Context, *Call) cli.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cli.Found(map[string]any{"status": s.status, "seq": s.seq})
}

func pendingKeys(t *testing.T, checkout string) []string {
	t.Helper()
	entries, err := cli.OpenPending(checkout).Entries()
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	return keys
}

func TestPendingKeySurvivesAKilledCLIAndAMovedStatus(t *testing.T) {
	h := newHarness(t)
	state := &itemState{status: "in_progress", seq: 3}
	h.handle("work show", state.handler)
	var (
		calls   atomic.Int32
		keys    sync.Map
		entered = make(chan struct{}, 4)
		release = make(chan struct{})
	)
	h.handle("work note", func(ctx context.Context, c *Call) cli.Result {
		n := calls.Add(1)
		keys.Store(n, c.Request.GetIdempotencyKey())
		if c.Inv.Flags["body"] == "first" && n == 1 {
			entered <- struct{}{}
			<-release
		}
		if c.Inv.Flags["body"] == "refuse me" {
			return cli.Refuse(cli.Refusal{Code: cli.CodeStatusChanged, Rule: "status_changed", Item: "T-A"})
		}
		return cli.Found(map[string]any{"noted": c.Inv.Flags["body"]})
	})
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")

	// 1. The CLI sends the write and is killed before the answer arrives.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		_, code := h.runWith(ctx, h.client(a), a, nil, "work", "note", "T-A", "--body", "first")
		done <- code
	}()
	<-entered
	cancel()
	if code := <-done; code != 2 {
		t.Fatalf("the killed CLI exits %d", code)
	}
	left := pendingKeys(t, a)
	if len(left) != 1 {
		t.Fatalf("the killed CLI leaves one pending entry, got %v", left)
	}
	close(release)
	waitFor(t, func() bool {
		return h.count(`SELECT count(*) FROM idempotency WHERE key = ? AND result IS NOT NULL`, left[0]) == 1
	})

	// 2. The write moved the status. A re-run of the same command sends
	// the same key and gets the stored answer.
	state.set("review", 4)
	env, code := h.run(a, "work", "note", "T-A", "--body", "first")
	if code != 0 || env["replayed"] != true {
		t.Fatalf("the re-run should replay: %d %v", code, env)
	}
	if calls.Load() != 1 {
		t.Fatalf("the handler ran %d times, want 1", calls.Load())
	}
	if n := h.count(`SELECT count(*) FROM idempotency`); n != 1 {
		t.Fatalf("idempotency rows = %d; the re-run must reuse the key", n)
	}
	if k := pendingKeys(t, a); len(k) != 0 {
		t.Fatalf("exit 0 clears the entry, left %v", k)
	}

	// 3. A different command gets a new key, and exit 0 clears it.
	env, code = h.run(a, "work", "note", "T-A", "--body", "second")
	if code != 0 || env["replayed"] != nil {
		t.Fatalf("second note: %d %v", code, env)
	}
	second, _ := keys.Load(int32(2))
	if second == left[0] || second == "" {
		t.Fatalf("a different command must get a new key: %v", second)
	}
	if k := pendingKeys(t, a); len(k) != 0 {
		t.Fatalf("exit 0 clears the entry, left %v", k)
	}

	// 4. The same command again, now that nothing is pending, is a new
	// write: the status moved, so the key differs.
	state.set("review", 5)
	if _, code := h.run(a, "work", "note", "T-A", "--body", "first"); code != 0 {
		t.Fatal("a repeated note after a definitive answer is a new write")
	}
	if calls.Load() != 3 {
		t.Fatalf("handler calls = %d, want 3", calls.Load())
	}

	// 5. Exit 3 is definitive too, and clears the entry.
	env, code = h.run(a, "work", "note", "T-A", "--body", "refuse me")
	if code != 3 || env["code"] != cli.CodeStatusChanged {
		t.Fatalf("got %d %v", code, env)
	}
	if k := pendingKeys(t, a); len(k) != 0 {
		t.Fatalf("exit 3 clears the entry, left %v", k)
	}
}

func TestExit2LeavesThePendingEntry(t *testing.T) {
	h := newHarness(t)
	h.start() // work note has no handler: NOT_IMPLEMENTED, exit 2
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	if _, code := h.run(a, "work", "note", "T-A", "--body", "x"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	first := pendingKeys(t, a)
	if len(first) != 1 {
		t.Fatalf("pending = %v", first)
	}
	if _, code := h.run(a, "work", "note", "T-A", "--body", "x"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if again := pendingKeys(t, a); len(again) != 1 || again[0] != first[0] {
		t.Fatalf("the repeat must reuse the pending key: %v then %v", first, again)
	}
}

func TestSameKeyWithOtherArgumentsIsUsage(t *testing.T) {
	h := newHarness(t)
	h.handle("work note", found)
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	tf, _ := cli.ReadTokenFile(a)
	h.proc.setCwd(a)
	conn, err := grpc.NewClient("unix://"+cli.SocketPath(h.dir), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	req := func(body string) *loopv1.VerbRequest {
		return &loopv1.VerbRequest{Verb: "work note", Args: []string{"T-A"}, Flags: map[string]string{"body": body},
			IdempotencyKey: "k1-fixed", RunToken: tf.Token, ClientVersion: cli.Version}
	}
	if env, _ := rawCall(t, conn, "/loop.v1.WorkService/Note", req("one")); !env.GetOk() {
		t.Fatalf("first call: %v", env)
	}
	env, st := rawCall(t, conn, "/loop.v1.WorkService/Note", req("two"))
	if env.GetRefusal().GetCode() != cli.CodeUsage || env.GetRefusal().GetRule() != "idempotency_key_conflict" {
		t.Fatalf("got %v", env)
	}
	if st.Code().String() != "InvalidArgument" {
		t.Fatalf("status = %v", st.Code())
	}
}

func TestHostWritesCarryNoKey(t *testing.T) {
	h := newHarness(t)
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	h.handle("project add", func(context.Context, *Call) cli.Result {
		if calls.Add(1) > 1 {
			return cli.Refuse(cli.Refusal{Code: cli.CodeAlreadyExists, Rule: "project_exists"})
		}
		return cli.Found(nil)
	})
	h.start()
	args := []string{"project", "add", "alpha", "--repo", "iantanwx/alpha", "--path", "/srv/alpha"}
	if _, code := h.run(t.TempDir(), args...); code != 0 {
		t.Fatal("first add")
	}
	if env, code := h.run(t.TempDir(), args...); code != 3 || env["replayed"] != nil {
		t.Fatalf("a second add is refused, never replayed: %d %v", code, env)
	}
}

func TestCLIWaitsForTheServerAndTheWriteLands(t *testing.T) {
	h := newHarness(t)
	h.handle("work note", found)
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	// The plan's 30-second outage, shrunk: down for 1.5 s, budget 6 s.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		h.start()
	}()
	c := h.client(a)
	c.Budget = 6 * time.Second
	start := time.Now()
	env, code := h.runWith(context.Background(), c, a, nil, "work", "note", "T-A", "--body", "lands")
	if code != 0 {
		t.Fatalf("the write should land after the wait: %d %v", code, env)
	}
	if waited := time.Since(start); waited < time.Second {
		t.Fatalf("the CLI did not wait: %v", waited)
	}
}

func TestCLIGivesUpAfterTheBudgetWithExit4(t *testing.T) {
	h := newHarness(t) // never started
	c := h.client(t.TempDir())
	c.Budget = 600 * time.Millisecond
	start := time.Now()
	env, code := h.runWith(context.Background(), c, t.TempDir(), nil, "status")
	elapsed := time.Since(start)
	wantRefusal(t, env, code, 4, cli.CodeServerUnavailable, "server_unreachable")
	if env["retryable"] != true || env["retry_after_seconds"] != float64(cli.RetryAfterSeconds) {
		t.Fatalf("want retryable with retry_after_seconds: %v", env)
	}
	if elapsed < 500*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("gave up after %v; the budget is 600ms", elapsed)
	}
}

func TestStaleSocketMakesTheCLIWaitNotFail(t *testing.T) {
	h := newHarness(t)
	h.handle("status", found)
	sock := cli.SocketPath(h.dir)
	if err := PrepareSocket(sock); err != nil {
		t.Fatal(err)
	}
	l, err := Bind(sock)
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	_ = l.Close() // a SIGKILLed server leaves this file
	go func() {
		time.Sleep(time.Second)
		h.start()
	}()
	if env, code := h.run(t.TempDir(), "status"); code != 0 {
		t.Fatalf("status after restart: %v", env)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A run's last write is often run finish, which revokes its token. When the
// answer is lost, the retry with the same key must get the stored answer,
// not token_revoked. A new key from the settled run is still refused.
func TestSettledRunReplaysItsLastWrite(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	h.handle("work note", func(context.Context, *Call) cli.Result {
		calls.Add(1)
		return cli.Found(map[string]any{"done": true})
	})
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	tf, _ := cli.ReadTokenFile(a)
	h.proc.setCwd(a)
	conn, err := grpc.NewClient("unix://"+cli.SocketPath(h.dir), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	req := func(key string) *loopv1.VerbRequest {
		return &loopv1.VerbRequest{Verb: "work note", Args: []string{"T-A"}, Flags: map[string]string{"body": "b"},
			IdempotencyKey: key, RunToken: tf.Token, ClientVersion: cli.Version}
	}
	if env, _ := rawCall(t, conn, "/loop.v1.WorkService/Note", req("k1-last")); !env.GetOk() {
		t.Fatalf("first call: %v", env)
	}
	if _, err := h.tokens.Revoke(context.Background(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if env, _ := rawCall(t, conn, "/loop.v1.WorkService/Note", req("k1-last")); !env.GetOk() {
		t.Fatalf("the retry of the last write must replay: %v", env)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times; a replay must not run it", n)
	}
	env, _ := rawCall(t, conn, "/loop.v1.WorkService/Note", req("k1-new"))
	if env.GetRefusal().GetRule() != "token_revoked" {
		t.Fatalf("a new write from a settled run: %v", env)
	}
}

// A refusal that depends on the moment (a full cap, a held mutex, an open
// dependency) changed nothing, so it is never stored: the same call later
// gets a fresh answer, not the old refusal.
func TestConditionalRefusalIsNotReplayed(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	h.handle("work note", func(context.Context, *Call) cli.Result {
		if calls.Add(1) == 1 {
			return cli.Refuse(cli.Refusal{Code: cli.CodeCapFull, Rule: "cap_full"})
		}
		return cli.Found(map[string]any{"done": true})
	})
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	tf, _ := cli.ReadTokenFile(a)
	h.proc.setCwd(a)
	conn, err := grpc.NewClient("unix://"+cli.SocketPath(h.dir), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	req := &loopv1.VerbRequest{Verb: "work note", Args: []string{"T-A"}, Flags: map[string]string{"body": "b"},
		IdempotencyKey: "k1-cap", RunToken: tf.Token, ClientVersion: cli.Version}
	if env, _ := rawCall(t, conn, "/loop.v1.WorkService/Note", req); env.GetRefusal().GetCode() != cli.CodeCapFull {
		t.Fatalf("first call: %v", env)
	}
	if env, _ := rawCall(t, conn, "/loop.v1.WorkService/Note", req); !env.GetOk() {
		t.Fatalf("the retry after the cap freed must run again: %v", env)
	}
}

// factorio-ny3n: a sign-off answer names an entry and no item. Its key
// holds the state of the epic the entry names. So a repeat at the same
// state replays, and the same words at the next sign-off of that entry
// are a new write.
func TestSignoffAnswerKeyHoldsTheStateOfTheEntrysEpic(t *testing.T) {
	h := newHarness(t)
	state := &itemState{status: "prd_signoff", seq: 3}
	var read atomic.Value
	h.handle("work show", func(ctx context.Context, c *Call) cli.Result {
		read.Store(c.Inv.Flags["project"] + "/" + c.Inv.Arg("item"))
		return state.handler(ctx, c)
	})
	var calls atomic.Int32
	h.handle("inbox answer", func(context.Context, *Call) cli.Result {
		return cli.Found(map[string]any{"answer": calls.Add(1)})
	})
	h.start()
	coord := h.issue("coordinator-r-1", cli.PrincipalCoordinator, "")
	answer := func() map[string]any {
		t.Helper()
		env, code := h.run(coord, "inbox", "answer", "signoff/alpha/E-1", "--body", "Approve")
		if code != 0 {
			t.Fatalf("exit %d: %v", code, env)
		}
		return env
	}

	answer()
	if got := read.Load(); got != "alpha/E-1" {
		t.Fatalf("the key read %v, want the state of alpha/E-1", got)
	}
	if env := answer(); env["replayed"] != true || calls.Load() != 1 {
		t.Fatalf("a repeat at the same state = %v after %d answers, want a replay", env, calls.Load())
	}
	state.set("spec_signoff", 7)
	if env := answer(); env["replayed"] == true || calls.Load() != 2 {
		t.Fatalf("the same words at the next sign-off = %v after %d answers, want a new write", env, calls.Load())
	}
}

// A replay guard reads a stored answer before the server replays it. Its
// refusal is the answer in place of the replay, and the handler does not
// run again.
func TestReplayGuardRefusesAStoredAnswer(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	h.handle("work note", func(context.Context, *Call) cli.Result {
		calls.Add(1)
		return cli.Found(map[string]any{"head": "h1"})
	})
	var head atomic.Value
	head.Store("h1")
	if err := h.srv.GuardReplay("work note", func(_ context.Context, _ *Call, stored cli.Envelope) *cli.Result {
		if stored.Data.(map[string]any)["head"] == head.Load() {
			return nil
		}
		return new(cli.Refuse(cli.Refusal{Code: cli.CodeStatusChanged, Rule: "answer_at_other_head"}))
	}); err != nil {
		t.Fatal(err)
	}
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	tf, _ := cli.ReadTokenFile(a)
	h.proc.setCwd(a)
	conn, err := grpc.NewClient("unix://"+cli.SocketPath(h.dir), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	req := &loopv1.VerbRequest{Verb: "work note", Args: []string{"T-A"}, Flags: map[string]string{"body": "b"},
		IdempotencyKey: "k1-guard", RunToken: tf.Token, ClientVersion: cli.Version}
	for range 2 {
		if env, _ := rawCall(t, conn, "/loop.v1.WorkService/Note", req); !env.GetOk() {
			t.Fatalf("the call at the stored head: %v", env)
		}
	}
	head.Store("h2")
	if env, _ := rawCall(t, conn, "/loop.v1.WorkService/Note", req); env.GetRefusal().GetRule() != "answer_at_other_head" {
		t.Fatalf("the replay at another head = %v, want the guard's refusal", env)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the handler ran %d times, want once", n)
	}
}
