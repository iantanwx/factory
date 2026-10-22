package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

// fixedProc says every caller works inside the run's checkout.
type fixedProc struct{ cwd string }

func (p fixedProc) Cwd(int) (string, error) { return p.cwd, nil }
func (p fixedProc) Parent(int) (int, error) { return 0, nil }

// rig serves the machine's verbs on a real socket, so a test drives the
// real CLI client: its pending keys, its expected status and its replay.
type rig struct {
	t        *testing.T
	w        *world
	dir      string
	checkout string
	tokens   *rpc.Tokens
	srv      *rpc.Server
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir, err := os.MkdirTemp("", "ae4")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	w := newWorld(t)
	r := &rig{t: t, w: w, dir: dir, checkout: filepath.Join(dir, "co"), tokens: &rpc.Tokens{Store: w.st, Redactor: w.red}}
	if err := os.Mkdir(r.checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	r.serve()
	return r
}

func (r *rig) serve() {
	r.t.Helper()
	srv, err := rpc.New(rpc.Options{Registry: cli.Surface(), Store: r.w.st, Redactor: r.w.red, Tokens: r.tokens,
		Instance: r.dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Proc: fixedProc{cwd: r.checkout}})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.w.m.Register(srv); err != nil {
		r.t.Fatal(err)
	}
	sock := cli.SocketPath(r.dir)
	if err := rpc.PrepareSocket(sock); err != nil {
		r.t.Fatal(err)
	}
	l, err := rpc.Bind(sock)
	if err != nil {
		r.t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	r.srv = srv
	r.t.Cleanup(srv.Stop)
}

// restart stops the server, starts a new machine that reconciles, and
// serves again: a server process that restarted.
func (r *rig) restart() {
	r.t.Helper()
	r.srv.Stop()
	r.w.restart()
	r.serve()
}

func (r *rig) issue(run, item string, epoch int64) {
	r.t.Helper()
	if _, err := r.tokens.IssueToCheckout(context.Background(), rpc.IssueRequest{
		RunID: run, Kind: cli.PrincipalRun, Project: prj, Item: item, Stage: StageImplementer, Checkout: r.checkout, Epoch: epoch,
	}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) run(ctx context.Context, state cli.StateReader, args ...string) (map[string]any, int) {
	r.t.Helper()
	reg := cli.Surface()
	(&cli.Client{Instance: r.dir, Getwd: func() (string, error) { return r.checkout, nil }, Budget: 3 * time.Second,
		State: state}).Attach(reg)
	var out bytes.Buffer
	code := reg.Run(ctx, args, &out, io.Discard)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		r.t.Fatalf("loop %v printed no envelope: %v\n%s", args, err, out.String())
	}
	return env, code
}

// implementing makes a ticket that run holds in in_progress, with a token.
func (r *rig) implementing(run string) string {
	r.t.Helper()
	id := r.w.create(KindTicket, "create", nil)
	r.issue(run, id, r.w.claim(id, StageImplementer, run))
	return id
}

// AE4: the CLI is killed after the server commits, and the same command
// runs again. It exits 0 with replayed: true, and the write landed once.
func TestAE4KilledAfterCommitReplays(t *testing.T) {
	r := newRig(t)
	id := r.implementing("run-1")
	ctx, kill := context.WithCancel(context.Background())
	r.w.m.hook = func(p string) bool {
		if p == pointMarked {
			kill()
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

// AE4: the CLI is killed while the server restarts, and the write lands
// through reconcile replay. The re-run returns replayed: true.
func TestAE4WriteLandsThroughReconcileAndReplays(t *testing.T) {
	r := newRig(t)
	id := r.implementing("run-1")
	ctx, kill := context.WithCancel(context.Background())
	r.w.m.hook = func(p string) bool {
		if p == pointRecorded {
			kill()
			return true
		}
		return false
	}
	if _, code := r.run(ctx, nil, "work", "note", id, "--body", "across a restart"); code == 0 {
		t.Fatal("the killed CLI saw an answer")
	}
	if n := r.w.count(`SELECT count(*) FROM intents WHERE state = 'pending'`); n != 1 {
		t.Fatalf("%d pending intents, want the recorded one", n)
	}
	r.restart()
	if _, history := r.w.verbCount(id, "note", "applied"); history != 1 {
		t.Fatal("reconcile did not land the write")
	}
	env, code := r.run(context.Background(), nil, "work", "note", id, "--body", "across a restart")
	if code != 0 || env["replayed"] != true {
		t.Fatalf("the re-run: exit %d %v", code, env)
	}
	if _, history := r.w.verbCount(id, "note", "applied"); history != 1 {
		t.Fatal("the re-run wrote twice")
	}
}

// movingReader reads the item as the CLI does, then lets another actor
// move it before the write arrives.
type movingReader struct {
	w    *world
	move func()
}

func (m movingReader) ItemState(ctx context.Context, item string) (cli.ItemState, *cli.Result) {
	it, ref := m.w.m.Show(ctx, prj, item)
	if ref != nil {
		res := cli.Refuse(*ref)
		return cli.ItemState{}, &res
	}
	m.move()
	return cli.ItemState{Status: it.Status, Seq: it.Seq}, nil
}

// AE4: another actor's write won first. The write exits 3 with
// STATUS_CHANGED, and the state shows the other actor's move.
func TestAE4AnotherActorWonFirst(t *testing.T) {
	r := newRig(t)
	id := r.implementing("run-1")
	reader := movingReader{w: r.w, move: func() { r.w.must(id, "pr_open", map[string]string{"head": "h1"}) }}
	env, code := r.run(context.Background(), reader, "work", "note", id, "--body", "late")
	if code != 3 || env["code"] != cli.CodeStatusChanged || env["current_status"] != InReview || env["expected_status"] != InProgress {
		t.Fatalf("exit %d %v", code, env)
	}
	if it := r.w.show(id); it.Status != InReview || it.Fact(FactNote) != "" {
		t.Fatalf("the state must show the other actor's move only: %+v", it)
	}
	// work show gives the client what its key needs.
	env, code = r.run(context.Background(), nil, "work", "show", id)
	data, _ := env["data"].(map[string]any)
	if code != 0 || data["status"] != InReview || data["seq"] == nil {
		t.Fatalf("work show: %d %v", code, env)
	}
}

// AE3 through the CLI: a move the table does not allow exits 3 and the
// state does not change.
func TestAE3RefusedMoveExits3(t *testing.T) {
	r := newRig(t)
	id := r.implementing("run-1")
	env, code := r.run(context.Background(), nil, "review", "submit", id, "--verdict", "pass", "--head", "h1")
	if code != 3 || env["code"] != cli.CodeTransitionNotAllowed {
		t.Fatalf("exit %d %v", code, env)
	}
	next, _ := env["allowed_next"].([]any)
	if len(next) == 0 {
		t.Fatalf("the refusal names no verb: %v", env)
	}
	if r.w.show(id).Status != InProgress {
		t.Fatal("the state changed")
	}
}

// failOnce fails the next update with a bd failure no rule classifies,
// as a Dolt lock error does, and writes nothing.
type failOnce struct {
	beadsx.Client
	armed atomic.Bool
}

func (c *failOnce) Update(ctx context.Context, id string, spec beadsx.UpdateSpec) (beadsx.Issue, error) {
	if c.armed.Swap(false) {
		return beadsx.Issue{}, fmt.Errorf("%w: database is locked", beadsx.ErrFailed)
	}
	return c.Client.Update(ctx, id, spec)
}

// An unclassified bd failure answers INTERNAL and keeps no answer under
// the key. The same command then writes again and is no replay.
func TestInternalRefusalIsNotReplayed(t *testing.T) {
	r := newRig(t)
	id := r.implementing("run-1")
	flaky := &failOnce{Client: r.w.bx}
	flaky.armed.Store(true)
	r.w.bc = flaky
	env, code := r.run(context.Background(), nil, "work", "note", id, "--body", "hello")
	if code != int(cli.ExitUndecided) || env["code"] != cli.CodeInternal {
		t.Fatalf("the first run: exit %d %v", code, env)
	}
	env, code = r.run(context.Background(), nil, "work", "note", id, "--body", "hello")
	if code != 0 || env["replayed"] == true {
		t.Fatalf("the re-run: exit %d %v", code, env)
	}
	if it := r.w.show(id); it.Fact(FactNote) != "hello" {
		t.Fatalf("the note did not land: %+v", it)
	}
	if _, history := r.w.verbCount(id, "note", "applied"); history != 1 {
		t.Fatalf("%d notes landed, want 1", history)
	}
}
