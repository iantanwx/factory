package rpc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	loopv1 "github.com/iantanwx/loop/gen/loop/v1"
	"github.com/iantanwx/loop/internal/cli"
)

func TestSecondServerOnTheSameInstanceRefusesToBind(t *testing.T) {
	dir := privateDir(t)
	first, err := LockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockInstance(dir); !errors.Is(err, ErrInstanceLocked) {
		t.Fatalf("second lock = %v, want ErrInstanceLocked", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := LockInstance(dir)
	if err != nil {
		t.Fatalf("a released lock is free again: %v", err)
	}
	_ = again.Release()
}

func TestStaleSocketIsRemovedOnlyAfterTheProbeFindsNoServer(t *testing.T) {
	sock := cli.SocketPath(t.TempDir())
	if err := PrepareSocket(sock); err != nil {
		t.Fatal(err)
	}
	live, err := Bind(sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := PrepareSocket(sock); !errors.Is(err, ErrServerRunning) {
		t.Fatalf("PrepareSocket with a live server = %v, want ErrServerRunning", err)
	}
	// A SIGKILL leaves the socket file behind: close without unlinking.
	live.SetUnlinkOnClose(false)
	_ = live.Close()
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("the stale socket should remain: %v", err)
	}
	if err := PrepareSocket(sock); err != nil {
		t.Fatalf("PrepareSocket on a stale socket: %v", err)
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatalf("the stale socket should be gone: %v", err)
	}
}

func TestSocketIsPrivate(t *testing.T) {
	sock := cli.SocketPath(t.TempDir())
	if err := PrepareSocket(sock); err != nil {
		t.Fatal(err)
	}
	l, err := Bind(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	info, _ := os.Stat(sock)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode %v, want 0600", perm)
	}
	dinfo, _ := os.Stat(filepath.Dir(sock))
	if perm := dinfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("socket directory mode %v, want 0700", perm)
	}
	if len(sock) > 100 {
		t.Fatalf("socket path is %d bytes; the limit is 100", len(sock))
	}
}

func TestSocketPathFallsBackWhenTheInstancePathIsTooLong(t *testing.T) {
	short := cli.SocketPath("/srv/loop")
	if short != "/srv/loop/run/loop.sock" {
		t.Fatalf("short instance socket = %s", short)
	}
	long := cli.SocketPath("/" + strings.Repeat("x", 120))
	if len(long) > 100 || !strings.HasPrefix(long, os.TempDir()) {
		t.Fatalf("long instance socket = %s", long)
	}
	if long != cli.SocketPath("/"+strings.Repeat("x", 120)) {
		t.Fatal("the fallback path must be stable")
	}
}

// A real cross-uid connection needs a second user, which this test cannot
// create, so the check runs on an injected credential.
func TestConnectionFromAnotherUIDIsRefused(t *testing.T) {
	if err := CheckPeerUID(PeerInfo{UID: os.Getuid() + 1}, os.Getuid()); err == nil {
		t.Fatal("CheckPeerUID admitted another uid")
	}
	creds := &peerCreds{ownUID: os.Getuid(), read: func(net.Conn) (PeerInfo, error) {
		return PeerInfo{UID: os.Getuid() + 1, PID: 42}, nil
	}}
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	if _, _, err := creds.ServerHandshake(a); err == nil || !strings.Contains(err.Error(), "uid") {
		t.Fatalf("handshake with another uid = %v, want a refusal", err)
	}
	creds.read = func(net.Conn) (PeerInfo, error) { return PeerInfo{UID: os.Getuid(), PID: 42}, nil }
	if _, info, err := creds.ServerHandshake(a); err != nil || info.(peerAuthInfo).Peer.PID != 42 {
		t.Fatalf("handshake with this uid = %v %v", info, err)
	}
}

func TestRealSocketReadsThePeerCredentials(t *testing.T) {
	h := newHarness(t)
	var seen PeerInfo
	h.handle("status", func(ctx context.Context, c *Call) cli.Result {
		seen = c.Principal.Peer
		return cli.Found(nil)
	})
	h.start()
	if _, code := h.run(t.TempDir(), "status"); code != 0 {
		t.Fatalf("status exit %d", code)
	}
	if seen.UID != os.Getuid() || seen.PID != os.Getpid() {
		t.Fatalf("peer = %+v, want uid %d pid %d", seen, os.Getuid(), os.Getpid())
	}
}

func TestCLIOfAnotherVersionIsRefused(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Version = "v2.0.0" })
	h.handle("status", found)
	h.start()
	env, code := h.run(t.TempDir(), "status")
	wantRefusal(t, env, code, 3, cli.CodeVersionMismatch, "version_mismatch")
	if env["server_version"] != "v2.0.0" {
		t.Fatalf("server_version = %v", env["server_version"])
	}
}

