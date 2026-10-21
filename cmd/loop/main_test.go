package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// buildOnce maps a version to its *builtBinary.
var buildOnce sync.Map

func TestMain(m *testing.M) {
	code := m.Run()
	buildOnce.Range(func(_, v any) bool {
		if b := v.(*builtBinary); b.path != "" {
			_ = os.RemoveAll(filepath.Dir(b.path))
		}
		return true
	})
	os.Exit(code)
}

type builtBinary struct {
	once sync.Once
	path string
	err  error
}

// binary builds the real binary once per version, the way the Makefile
// does.
func binary(t *testing.T, version string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	v, _ := buildOnce.LoadOrStore(version, &builtBinary{})
	b := v.(*builtBinary)
	b.once.Do(func() {
		dir, err := os.MkdirTemp("", "loop-bin-")
		if err != nil {
			b.err = err
			return
		}
		b.path = filepath.Join(dir, "loop")
		build := exec.Command("go", "build", "-trimpath",
			"-ldflags", "-X github.com/iantanwx/loop/internal/cli.Version="+version, "-o", b.path, ".")
		build.Env = append(build.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			b.err = errors.New(string(out))
		}
	})
	if b.err != nil {
		t.Fatalf("go build: %v", b.err)
	}
	return b.path
}

const testVersion = "v9.9.9-test"

type result struct {
	env    map[string]any
	exit   int
	stdout string
}

// loop runs the binary once.
func loop(t *testing.T, bin, instance, dir, stdin string, args ...string) result {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), cli.InstanceEnv+"="+instance, cli.WaitBudgetEnv+"=10s")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	stdout, err := cmd.Output()
	exit := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	var env map[string]any
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatalf("%v: stdout is not one JSON envelope: %v\n%s", args, err, stdout)
	}
	if env["exit"] != float64(exit) {
		t.Fatalf("%v: process exit %d, envelope exit %v", args, exit, env["exit"])
	}
	return result{env: env, exit: exit, stdout: string(stdout)}
}

func want(t *testing.T, r result, exit int, code string) {
	t.Helper()
	if r.exit != exit || (code != "" && r.env["code"] != code) {
		t.Fatalf("want exit %d %s, got %d: %s", exit, code, r.exit, r.stdout)
	}
}

// TestBinary checks the process boundary: one JSON envelope on stdout and
// the envelope's exit as the process exit.
func TestBinary(t *testing.T) {
	bin := binary(t, testVersion)
	instance := filepath.Join(t.TempDir(), "instance") // no server
	cases := []struct {
		args []string
		exit int
		code string
	}{
		{[]string{"schema", "--json"}, 0, ""},
		{[]string{"version"}, 0, ""},
		{[]string{"work", "peek", "--bogus"}, 2, "USAGE"},
		{[]string{"doctor"}, 2, "NOT_IMPLEMENTED"},
	}
	for _, tc := range cases {
		r := loop(t, bin, instance, t.TempDir(), "", tc.args...)
		want(t, r, tc.exit, tc.code)
		if tc.args[0] == "version" {
			if v := r.env["data"].(map[string]any)["version"]; v != testVersion {
				t.Errorf("ldflags version = %v", v)
			}
		}
	}

	// No server: a server verb waits out its budget, then exits 4.
	cmd := exec.Command(bin, "work", "list")
	cmd.Env = append(os.Environ(), cli.InstanceEnv+"="+instance, cli.WaitBudgetEnv+"=500ms")
	start := time.Now()
	out, _ := cmd.Output()
	if cmd.ProcessState.ExitCode() != 4 || !strings.Contains(string(out), `"code":"SERVER_UNAVAILABLE"`) ||
		!strings.Contains(string(out), `"retry_after_seconds":5`) {
		t.Fatalf("no server: exit %d %s", cmd.ProcessState.ExitCode(), out)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the shrunk budget was not used: %v", time.Since(start))
	}
}

type serveProc struct {
	cmd    *exec.Cmd
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	done   chan error
}

func serve(t *testing.T, bin, instance string) *serveProc {
	t.Helper()
	cmd := exec.Command(bin, "serve", "--instance", instance)
	cmd.Dir = t.TempDir()
	cmd.Env = os.Environ()
	p := &serveProc{cmd: cmd, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, done: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			if err := <-p.done; err == nil {
				t.Log("serve exited cleanly at cleanup")
			}
		}
	})
	return p
}

