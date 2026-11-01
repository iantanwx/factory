package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/gitx/gittest"
)

// goldenKeyParts and goldenKey freeze the key canonicalisation (KTD13).
// Releases N and N+1 must derive this exact key for this command. Never
// change goldenKey: change keyScheme and keep the old scheme instead.
var goldenKeyParts = KeyParts{
	RunID:          "run-42",
	Verb:           "work note",
	Args:           []string{"T-7"},
	Flags:          map[string]string{"body": "looks good", "@content_hash": "ignored"},
	ExpectedStatus: "in_review",
	Seq:            9,
	ContentHash:    "body:5f1c",
}

const goldenKey = "k1-e3d5b6e4ce6a2f8e41d2298e1f0dbc63c014d187a602a25f29514b474168ac11"

func TestKeyCanonicalisationIsFrozen(t *testing.T) {
	if got := DeriveKey(goldenKeyParts); got != goldenKey {
		t.Fatalf("DeriveKey changed: got %s, want %s. Canonicalisation is frozen across releases.", got, goldenKey)
	}
}

func TestKeyDependsOnEveryPart(t *testing.T) {
	base := DeriveKey(goldenKeyParts)
	mutations := map[string]func(*KeyParts){
		"run":     func(p *KeyParts) { p.RunID = "run-43" },
		"verb":    func(p *KeyParts) { p.Verb = "pr reply" },
		"args":    func(p *KeyParts) { p.Args = []string{"T-8"} },
		"flags":   func(p *KeyParts) { p.Flags = map[string]string{"body": "other"} },
		"status":  func(p *KeyParts) { p.ExpectedStatus = "done" },
		"seq":     func(p *KeyParts) { p.Seq = 10 },
		"content": func(p *KeyParts) { p.ContentHash = "body:5f1d" },
		"request": func(p *KeyParts) { p.RequestID = "coord-1" },
	}
	for name, mutate := range mutations {
		p := goldenKeyParts
		mutate(&p)
		if DeriveKey(p) == base {
			t.Errorf("changing %s does not change the key", name)
		}
	}
	p := goldenKeyParts
	p.Flags = map[string]string{"body": "looks good"}
	if DeriveKey(p) != base {
		t.Error("a reserved transport field must not change the key")
	}
}

func TestArgsHashIgnoresFlagOrderAndReservedFields(t *testing.T) {
	a := ArgsHash("inbox defer", []string{"E-1"}, map[string]string{"until": "2026-10-21T08:00:00Z", "x": "1"})
	b := ArgsHash("inbox defer", []string{"E-1"}, map[string]string{"x": "1", "until": "2026-10-21T08:00:00Z", "@stdin": "zz"})
	if a != b {
		t.Fatal("ArgsHash depends on map order or reserved fields")
	}
	if a == ArgsHash("inbox defer", []string{"E-2"}, map[string]string{"until": "2026-10-21T08:00:00Z", "x": "1"}) {
		t.Fatal("ArgsHash ignores the arguments")
	}
}

