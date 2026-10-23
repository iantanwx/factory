package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/rpc"
)

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

type running struct {
	dir    string
	cancel context.CancelFunc
	done   chan error
	logs   *syncBuffer
}

func instanceDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// start runs a server on a new instance. With wait, it returns once the
// socket accepts calls.
func start(t *testing.T, dir string, opts Options, wait bool) *running {
	t.Helper()
	ready := make(chan struct{})
	logs := &syncBuffer{}
	opts.Instance = dir
	opts.Ready = ready
	opts.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{dir: dir, cancel: cancel, done: make(chan error, 1), logs: logs}
	go func() { r.done <- Run(ctx, opts) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-r.done:
			if err != nil {
				t.Logf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the server did not stop")
		}
	})
	if wait {
		select {
		case <-ready:
		case err := <-r.done:
			t.Fatalf("the server stopped: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("the server did not become ready")
		}
	}
	return r
}

func call(t *testing.T, dir string, budget time.Duration, stdin io.Reader, args ...string) (map[string]any, int, string) {
	t.Helper()
	r := cli.Surface()
	(&cli.Client{Instance: dir, Stdin: stdin, Budget: budget}).Attach(r)
	var out, errOut bytes.Buffer
	code := r.Run(context.Background(), args, &out, &errOut)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("loop %v printed no envelope: %v\n%s", args, err, out.String())
	}
	return env, code, out.String()
}

func TestSocketAcceptsNoCallUntilReconcileFinished(t *testing.T) {
	dir := instanceDir(t)
	release := make(chan struct{})
	reconciled := make(chan struct{})
	start(t, dir, Options{Reconcile: []Step{func(ctx context.Context) error {
		close(reconciled)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}}}, false)
	<-reconciled
	env, code, _ := call(t, dir, 500*time.Millisecond, nil, "status")
	if code != 4 || env["code"] != cli.CodeServerUnavailable {
		t.Fatalf("a call during reconcile must wait and exit 4: %d %v", code, env)
	}
	close(release)
	env, code, _ = call(t, dir, 5*time.Second, nil, "status")
	if code != 0 || env["data"].(map[string]any)["ready"] != true {
		t.Fatalf("status after reconcile: %d %v", code, env)
	}
}

func TestFailedReconcileNeverServes(t *testing.T) {
	dir := instanceDir(t)
	err := Run(context.Background(), Options{Instance: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Reconcile: []Step{func(context.Context) error { return errors.New("beads unreachable") }}})
	if err == nil || !strings.Contains(err.Error(), "beads unreachable") {
		t.Fatalf("Run = %v", err)
	}
	if _, err := os.Lstat(cli.SocketPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("no socket may exist after a failed start: %v", err)
	}
}

