package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// procService is a service manager that runs `current/loop serve` as a
// child process of the test. It never touches the host's systemd.
type procService struct {
	t        *testing.T
	instance string
	mu       sync.Mutex
	proc     *serveProc
	events   []string
	onStop   func()
}

func (p *procService) Unit(instance string) ([]byte, error) { return []byte("unit " + instance), nil }
func (p *procService) UnitPath() string                     { return filepath.Join(p.instance, "test.unit") }
func (p *procService) Register(context.Context, []byte) error {
	return nil
}

func (p *procService) Start(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, "start")
	p.proc = serve(p.t, filepath.Join(p.instance, "current", "loop"), p.instance)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("unix", cli.SocketPath(p.instance)); err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("the server did not bind its socket")
}

func (p *procService) Stop(context.Context) error {
	p.mu.Lock()
	p.events = append(p.events, "stop")
	proc := p.proc
	p.proc = nil
	p.mu.Unlock()
	if proc != nil && proc.cmd.ProcessState == nil {
		_ = proc.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-proc.done:
			_ = err // a SIGTERM exit is the expected end
		case <-time.After(30 * time.Second):
			p.t.Fatal("the server did not stop")
		}
	}
	if p.onStop != nil {
		p.onStop()
	}
	return nil
}

func copyBinary(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// runVerb runs one command line through the registry with the host verbs.
func runVerb(t *testing.T, h *hostVerbs, args ...string) map[string]any {
	t.Helper()
	var out, errb bytes.Buffer
	code := h.reg.Run(context.Background(), args, &out, &errb)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("%v: %v: %s", args, err, out.String())
	}
	if env["exit"] != float64(code) {
		t.Fatalf("%v: exit %d, envelope %v", args, code, env["exit"])
	}
	return env
}

func testHostVerbs(t *testing.T, instance string, svc release.ServiceManager) *hostVerbs {
	t.Helper()
	r := cli.Surface()
	c := &cli.Client{Instance: instance, Budget: 10 * time.Second}
	c.Attach(r)
	h := newHostVerbs(r, c)
	h.service = func(string) (release.ServiceManager, error) { return svc, nil }
	h.home = func() (string, error) { return t.TempDir(), nil }
	if err := h.register(); err != nil {
		t.Fatal(err)
	}
	return h
}