func TestRunOfThePreviousReleaseKeepsCalling(t *testing.T) {
	// The CLI is cli.Version ("dev"); the server is v2 and replaced dev.
	h := newHarness(t, func(o *Options) { o.Version = "v2.0.0"; o.PreviousVersion = cli.Version })
	h.handle("work show", found)
	h.start()
	old := h.issue("run-old", cli.PrincipalRun, "T-1") // issued under cli.Version
	if env, code := h.run(old, "work", "show", "T-1"); code != 0 {
		t.Fatalf("a run of the previous release should succeed: %v", env)
	}

	// A token whose run started under v2 gains nothing from an old CLI.
	ctx := context.Background()
	checkout := filepath.Join(h.dir, "checkouts", "run-new")
	if _, err := h.tokens.IssueToCheckout(ctx, IssueRequest{RunID: "run-new", Kind: cli.PrincipalRun,
		Item: "T-2", Checkout: checkout, Release: "v2.0.0"}); err != nil {
		t.Fatal(err)
	}
	env, code := h.run(checkout, "work", "show", "T-2")
	wantRefusal(t, env, code, 3, cli.CodeVersionMismatch, "")
	// No token: no exception.
	env, code = h.run(t.TempDir(), "status")
	wantRefusal(t, env, code, 3, cli.CodeVersionMismatch, "")
}

func TestRunTokenMovesOnlyItsOwnItem(t *testing.T) {
	h := newHarness(t)
	h.handle("work block", found)
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	h.issue("run-b", cli.PrincipalRun, "T-B")
	env, code := h.run(a, "work", "block", "T-B")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "item_not_held")
	if env["item"] != "T-B" {
		t.Fatalf("refusal item = %v", env["item"])
	}
	if env, code := h.run(a, "work", "block", "T-A"); code != 0 {
		t.Fatalf("run A moves its own item: %v", env)
	}
}

func TestHoldsItemHookDecides(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.HoldsItem = func(_ context.Context, p Principal, item string) (bool, error) {
			return p.RunID == "run-a" && item == "T-LEASED", nil
		}
	})
	h.handle("work block", found)
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	if env, code := h.run(a, "work", "block", "T-LEASED"); code != 0 {
		t.Fatalf("the hook grants T-LEASED: %v", env)
	}
	env, code := h.run(a, "work", "block", "T-A")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "item_not_held")
}

// The ReadsItem hook opens a read verb on an item the run does not hold,
// and never a write verb.
func TestReadsItemHookOpensReadsOnly(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.ReadsItem = func(_ context.Context, p Principal, item string) (bool, error) {
			return p.RunID == "run-a" && item == "E-1", nil
		}
	})
	h.handle("work show", found)
	h.handle("work block", found)
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	if env, code := h.run(a, "work", "show", "E-1"); code != 0 {
		t.Fatalf("the hook opens work show of E-1: %v", env)
	}
	env, code := h.run(a, "work", "block", "E-1")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "item_not_held")
	env, code = h.run(a, "work", "show", "E-2")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "item_not_held")
}