func TestSecondServerOnTheInstanceIsRefused(t *testing.T) {
	dir := instanceDir(t)
	start(t, dir, Options{}, true)
	err := Run(context.Background(), Options{Instance: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if !errors.Is(err, rpc.ErrInstanceLocked) {
		t.Fatalf("second Run = %v, want ErrInstanceLocked", err)
	}
	if env, code, _ := call(t, dir, time.Second, nil, "status"); code != 0 {
		t.Fatalf("the first server still serves: %v", env)
	}
}

func TestStatusReportsTheServer(t *testing.T) {
	dir := instanceDir(t)
	start(t, dir, Options{}, true)
	env, code, _ := call(t, dir, time.Second, nil, "status")
	data := env["data"].(map[string]any)
	if code != 0 || data["server_version"] != cli.Version || data["instance"] != dir || data["ready"] != true {
		t.Fatalf("status = %v", env)
	}
	if _, ok := data["uptime_seconds"]; !ok {
		t.Fatalf("status lacks uptime: %v", data)
	}
}

func TestProjectVerbs(t *testing.T) {
	dir := instanceDir(t)
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
	start(t, dir, Options{}, true)
	add := []string{"project", "add", "alpha", "--repo", "iantanwx/alpha", "--path", "/srv/alpha"}
	if env, code, _ := call(t, dir, time.Second, nil, add...); code != 0 {
		t.Fatalf("project add: %v", env)
	}
	env, code, _ := call(t, dir, time.Second, nil, add...)
	if code != 3 || env["code"] != cli.CodeAlreadyExists {
		t.Fatalf("a second add is refused: %d %v", code, env)
	}
	env, code, _ = call(t, dir, time.Second, nil, "project", "list")
	if list := env["data"].([]any); code != 0 || len(list) != 1 || list[0].(map[string]any)["repo"] != "iantanwx/alpha" {
		t.Fatalf("project list: %v", env)
	}
	if env, code, _ := call(t, dir, time.Second, nil, "project", "show", "alpha"); code != 0 || env["data"].(map[string]any)["path"] != "/srv/alpha" {
		t.Fatalf("project show: %v", env)
	}
	env, code, _ = call(t, dir, time.Second, nil, "project", "show", "beta")
	if code != 2 || env["code"] != cli.CodeNotFound {
		t.Fatalf("project show missing: %v", env)
	}
	env, code, _ = call(t, dir, time.Second, nil, "project", "add", "Bad", "--repo", "x", "--path", "rel")
	if code != 2 || env["code"] != cli.CodeUsage {
		t.Fatalf("an invalid project is a usage fault: %v", env)
	}
	// A transfer on GitHub: move keeps the id and the path.
	env, code, _ = call(t, dir, time.Second, nil, "project", "move", "alpha", "--repo", "acme/alpha")
	if code != 0 || env["data"].(map[string]any)["repo"] != "acme/alpha" || env["data"].(map[string]any)["path"] != "/srv/alpha" {
		t.Fatalf("project move: %d %v", code, env)
	}
	if env, code, _ := call(t, dir, time.Second, nil, "project", "remove", "alpha"); code != 0 {
		t.Fatalf("project remove: %v", env)
	}
	if env, code, _ := call(t, dir, time.Second, nil, "project", "list"); code != 1 {
		t.Fatalf("an empty list exits 1: %v", env)
	}
	// The removed id comes back for another repository: its store row
	// follows.
	if env, code, _ := call(t, dir, time.Second, nil, "project", "add", "alpha", "--repo", "other/alpha", "--path", "/srv/alpha"); code != 0 {
		t.Fatalf("project add after remove: %v", env)
	}
	if env, code, _ := call(t, dir, time.Second, nil, "project", "remove", "alpha"); code != 0 {
		t.Fatalf("project remove: %v", env)
	}
	cfg, err := config.LoadHost(filepath.Join(dir, config.HostConfigFile))
	if err != nil || len(cfg.Projects) != 0 {
		t.Fatalf("host config = %+v %v", cfg, err)
	}
}

// The operator's own shell reports a loop fault with the host-admin
// credential.
func TestTheHostReportsAFault(t *testing.T) {
	dir := instanceDir(t)
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
	start(t, dir, Options{}, true)
	env, code, _ := call(t, dir, time.Second, nil, "fault", "report", "--file", writeReport(t, "register refused a moved repository"))
	if code != 0 || env["data"].(map[string]any)["reporter"] != cli.PrincipalHost {
		t.Fatalf("fault report from the host: %d %v", code, env)
	}
	if env, code, _ := call(t, dir, time.Second, nil, "fault", "list"); code != 0 || len(env["data"].([]any)) != 1 {
		t.Fatalf("fault list from the host: %d %v", code, env)
	}
}

func TestSecretSetReadsStdinAndNeverEchoes(t *testing.T) {
	const value = "ghp_supersecretvalue_0123456789"
	dir := instanceDir(t)
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
	s := start(t, dir, Options{}, true)
	if _, code, _ := call(t, dir, time.Second, nil, "project", "add", "alpha", "--repo", "iantanwx/alpha", "--path", "/srv/alpha"); code != 0 {
		t.Fatal("project add")
	}
	env, code, raw := call(t, dir, time.Second, strings.NewReader(value+"\n"), "secret", "set", "API_TOKEN", "--project", "alpha")
	if code != 0 || env["data"].(map[string]any)["stored"] != true {
		t.Fatalf("secret set: %d %v", code, env)
	}
	if strings.Contains(raw, value) {
		t.Fatalf("secret set echoed the value: %s", raw)
	}
	// The value now sits in the redactor: an error that would echo it
	// holds none.
	env, code, raw = call(t, dir, time.Second, nil, "project", "show", value)
	if code != 2 || strings.Contains(raw, value) || !strings.Contains(env["hint"].(string), "REDACTED") {
		t.Fatalf("the error envelope echoes the secret: %s", raw)
	}
	if strings.Contains(s.logs.String(), value) {
		t.Fatalf("a log line holds the secret:\n%s", s.logs.String())
	}
	// No stdin: refused, never read from a flag.
	r := cli.Surface()
	(&cli.Client{Instance: dir, Budget: time.Second}).Attach(r)
	var out bytes.Buffer
	if code := r.Run(context.Background(), []string{"secret", "set", "API_TOKEN", "--project", "alpha"}, &out, io.Discard); code != 2 {
		t.Fatalf("secret set with no stdin: %d %s", code, out.String())
	}
	if code := r.Run(context.Background(), []string{"secret", "set", "API_TOKEN", "--project", "alpha", "--value", value}, &out, io.Discard); code != 2 {
		t.Fatal("secret set has no --value flag")
	}
}

func TestSecretCheckFromARun(t *testing.T) {
	dir := instanceDir(t)
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
	var srv *Server
	start(t, dir, Options{Setup: []func(*Server) error{func(s *Server) error { srv = s; return nil }}}, true)
	if _, code, _ := call(t, dir, time.Second, nil, "project", "add", "alpha", "--repo", "iantanwx/alpha", "--path", "/srv/alpha"); code != 0 {
		t.Fatal("project add")
	}
	if _, code, _ := call(t, dir, time.Second, strings.NewReader("value-0123456789"), "secret", "set", "API_TOKEN", "--project", "alpha"); code != 0 {
		t.Fatal("secret set")
	}
	checkout := t.TempDir()
	if _, err := srv.Tokens.IssueToCheckout(context.Background(), rpc.IssueRequest{
		RunID: "run-1", Kind: cli.PrincipalRun, Project: "alpha", Item: "T-1", Checkout: checkout,
	}); err != nil {
		t.Fatal(err)
	}
	runCall := func(args ...string) (map[string]any, int) {
		r := cli.Surface()
		(&cli.Client{Instance: dir, Budget: time.Second, Getwd: func() (string, error) { return checkout, nil }}).Attach(r)
		var out bytes.Buffer
		code := r.Run(context.Background(), args, &out, io.Discard)
		var env map[string]any
		_ = json.Unmarshal(out.Bytes(), &env)
		return env, code
	}
	// The real /proc says this test runs outside the checkout, so the
	// token check refuses: a token works only inside its checkout.
	env, code := runCall("secret", "check", "API_TOKEN")
	if code != 3 || env["rule"] != "token_outside_checkout" {
		t.Fatalf("got %d %v", code, env)
	}
	t.Chdir(checkout)
	env, code = runCall("secret", "check", "API_TOKEN")
	if code != 0 || env["data"].(map[string]any)["present"] != true {
		t.Fatalf("secret check: %d %v", code, env)
	}
	if _, code := runCall("secret", "check", "MISSING"); code != 1 {
		t.Fatalf("a missing secret exits 1, got %d", code)
	}
	env, code = runCall("secret", "check", "API_TOKEN", "--project", "beta")
	if code != 3 || env["rule"] != "other_project" {
		t.Fatalf("a run checks only its own project: %d %v", code, env)
	}
}

func TestSecretImportUsesTheManifestAndOp(t *testing.T) {
	dir := instanceDir(t)
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, config.HostConfigFile)
	if err := config.SaveHost(cfgPath, &config.HostConfig{StateDir: dir, Vaults: []string{"Loop"}, Throughput: config.DefaultThroughput()}); err != nil {
		t.Fatal(err)
	}
	op := filepath.Join(t.TempDir(), "op")
	if err := os.WriteFile(op, []byte("#!/bin/sh\nprintf %s imported-value-0123456789\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := config.ParseManifest([]byte("secrets:\n  - name: API_TOKEN\n    source: op://Loop/api/token\n"))
	if err != nil {
		t.Fatal(err)
	}
	var gotBranch string
	start(t, dir, Options{OpPath: op, ManifestReader: func(_ context.Context, p config.Project, branch string) (*config.Manifest, error) {
		gotBranch = branch
		return manifest, nil
	}}, true)
	if _, code, _ := call(t, dir, time.Second, nil, "project", "add", "alpha", "--repo", "iantanwx/alpha", "--path", "/srv/alpha"); code != 0 {
		t.Fatal("project add")
	}
	env, code, raw := call(t, dir, 5*time.Second, nil, "secret", "import", "--project", "alpha")
	if code != 0 || gotBranch != "main" {
		t.Fatalf("secret import: %d %v (branch %q)", code, env, gotBranch)
	}
	if strings.Contains(raw, "imported-value") {
		t.Fatalf("import echoed a value: %s", raw)
	}
	env, code, raw = call(t, dir, time.Second, nil, "project", "show", "imported-value-0123456789")
	if code != 2 || strings.Contains(raw, "imported-value-0123456789") {
		t.Fatalf("the imported value is not redacted: %v", env)
	}
}

func TestSigtermCompletesTheInFlightCall(t *testing.T) {
	dir := instanceDir(t)
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	persisted := make(chan struct{})
	s := start(t, dir, Options{
		Setup: []func(*Server) error{func(s *Server) error {
			return s.RPC.Handle("drain", func(context.Context, *rpc.Call) cli.Result {
				close(entered)
				<-release
				return cli.Found(map[string]any{"drained": true})
			})
		}},
		OnStop: []Step{func(context.Context) error { close(persisted); return nil }},
	}, true)
	got := make(chan int, 1)
	go func() {
		_, code, _ := call(t, dir, 5*time.Second, nil, "drain")
		got <- code
	}()
	<-entered
	s.cancel() // what SIGTERM does to serve's context
	select {
	case err := <-s.done:
		t.Fatalf("the server stopped before its in-flight call finished: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if code := <-got; code != 0 {
		t.Fatalf("the in-flight call exits %d", code)
	}
	select {
	case err := <-s.done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop")
	}
	select {
	case <-persisted:
	default:
		t.Fatal("the stop steps did not run")
	}
	s.done <- nil // let the cleanup read a result
}

func TestShutdownDeadlineStopsAStuckCall(t *testing.T) {
	dir := instanceDir(t)
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	s := start(t, dir, Options{
		ShutdownTimeout: 300 * time.Millisecond,
		Setup: []func(*Server) error{func(s *Server) error {
			return s.RPC.Handle("drain", func(ctx context.Context, _ *rpc.Call) cli.Result {
				close(entered)
				<-ctx.Done()
				return cli.Found(nil)
			})
		}},
	}, true)
	go func() { _, _, _ = call(t, dir, time.Second, nil, "drain") }()
	<-entered
	s.cancel()
	select {
	case err := <-s.done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop after the deadline did not end the server")
	}
	s.done <- nil
}