// TestServeEndToEnd starts `loop serve` on a temporary instance and drives
// verbs through the real CLI over the real socket.
func TestServeEndToEnd(t *testing.T) {
	bin := binary(t, testVersion)
	instance := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(instance, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.EnsureAdminCredential(instance); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	srv := serve(t, bin, instance)
	r := loop(t, bin, instance, outside, "", "status")
	want(t, r, 0, "")
	if r.env["server_version"] != testVersion {
		t.Fatalf("server_version = %v", r.env["server_version"])
	}

	// A second server on the same instance refuses to bind.
	second := exec.Command(bin, "serve", "--instance", instance)
	out, _ := second.Output()
	if second.ProcessState.ExitCode() != 2 || !strings.Contains(string(out), `"rule":"instance_locked"`) {
		t.Fatalf("second serve: exit %d %s", second.ProcessState.ExitCode(), out)
	}

	// Host verbs with the credential file, from outside every checkout.
	add := []string{"project", "add", "alpha", "--repo", "iantanwx/alpha", "--path", "/srv/alpha"}
	want(t, loop(t, bin, instance, outside, "", add...), 0, "")
	want(t, loop(t, bin, instance, outside, "", add...), 3, cli.CodeAlreadyExists)
	const value = "ghp_endtoendsecret_0123456789"
	r = loop(t, bin, instance, outside, value+"\n", "secret", "set", "API_TOKEN", "--project", "alpha")
	want(t, r, 0, "")
	if strings.Contains(r.stdout, value) {
		t.Fatalf("secret set echoed the value: %s", r.stdout)
	}
	r = loop(t, bin, instance, outside, "", "project", "show", value)
	want(t, r, 2, cli.CodeNotFound)
	if strings.Contains(r.stdout, value) {
		t.Fatalf("an error envelope echoes a stored secret: %s", r.stdout)
	}
	want(t, loop(t, bin, instance, outside, "", "drain"), 2, cli.CodeNotImplemented)
	want(t, loop(t, bin, instance, outside, "", "work", "list"), 3, cli.CodeCapabilityDenied)

	// A CLI of another release is refused.
	other := binary(t, "v0.0.1-other")
	want(t, loop(t, other, instance, outside, "", "status"), 3, cli.CodeVersionMismatch)

	// Run tokens, issued the way U8 will, from a second store handle.
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(instance, "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	tokens := &rpc.Tokens{Store: st}
	checkoutA, checkoutB := t.TempDir(), t.TempDir()
	tfA, err := tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: "run-a", Kind: cli.PrincipalRun, Project: "alpha",
		Item: "T-A", Checkout: checkoutA, Release: testVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: "run-b", Kind: cli.PrincipalRun, Project: "alpha",
		Item: "T-B", Checkout: checkoutB, Release: testVersion}); err != nil {
		t.Fatal(err)
	}
	// Run A moves an item that run B holds.
	r = loop(t, bin, instance, checkoutA, "", "work", "block", "T-B")
	want(t, r, 3, cli.CodeCapabilityDenied)
	// Run A calls host verbs.
	want(t, loop(t, bin, instance, checkoutA, "", "upgrade"), 3, cli.CodeCapabilityDenied)
	want(t, loop(t, bin, instance, checkoutA, "x-value-123", "secret", "set", "X", "--project", "alpha"), 3, cli.CodeCapabilityDenied)
	// Run A's own item reaches the verb, which U5 builds.
	want(t, loop(t, bin, instance, checkoutA, "", "work", "block", "T-A"), 2, cli.CodeNotImplemented)
	// Run A's token copied into a directory outside its checkout.
	stolen := t.TempDir()
	if err := cli.WriteTokenFile(stolen, tfA); err != nil {
		t.Fatal(err)
	}
	r = loop(t, bin, instance, stolen, "", "work", "show", "T-A")
	want(t, r, 3, cli.CodeCapabilityDenied)
	if r.env["rule"] != "token_outside_checkout" {
		t.Fatalf("rule = %v", r.env["rule"])
	}
	// A tokenless upgrade from inside a registered checkout.
	registered := t.TempDir()
	if err := st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO checkouts (id, project, path, state, created_at, updated_at) VALUES ('c1', 'alpha', ?, 'active', 0, 0)`, registered)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r = loop(t, bin, instance, registered, "", "upgrade")
	want(t, r, 3, cli.CodeCapabilityDenied)
	if r.env["rule"] != "host_verb_from_checkout" {
		t.Fatalf("rule = %v", r.env["rule"])
	}
	// Settlement revokes the token.
	if _, err := tokens.Revoke(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	r = loop(t, bin, instance, checkoutA, "", "work", "show", "T-A")
	if r.env["rule"] != "token_revoked" {
		t.Fatalf("a settled run's token: %s", r.stdout)
	}
	for _, a := range [][2]string{{rpc.AlertTokenOutsideCheckout, "run-a"}, {rpc.AlertHostVerbFromCheckout, registered}} {
		var n int
		if err := st.Read(ctx, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*) FROM alerts WHERE kind = ? AND subject = ?`, a[0], a[1]).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("alert %v: %d rows, want 1", a, n)
		}
	}
	_ = st.Close()

	// SIGKILL leaves a stale socket. The next start removes it and serves.
	if err := srv.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := <-srv.done; err == nil {
		t.Fatal("a SIGKILLed serve reports no error")
	}
	if _, err := os.Lstat(cli.SocketPath(instance)); err != nil {
		t.Fatalf("SIGKILL should leave the socket file: %v", err)
	}
	srv = serve(t, bin, instance)
	want(t, loop(t, bin, instance, outside, "", "status"), 0, "")
	want(t, loop(t, bin, instance, outside, "", "project", "show", "alpha"), 0, "")

	// SIGTERM stops it cleanly with one envelope.
	if err := srv.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-srv.done:
		if err != nil {
			t.Fatalf("serve after SIGTERM: %v\n%s", err, srv.stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not stop on SIGTERM")
	}
	if !strings.Contains(srv.stdout.String(), `"stopped":true`) {
		t.Fatalf("serve stdout = %s", srv.stdout.String())
	}
	if _, err := os.Lstat(cli.SocketPath(instance)); !os.IsNotExist(err) {
		t.Fatalf("a clean stop removes the socket: %v", err)
	}
	if strings.Contains(srv.stderr.String(), value) {
		t.Fatal("a server log line holds the secret")
	}
}