func TestRunTokenCannotCallHostVerbs(t *testing.T) {
	h := newHarness(t)
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.handle("secret set", found)
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	env, code := h.runWith(context.Background(), h.client(a), a, strings.NewReader("value-123456"), "secret", "set", "TOKEN", "--project", "alpha")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "principal_not_allowed")
	env, code = h.run(a, "upgrade")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "principal_not_allowed")
}

func TestTokenlessCallsReachMetaVerbsOnly(t *testing.T) {
	h := newHarness(t)
	h.handle("status", found)
	h.handle("work list", found)
	h.start()
	if env, code := h.run(t.TempDir(), "status"); code != 0 {
		t.Fatalf("status is open to anyone: %v", env)
	}
	env, code := h.run(t.TempDir(), "work", "list")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "principal_not_allowed")
}

func TestHostCredentialComesOnlyFromTheFile(t *testing.T) {
	h := newHarness(t)
	h.handle("project list", found)
	h.start()
	// No file: a credential in the environment changes nothing.
	t.Setenv("LOOP_ADMIN_CREDENTIAL", "loopadmin_from_env")
	t.Setenv("LOOP_HOST_ADMIN", "loopadmin_from_env")
	env, code := h.run(t.TempDir(), "project", "list")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "principal_not_allowed")

	cred, err := EnsureAdminCredential(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(cli.AdminCredentialPath(h.dir))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode %v", info.Mode().Perm())
	}
	t.Setenv("LOOP_ADMIN_CREDENTIAL", cred) // still ignored; the file decides
	if env, code := h.run(t.TempDir(), "project", "list"); code != 0 {
		t.Fatalf("with the file a host verb runs: %v", env)
	}
	again, err := EnsureAdminCredential(h.dir)
	if err != nil || again != cred {
		t.Fatal("EnsureAdminCredential must never replace a credential")
	}
}

func TestWrongAdminCredentialIsRefused(t *testing.T) {
	h := newHarness(t)
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.handle("project list", found)
	h.start()
	conn, err := grpc.NewClient("unix://"+cli.SocketPath(h.dir), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	h.proc.setCwd(t.TempDir())
	env, _ := rawCall(t, conn, "/loop.v1.ProjectService/List", &loopv1.VerbRequest{Verb: "project list", ClientVersion: cli.Version},
		cli.AdminCredentialHeader, "loopadmin_wrong")
	if env.GetRefusal().GetRule() != "admin_credential_invalid" {
		t.Fatalf("got %v", env)
	}
}

func TestTokenlessUpgradeFromInsideACheckoutIsRefusedAndAlerts(t *testing.T) {
	h := newHarness(t)
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.start()
	checkout := filepath.Join(h.dir, "registered")
	h.checkouts = []string{checkout}
	inside := filepath.Join(checkout, "src")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	env, code := h.run(inside, "upgrade")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "host_verb_from_checkout")
	if n := h.count(`SELECT count(*) FROM alerts WHERE kind = ? AND state = 'open'`, AlertHostVerbFromCheckout); n != 1 {
		t.Fatalf("alerts = %d, want 1", n)
	}
	if n := h.count(`SELECT count(*) FROM audit WHERE action = ?`, AlertHostVerbFromCheckout); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}

	// The caller sits outside, but its parent sits inside: still refused.
	outside := t.TempDir()
	h.proc.mu.Lock()
	h.proc.parents[os.Getpid()] = 4242
	h.proc.cwd[4242] = checkout
	h.proc.mu.Unlock()
	env, code = h.run(outside, "upgrade")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "host_verb_from_checkout")

	// From outside every checkout the gate passes, and the verb is not
	// built yet.
	h.proc.mu.Lock()
	delete(h.proc.parents, os.Getpid())
	h.proc.mu.Unlock()
	env, code = h.run(outside, "upgrade")
	wantRefusal(t, env, code, 2, cli.CodeNotImplemented, "")
}