// TestRegisterRelease is the call run makes once main.go wires it.
func TestRegisterRelease(t *testing.T) {
	r := cli.Surface()
	(&cli.Client{}).Attach(r)
	if err := registerRelease(r, &cli.Client{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Refusal(release.CodeNoRollback); !ok {
		t.Fatal("NO_ROLLBACK is not registered")
	}
}

func TestHostVerbsRegisterAndRefuse(t *testing.T) {
	instance := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(instance, 0o700); err != nil {
		t.Fatal(err)
	}
	h := testHostVerbs(t, instance, &procService{t: t, instance: instance})

	start := time.Now()
	env := runVerb(t, h, "rollback")
	if env["code"] != cli.CodeNotFound || env["rule"] != "not_installed" {
		t.Fatalf("rollback = %v", env)
	}
	env = runVerb(t, h, "restore")
	if env["exit"] != float64(3) || env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("restore without the credential = %v", env)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("a host verb waited on a missing server: %v", time.Since(start))
	}
	env = runVerb(t, h, "schema")
	raw, _ := json.Marshal(env["data"])
	for _, code := range []string{release.CodeNoRollback, release.CodeSignatureInvalid, release.CodeDrift} {
		if !strings.Contains(string(raw), code) {
			t.Errorf("the schema lacks %s", code)
		}
	}
}

// TestReachableNeverWaits: a socket file that no server listens on reads
// unreachable at once, so restore skips the drain without waiting.
func TestReachableNeverWaits(t *testing.T) {
	instance := filepath.Join(t.TempDir(), "i")
	sock := cli.SocketPath(instance)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close() // the file stays, and nothing listens
	ctl := &serverControl{instance: instance}
	start := time.Now()
	if ctl.Reachable(context.Background()) {
		t.Fatal("a stale socket read reachable")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Reachable waited %v", time.Since(start))
	}
}

// TestUpgradeAcrossARealServer upgrades a running `loop serve` from
// v1.0.0 to v1.1.0 through `loop upgrade`, then rolls it back (AE6). A
// CLI that calls while the server is down completes against the new
// server. A run started under v1.0.0 still holds its token after the
// drain, and its calls succeed against v1.1.0, because both schemas hold
// the verb.
func TestUpgradeAcrossARealServer(t *testing.T) {
	const v1, v2 = "v1.0.0", "v1.1.0"
	bin1, bin2 := binary(t, v1), binary(t, v2)
	ctx := context.Background()
	instance := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(instance, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveHost(filepath.Join(instance, config.HostConfigFile), config.DefaultHost(instance)); err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.EnsureAdminCredential(instance); err != nil {
		t.Fatal(err)
	}
	layout := release.Layout{Dir: instance}

	// Synthetic pinned binaries, already in place, so the upgrade fetches
	// none.
	var pins []release.Pin
	for _, name := range []string{"bd", "dolt"} {
		body := []byte("#!/bin/sh\necho " + name + "\n")
		copyBinaryBytes(t, layout.Bin(name), body)
		pins = append(pins, release.Pin{Name: name, Version: "1", Platform: runtime.GOOS + "/" + runtime.GOARCH,
			ArchiveSHA256: strings.Repeat("a", 64), BinarySHA256: sha(body)})
	}
	self, err := release.ThisRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	meta := func(v string) release.Metadata {
		m := self
		m.Version, m.Pins = v, pins
		return m
	}
	copyBinary(t, bin1, layout.Binary(v1))
	copyBinaryBytes(t, filepath.Join(layout.VersionDir(v1), release.MetadataFile), meta(v1).Encode())
	if err := layout.SetCurrent(v1); err != nil {
		t.Fatal(err)
	}
	if err := layout.Record(v1); err != nil {
		t.Fatal(err)
	}

	// A run of v1.0.0 holds a token in its checkout.
	st, err := store.Open(ctx, filepath.Join(instance, "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	if _, err := (&rpc.Tokens{Store: st}).IssueToCheckout(ctx, rpc.IssueRequest{RunID: "run-v1", Kind: cli.PrincipalRun,
		Item: "T-1", Checkout: checkout, Release: v1}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// The signed v1.1.0 release.
	pub, sec, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	asset := release.AssetName(runtime.GOOS, runtime.GOARCH)
	bin2Bytes, _ := os.ReadFile(bin2)
	metaRaw := meta(v2).Encode()
	sums := release.Checksums{asset: sha(bin2Bytes), release.MetadataFile: sha(metaRaw)}
	sig, err := release.Sign(sec, sums.Bytes(), release.TrustedComment(v2))
	if err != nil {
		t.Fatal(err)
	}
	files["/"+v2+"/"+asset] = bin2Bytes
	files["/"+v2+"/"+release.MetadataFile] = metaRaw
	files["/"+v2+"/"+release.ChecksumsFile] = sums.Bytes()
	files["/"+v2+"/"+release.SignatureFile] = sig
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	svc := &procService{t: t, instance: instance}
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Stop(ctx) }()
	if r := loop(t, bin1, instance, checkout, "", "status"); r.exit != 0 || r.env["server_version"] != v1 {
		t.Fatalf("status before the upgrade: %s", r.stdout)
	}

	// While the server is down, the run calls status. It waits, and it
	// completes against v1.1.0.
	var waiting sync.WaitGroup
	var during result
	svc.onStop = func() {
		svc.onStop = nil
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			during = loopNoFatal(bin1, instance, checkout, "status")
		}()
		time.Sleep(300 * time.Millisecond) // the call starts while the socket is gone
	}

	// The upgrade runs in this process as the v1.0.0 CLI.
	old := cli.Version
	cli.Version = v1
	defer func() { cli.Version = old }()
	h := testHostVerbs(t, instance, svc)
	h.source = func() release.Source { return release.Source{Base: srv.URL, HTTP: srv.Client()} }
	h.keys = func() ([]release.PublicKey, error) { return []release.PublicKey{pub}, nil }
	env := runVerb(t, h, "upgrade", v2)
	if env["exit"] != float64(0) {
		t.Fatalf("upgrade = %v", env)
	}
	data := env["data"].(map[string]any)
	if data["drain"].(map[string]any)["skipped"] == nil || data["snapshot"] == "" || data["started"] != true {
		t.Fatalf("upgrade report = %v", data)
	}
	waiting.Wait()
	if during.exit != 0 || during.env["server_version"] != v2 {
		t.Fatalf("the waiting CLI of the v1.0.0 run: exit %d %s", during.exit, during.stdout)
	}
	// The exception is the handshake's, not a lax check: an old CLI with
	// no run token is refused.
	if r := loop(t, bin1, instance, t.TempDir(), "", "status"); r.exit != 3 || r.env["code"] != cli.CodeVersionMismatch {
		t.Fatalf("an old CLI with no token: %s", r.stdout)
	}
	if r := loop(t, bin2, instance, t.TempDir(), "", "status"); r.exit != 0 || r.env["server_version"] != v2 {
		t.Fatalf("status after the upgrade: %s", r.stdout)
	}

	// Rollback through the v1.1.0 CLI returns the service to v1.0.0.
	cli.Version = v2
	h = testHostVerbs(t, instance, svc)
	env = runVerb(t, h, "rollback")
	if env["exit"] != float64(0) {
		t.Fatalf("rollback = %v", env)
	}
	if r := loop(t, bin1, instance, t.TempDir(), "", "status"); r.exit != 0 || r.env["server_version"] != v1 {
		t.Fatalf("status after the rollback: %s", r.stdout)
	}
	if got := strings.Join(svc.events, " "); got != "start stop start stop start" {
		t.Fatalf("service events = %q", got)
	}
}

func loopNoFatal(bin, instance, dir string, args ...string) result {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), cli.InstanceEnv+"="+instance, cli.WaitBudgetEnv+"=60s")
	stdout, _ := cmd.Output()
	var env map[string]any
	_ = json.Unmarshal(stdout, &env)
	return result{env: env, exit: cmd.ProcessState.ExitCode(), stdout: string(stdout)}
}

func copyBinaryBytes(t *testing.T, dst string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// restore takes an optional snapshot argument, and the handler passes it
// to the restore: a named snapshot that is not there is NOT_FOUND, and the
// hint names it.
func TestRestoreNamesItsSnapshot(t *testing.T) {
	instance := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(instance, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.EnsureAdminCredential(instance); err != nil {
		t.Fatal(err)
	}
	h := testHostVerbs(t, instance, &procService{t: t, instance: instance})
	env := runVerb(t, h, "restore", "nightly-1.db")
	if env["code"] != cli.CodeNotFound || env["rule"] != "no_snapshot" || !strings.Contains(env["hint"].(string), "nightly-1.db") {
		t.Fatalf("restore nightly-1.db = %v", env)
	}
}
