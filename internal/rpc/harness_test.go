package rpc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// syncBuffer is a log sink that many goroutines write.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeProc answers the working directory and parent of each pid.
type fakeProc struct {
	mu      sync.Mutex
	cwd     map[int]string
	parents map[int]int
}

func (f *fakeProc) Cwd(pid int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.cwd[pid]; ok {
		return c, nil
	}
	return "/", nil
}

func (f *fakeProc) Parent(pid int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.parents[pid]; ok {
		return p, nil
	}
	return 1, nil
}

// setCwd sets the working directory the server reads for this process.
func (f *fakeProc) setCwd(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cwd[os.Getpid()] = dir
}

type harness struct {
	t         *testing.T
	dir       string
	st        *store.Store
	red       *secrets.Redactor
	tokens    *Tokens
	srv       *Server
	proc      *fakeProc
	logs      *syncBuffer
	checkouts []string
	opts      Options
}

func newHarness(t *testing.T, tweak ...func(*Options)) *harness {
	t.Helper()
	ctx := context.Background()
	dir := privateDir(t)
	st, err := store.Open(ctx, filepath.Join(dir, "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h := &harness{t: t, dir: dir, st: st, red: secrets.NewRedactor(), logs: &syncBuffer{},
		proc: &fakeProc{cwd: map[int]string{}, parents: map[int]int{}}}
	h.tokens = &Tokens{Store: st, Redactor: h.red}
	h.opts = Options{
		Registry: cli.Surface(), Store: st, Redactor: h.red, Tokens: h.tokens, Instance: dir,
		Logger: slog.New(slog.NewJSONHandler(h.logs, nil)), Proc: h.proc,
		Checkouts: func(context.Context) ([]string, error) { return h.checkouts, nil },
	}
	for _, f := range tweak {
		f(&h.opts)
	}
	h.srv, err = New(h.opts)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// start binds the socket and serves.
func (h *harness) start() {
	h.t.Helper()
	sock := cli.SocketPath(h.dir)
	if err := PrepareSocket(sock); err != nil {
		h.t.Fatal(err)
	}
	l, err := Bind(sock)
	if err != nil {
		h.t.Fatal(err)
	}
	go func() { _ = h.srv.Serve(l) }()
	h.t.Cleanup(h.srv.Stop)
}

// privateDir is a new 0700 directory. t.TempDir is 0775 under this umask.
func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (h *harness) handle(path string, fn Handler) {
	h.t.Helper()
	if err := h.srv.Handle(path, fn); err != nil {
		h.t.Fatal(err)
	}
}

// issue writes a token into a new checkout and returns the checkout.
func (h *harness) issue(runID, kind, item string) string {
	h.t.Helper()
	checkout := filepath.Join(h.dir, "checkouts", runID)
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.tokens.IssueToCheckout(context.Background(), IssueRequest{
		RunID: runID, Kind: kind, Project: "alpha", Item: item, Stage: "implement", Checkout: checkout,
	}); err != nil {
		h.t.Fatal(err)
	}
	return checkout
}

func (h *harness) client(cwd string) *cli.Client {
	return &cli.Client{Instance: h.dir, Getwd: func() (string, error) { return cwd, nil }, Budget: 3 * time.Second}
}

// run runs one command line through a client from cwd. The server reads
// cwd as the caller's working directory.
func (h *harness) run(cwd string, args ...string) (map[string]any, int) {
	h.t.Helper()
	return h.runWith(context.Background(), h.client(cwd), cwd, nil, args...)
}

func (h *harness) runWith(ctx context.Context, c *cli.Client, cwd string, stdin io.Reader, args ...string) (map[string]any, int) {
	h.t.Helper()
	h.proc.setCwd(cwd)
	c.Stdin = stdin
	r := cli.Surface()
	c.Attach(r)
	var out, errOut bytes.Buffer
	code := r.Run(ctx, args, &out, &errOut)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		h.t.Fatalf("loop %v printed no envelope: %v\n%s\nstderr: %s", args, err, out.String(), errOut.String())
	}
	if env["exit"] != float64(code) {
		h.t.Fatalf("envelope exit %v differs from exit %d", env["exit"], code)
	}
	return env, code
}

func (h *harness) count(query string, args ...any) int {
	h.t.Helper()
	var n int
	err := h.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(query, args...).Scan(&n)
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

func wantRefusal(t *testing.T, env map[string]any, code int, wantExit int, wantCode, wantRule string) {
	t.Helper()
	if code != wantExit || env["code"] != wantCode {
		t.Fatalf("want exit %d %s, got exit %d %v", wantExit, wantCode, code, env)
	}
	if wantRule != "" && env["rule"] != wantRule {
		t.Fatalf("want rule %s, got %v", wantRule, env)
	}
}

func found(context.Context, *Call) cli.Result { return cli.Found(map[string]any{"done": true}) }