func TestDefaultCheckoutListerHoldsLiveTokenCheckouts(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Checkouts = nil })
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.handle("project list", found)
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	// Tokenless, from a run's checkout: the client finds the token there,
	// so run from a subdirectory with the token file hidden.
	sub := filepath.Join(a, "deep")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	c := h.client(t.TempDir()) // the CLI finds no token
	h.proc.setCwd(sub)         // the server sees the caller inside run A's checkout
	c.Stdin = nil
	r := cli.Surface()
	c.Attach(r)
	var out strings.Builder
	code := r.Run(context.Background(), []string{"project", "list"}, &out, &strings.Builder{})
	if code != 3 || !strings.Contains(out.String(), "host_verb_from_checkout") {
		t.Fatalf("exit %d %s", code, out.String())
	}
}

func TestSettledRunTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	h.handle("work show", found)
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	if _, code := h.run(a, "work", "show", "T-A"); code != 0 {
		t.Fatal("a live token works")
	}
	if n, err := h.tokens.Revoke(context.Background(), "run-a"); err != nil || n != 1 {
		t.Fatalf("Revoke = %d %v", n, err)
	}
	env, code := h.run(a, "work", "show", "T-A")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "token_revoked")
	env, code = h.run(a, "status")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "token_revoked")
}

func TestRunTokenFromAnotherCheckoutIsRefusedAndAlerts(t *testing.T) {
	h := newHarness(t)
	h.handle("work show", found)
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	b := h.issue("run-b", cli.PrincipalRun, "T-B")
	c := h.client(a) // the CLI reads run A's token
	h.proc.setCwd(b)
	r := cli.Surface()
	c.Attach(r)
	var out strings.Builder
	code := r.Run(context.Background(), []string{"work", "show", "T-A"}, &out, &strings.Builder{})
	if code != 3 || !strings.Contains(out.String(), `"rule":"token_outside_checkout"`) {
		t.Fatalf("exit %d %s", code, out.String())
	}
	if n := h.count(`SELECT count(*) FROM alerts WHERE kind = ?`, AlertTokenOutsideCheckout); n != 1 {
		t.Fatalf("alerts = %d, want 1", n)
	}
}

func TestUnwiredVerbAnswersNotImplementedThroughTheSocket(t *testing.T) {
	h := newHarness(t)
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	env, code := h.run(a, "work", "list")
	wantRefusal(t, env, code, 2, cli.CodeNotImplemented, "not_implemented")
	for _, k := range []string{"ok", "verb", "request_id", "server_version", "hint", "allowed_next"} {
		if _, ok := env[k]; !ok {
			t.Fatalf("envelope lacks %s: %v", k, env)
		}
	}
	if env["server_version"] != cli.Version {
		t.Fatalf("server_version = %v", env["server_version"])
	}
}