func TestContentHash(t *testing.T) {
	ctx := context.Background()
	r := New()
	note, _ := r.Verb("work note")
	h1, _ := ContentHash(ctx, Invocation{Verb: note, Flags: map[string]string{"body": "a"}}, "", "")
	h2, _ := ContentHash(ctx, Invocation{Verb: note, Flags: map[string]string{"body": "b"}}, "", "")
	if h1 == h2 || !strings.HasPrefix(h1, "body:") {
		t.Fatalf("body hashes %s %s", h1, h2)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.yaml"), []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dec, _ := r.Verb("epic decompose")
	hf, err := ContentHash(ctx, Invocation{Verb: dec, Flags: map[string]string{"file": "plan.yaml"}}, dir, dir)
	if err != nil || !strings.HasPrefix(hf, "file:") {
		t.Fatalf("file hash %s %v", hf, err)
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "one"},
	} {
		cmd := gittest.Command(append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	push, _ := r.Verb("branch push")
	head1, err := ContentHash(ctx, Invocation{Verb: push, Flags: map[string]string{}}, repo, repo)
	if err != nil || !strings.HasPrefix(head1, "head:") {
		t.Fatalf("head hash %s %v", head1, err)
	}
	cmd := gittest.Command("-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "two")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	head2, _ := ContentHash(ctx, Invocation{Verb: push, Flags: map[string]string{}}, repo, repo)
	if head1 == head2 {
		t.Fatal("a new HEAD must change the push's content hash")
	}
}

func TestPendingReserveReuseAndClear(t *testing.T) {
	checkout := t.TempDir()
	p := OpenPending(checkout)
	entry := PendingEntry{Verb: "work note", ArgsHash: "a1", ContentHash: "c1", Created: time.Now()}
	n := 0
	derive := func() (string, error) { n++; return "key-" + string(rune('0'+n)), nil }

	k1, reused, err := p.Reserve(entry, derive)
	if err != nil || reused || k1 != "key-1" {
		t.Fatalf("first reserve = %s %v %v", k1, reused, err)
	}
	k2, reused, _ := p.Reserve(entry, derive)
	if !reused || k2 != k1 {
		t.Fatalf("a repeat reuses the key: %s %v", k2, reused)
	}
	other := entry
	other.ContentHash = "c2"
	k3, reused, _ := p.Reserve(other, derive)
	if reused || k3 == k1 {
		t.Fatalf("other content gets a new key: %s", k3)
	}
	info, _ := os.Stat(filepath.Join(checkout, LoopDir, PendingFileName))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("pending log mode %v", info.Mode().Perm())
	}

	// A killed CLI may leave a torn last line. It costs only itself.
	f, _ := os.OpenFile(filepath.Join(checkout, LoopDir, PendingFileName), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"key":"torn`)
	_ = f.Close()
	entries, err := p.Entries()
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %v %v", entries, err)
	}

	if err := p.Clear(k1); err != nil {
		t.Fatal(err)
	}
	entries, _ = p.Entries()
	if len(entries) != 1 || entries[0].Key != k3 {
		t.Fatalf("after clear: %v", entries)
	}
	k4, reused, _ := p.Reserve(entry, derive)
	if reused || k4 == k1 {
		t.Fatal("after a clear the same command gets a new key")
	}
}

func TestWaitBudgetStaysBelowTheToolTimeout(t *testing.T) {
	if DefaultWaitBudget != 100*time.Second {
		t.Fatalf("DefaultWaitBudget = %v, want 100s (KTD13)", DefaultWaitBudget)
	}
	// The plan's bound: exit 4 within 110 seconds. The last backoff wait
	// is cut at the deadline, so the budget plus one attempt bounds it.
	if DefaultWaitBudget+5*time.Second > 110*time.Second {
		t.Fatal("the wait budget leaves no room under 110 seconds")
	}
	if WaitBudget != DefaultWaitBudget {
		t.Fatalf("WaitBudget = %v at start", WaitBudget)
	}
	t.Setenv(WaitBudgetEnv, "2s")
	if got := effectiveBudget(); got != 2*time.Second {
		t.Fatalf("LOOP_WAIT_BUDGET=2s gives %v", got)
	}
	t.Setenv(WaitBudgetEnv, "1h")
	if got := effectiveBudget(); got != DefaultWaitBudget {
		t.Fatalf("LOOP_WAIT_BUDGET cannot raise the budget, got %v", got)
	}
}

func TestBackoffCapsAtTwoSeconds(t *testing.T) {
	var b backoff
	var last time.Duration
	for range 20 {
		last = b.wait()
	}
	if last != 2*time.Second {
		t.Fatalf("backoff = %v", last)
	}
}

func TestCoordinatorRequestIDIsKept(t *testing.T) {
	t.Setenv(RequestIDEnv, "coord-req-7")
	env, _ := runJSON(t, New(), "version")
	if env["request_id"] != "coord-req-7" {
		t.Fatalf("request_id = %v", env["request_id"])
	}
	t.Setenv(RequestIDEnv, "bad id with spaces")
	env, _ = runJSON(t, New(), "version")
	if env["request_id"] == "bad id with spaces" {
		t.Fatal("an invalid request id must be replaced")
	}
}

func TestCapabilityMatrixDerivesFromCallers(t *testing.T) {
	r := Surface()
	m, ok := r.CapabilityMatrix()
	if !ok {
		t.Fatal("Surface sets the matrix")
	}
	cases := map[string][]string{
		"status":        PrincipalKinds,
		"work peek":     {PrincipalRun, PrincipalCoordinator},
		"inbox answer":  {PrincipalCoordinator},
		"room apply":    {PrincipalRoom},
		"secret set":    {PrincipalHost},
		"upgrade":       {PrincipalCoordinator, PrincipalHost},
		"install":       {PrincipalHost},
		"review submit": {PrincipalRun},
	}
	for path, want := range cases {
		got := m.Verbs[path]
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
	for _, path := range []string{"work block", "work show", "review submit"} {
		if !m.IsItemScoped(path) {
			t.Errorf("%s is item scoped", path)
		}
	}
	for _, path := range []string{"install", "upgrade", "rollback", "restore", "drain", "project add", "project list",
		"project show", "project remove", "secret set", "secret import"} {
		if !m.IsHostVerb(path) {
			t.Errorf("%s is a host verb", path)
		}
	}
	if m.IsHostVerb("secret check") {
		t.Error("secret check is a run verb")
	}
	// factorio-obc: a verb the coordinator calls as a host caller stays a
	// host verb; a verb for the coordinator and the host that reads the
	// instance's state is none.
	if m.IsHostVerb("merge status") {
		t.Error("merge status is a coordinator verb, not a host verb")
	}
	env, _ := runJSON(t, r, "schema")
	if env["data"].(map[string]any)["capabilities"].(map[string]any)["verbs"] == nil {
		t.Fatal("the schema prints the matrix")
	}
	if err := r.RegisterVerb(Verb{Noun: "work", Name: "fly", Summary: "s", Side: SideClient, Callers: []string{"pilot"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveMatrix(r); err == nil {
		t.Fatal("an unknown caller label must be refused")
	}
}

func TestWriteVerbsAreMarked(t *testing.T) {
	r := New()
	for _, path := range []string{"work note", "branch push", "run finish", "secret set", "project add", "drain"} {
		if v, _ := r.Verb(path); !v.Write {
			t.Errorf("%s is a write", path)
		}
	}
	for _, path := range []string{"work show", "status", "project list", "inbox wait"} {
		if v, _ := r.Verb(path); v.Write {
			t.Errorf("%s is not a write", path)
		}
	}
}

func TestSurfaceFixes(t *testing.T) {
	r := New()
	fin, _ := r.Verb("run finish")
	if strings.Join(fin.Flags[0].Enum, ",") != "done,failed,blocked" {
		t.Fatalf("run finish --outcome enum = %v", fin.Flags[0].Enum)
	}
	for _, v := range r.Verbs() {
		text := v.Summary
		for _, f := range v.Flags {
			text += " " + f.Description
		}
		for _, mangled := range []string{"runOrCoordinator", "runOrRoom", "hostOnly", "coordinator)"} {
			if strings.Contains(text, mangled) {
				t.Errorf("%s holds the mangled text %q: %s", v.Path, mangled, text)
			}
		}
	}
}

func TestValidateChecksWireCalls(t *testing.T) {
	r := New()
	if _, res := r.Validate("inbox defer", []string{"E-1"}, map[string]string{"until": "2026-10-21T10:00:00+02:00"}, "r"); res == nil {
		t.Fatal("a non-canonical time must be refused")
	}
	inv, res := r.Validate("inbox defer", []string{"E-1"}, map[string]string{"until": "2026-10-21T08:00:00Z", "reason": "the deploy", "@content_hash": "h"}, "r")
	if res != nil || inv.Extra["@content_hash"] != "h" || len(inv.Flags) != 2 {
		t.Fatalf("Validate = %+v %v", inv, res)
	}
	if _, res := r.Validate("work show", nil, nil, "r"); res == nil || res.Refusal.Rule != "missing_argument" {
		t.Fatalf("missing argument: %v", res)
	}
	if _, res := r.Validate("run finish", nil, map[string]string{"outcome": "maybe"}, "r"); res == nil || res.Refusal.Rule != "bad_flag_value" {
		t.Fatalf("bad enum: %v", res)
	}
	if _, res := r.Validate("work show", []string{"T-1"}, map[string]string{"json": "true"}, "r"); res == nil {
		t.Fatal("a global flag never travels on the wire")
	}
}