func TestGRPCStatusMatchesTheRefusalCode(t *testing.T) {
	h := newHarness(t)
	h.start()
	conn, err := grpc.NewClient("unix://"+cli.SocketPath(h.dir), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	h.proc.setCwd(t.TempDir())
	env, st := rawCall(t, conn, "/loop.v1.WorkService/List", &loopv1.VerbRequest{Verb: "work list", ClientVersion: cli.Version})
	if env.GetRefusal().GetCode() != cli.CodeCapabilityDenied || st.Code().String() != "PermissionDenied" {
		t.Fatalf("got %v %v", env, st)
	}
	env, st = rawCall(t, conn, "/loop.v1.WorkService/List", &loopv1.VerbRequest{Verb: "work peek", ClientVersion: cli.Version})
	if env.GetRefusal().GetRule() != "verb_mismatch" || st.Code().String() != "InvalidArgument" {
		t.Fatalf("got %v %v", env, st)
	}
	env, _ = rawCall(t, conn, "/loop.v1.WorkService/Teleport", &loopv1.VerbRequest{Verb: "work teleport", ClientVersion: cli.Version})
	if env.GetRefusal().GetRule() != "unknown_verb" {
		t.Fatalf("got %v", env)
	}
}

func TestServerValidatesArgumentsAndFlags(t *testing.T) {
	h := newHarness(t)
	h.handle("work show", found)
	h.start()
	conn, err := grpc.NewClient("unix://"+cli.SocketPath(h.dir), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	tf, _ := cli.ReadTokenFile(a)
	h.proc.setCwd(a)
	for name, req := range map[string]*loopv1.VerbRequest{
		"missing argument": {Verb: "work show"},
		"unknown flag":     {Verb: "work show", Args: []string{"T-A"}, Flags: map[string]string{"bogus": "1"}},
		"extra argument":   {Verb: "work show", Args: []string{"T-A", "T-B"}},
	} {
		req.RunToken, req.ClientVersion = tf.Token, cli.Version
		env, _ := rawCall(t, conn, "/loop.v1.WorkService/Show", req)
		if env.GetRefusal().GetCode() != cli.CodeUsage {
			t.Errorf("%s: got %v", name, env)
		}
	}
}

func TestErrorEnvelopesAndLogLinesHoldNoStoredSecret(t *testing.T) {
	const value = "sk-live-0123456789abcdef"
	h := newHarness(t)
	h.red.SetStored(nil)
	h.red.SetLive("alpha/API_KEY", value)
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.handle("project show", func(_ context.Context, c *Call) cli.Result {
		h.srv.Logger().Info("handler saw", "project", c.Inv.Arg("project"))
		return cli.Refuse(cli.Refusal{Code: cli.CodeNotFound, Rule: "project_not_found",
			Item: c.Inv.Arg("project"), Hint: "no project " + c.Inv.Arg("project")})
	})
	h.start()
	env, code := h.run(t.TempDir(), "project", "show", value)
	if code != 2 || env["code"] != cli.CodeNotFound {
		t.Fatalf("got %d %v", code, env)
	}
	if strings.Contains(env["hint"].(string), value) || strings.Contains(env["item"].(string), value) {
		t.Fatalf("the envelope echoes the secret: %v", env)
	}
	if !strings.Contains(env["hint"].(string), "[REDACTED:alpha/API_KEY]") {
		t.Fatalf("hint = %v", env["hint"])
	}
	logs := h.logs.String()
	if strings.Contains(logs, value) {
		t.Fatalf("a log line holds the secret:\n%s", logs)
	}
	if !strings.Contains(logs, "REDACTED") {
		t.Fatalf("the log should show the redaction:\n%s", logs)
	}
}

func TestEveryLiveTokenIsRedacted(t *testing.T) {
	h := newHarness(t)
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	tf, _ := cli.ReadTokenFile(a)
	if got := h.red.Redact("token " + tf.Token); strings.Contains(got, tf.Token) {
		t.Fatalf("an issued token is not redacted: %s", got)
	}
	// A restarted server knows the token by hash only; LoadLive restores it.
	fresh := &Tokens{Store: h.st, Redactor: newRedactor()}
	if err := fresh.LoadLive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fresh.Redactor.Redact(tf.Token); strings.Contains(got, tf.Token) {
		t.Fatalf("LoadLive did not restore the token: %s", got)
	}
}

func TestTokensAreStoredHashedAndBound(t *testing.T) {
	h := newHarness(t)
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	tf, _ := cli.ReadTokenFile(a)
	if len(tf.Token) != len(tokenPrefix)+64 {
		t.Fatalf("token length %d; want 256 bits in hex", len(tf.Token))
	}
	if n := h.count(`SELECT count(*) FROM run_tokens WHERE token_hash = ?`, tf.Token); n != 0 {
		t.Fatal("the store holds the token in plain text")
	}
	info, _ := os.Stat(filepath.Join(a, cli.LoopDir, cli.TokenFileName))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v", info.Mode().Perm())
	}
	p, err := h.tokens.Verify(context.Background(), tf.Token)
	if err != nil || p.RunID != "run-a" || p.Item != "T-A" || p.Kind != cli.PrincipalRun || p.Checkout != a || p.Stage != "implement" {
		t.Fatalf("Verify = %+v %v", p, err)
	}
	if p.Actor() != "run:run-a" {
		t.Fatalf("actor = %s", p.Actor())
	}
	if _, err := h.tokens.Verify(context.Background(), tokenPrefix+strings.Repeat("0", 64)); !errors.Is(err, ErrTokenUnknown) {
		t.Fatalf("unknown token = %v", err)
	}
	if _, err := h.tokens.Issue(context.Background(), IssueRequest{RunID: "x", Kind: "host", Checkout: "/x"}); err == nil {
		t.Fatal("a host token must be refused")
	}
}

func TestReadyNotificationReachesSystemd(t *testing.T) {
	addr := filepath.Join(t.TempDir(), "notify")
	if len(addr) > 100 {
		addr = filepath.Join(os.TempDir(), "loop-notify-test")
		_ = os.Remove(addr)
	}
	l, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close(); _ = os.Remove(addr) }()
	t.Setenv(NotifySocketEnv, addr)
	if err := NotifyReady(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = l.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := l.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Fatalf("read %q %v", buf[:n], err)
	}
	t.Setenv(NotifySocketEnv, "")
	if err := NotifyReady(); err != nil {
		t.Fatalf("no NOTIFY_SOCKET is a no-op: %v", err)
	}
}

func TestHandlerGetsThePrincipal(t *testing.T) {
	h := newHarness(t)
	var fromCall, fromCtx Principal
	h.handle("work show", func(ctx context.Context, c *Call) cli.Result {
		fromCall = c.Principal
		fromCtx, _ = PrincipalFrom(ctx)
		return cli.Found(nil)
	})
	h.start()
	a := h.issue("run-a", cli.PrincipalRun, "T-A")
	if _, code := h.run(a, "work", "show", "T-A"); code != 0 {
		t.Fatal("work show failed")
	}
	if fromCall.RunID != "run-a" || fromCtx.Actor() != "run:run-a" {
		t.Fatalf("principal = %+v / %+v", fromCall, fromCtx)
	}
}

func TestHandleRefusesUnknownClientAndDuplicateVerbs(t *testing.T) {
	h := newHarness(t)
	if err := h.srv.Handle("work teleport", found); err == nil {
		t.Fatal("an unknown verb was accepted")
	}
	if err := h.srv.Handle("upgrade", found); err == nil {
		t.Fatal("a client verb was accepted")
	}
	if err := h.srv.Handle("work show", found); err != nil {
		t.Fatal(err)
	}
	if err := h.srv.Handle("work show", found); err == nil {
		t.Fatal("a second handler was accepted")
	}
}

func TestNewRefusesARegistryWithoutAMatrix(t *testing.T) {
	h := newHarness(t)
	opts := h.opts
	opts.Registry = cli.New()
	if _, err := New(opts); err == nil {
		t.Fatal("a registry without a capability matrix must be refused")
	}
}

// Unhandled names each server verb with no handler, in path order. It
// leaves out a client verb, a verb with a handler, and a verb that the
// registry answers itself, as it answers schema.
func TestUnhandledListsServerVerbsWithNoHandlerInPathOrder(t *testing.T) {
	reg := cli.NewRegistry()
	for _, v := range []cli.Verb{
		{Noun: "zeta", Name: "poke", Side: cli.SideServer},
		{Noun: "work", Name: "show", Side: cli.SideServer},
		{Noun: "work", Name: "open", Side: cli.SideClient},
		{Noun: "alpha", Name: "poke", Side: cli.SideServer},
		{Noun: "beta", Name: "poke", Side: cli.SideServer},
	} {
		v.Summary = "test"
		if err := reg.RegisterVerb(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.SetHandler("beta poke", func(context.Context, cli.Invocation) cli.Result { return cli.Found(nil) }); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetCapabilityMatrix(cli.CapabilityMatrix{}); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(o *Options) { o.Registry = reg })
	if err := h.srv.Handle("work show", found); err != nil {
		t.Fatal(err)
	}
	if got, want := h.srv.Unhandled(), []string{"alpha poke", "zeta poke"}; !slices.Equal(got, want) {
		t.Fatalf("Unhandled() = %q, want %q", got, want)
	}
}
