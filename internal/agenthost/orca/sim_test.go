package orca_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/orca"
)

// The fixtures in testdata keep the JSON shapes of orca 1.4.232 output.
// terminal_list*.json, terminal_show_*.json, terminal_wait_timeout.json and
// account_list.json came from read-only commands on 2026-10-21. probe_*.json
// came from the operator-approved restart measurement
// (docs/evidence/orca-restart.md). Every path, branch, id, usage figure and
// time in them is synthetic. Ids stay consistent across files, so the probe
// before and after share a handle and incarnation while the runtime id differs.
// The shapes of terminal create, terminal close, terminal send and
// worktree create that the sim builds are transcribed from what alpha's
// agent-start.ts, finished-probe.ts, supervisor-wake.ts and
// worktree-names.ts read.

func fixture(t testing.TB, name string) orca.Output {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	code := 0
	if !env.OK {
		code = 1
	}
	return orca.Output{Code: code, Stdout: b}
}

func okOut(result any, runtime string) orca.Output {
	b, _ := json.Marshal(map[string]any{"id": "x", "ok": true, "result": result, "_meta": map[string]any{"runtimeId": runtime}})
	return orca.Output{Stdout: b}
}

func failOut(code string, result any) orca.Output {
	env := map[string]any{"id": "x", "ok": false, "error": map[string]any{"code": code, "message": code}}
	if result != nil {
		env["result"] = result
	}
	b, _ := json.Marshal(env)
	return orca.Output{Code: 1, Stdout: b}
}

// sim is a scripted orca with one terminal, term_A. It records every call.
type sim struct {
	mu        sync.Mutex
	calls     [][]string
	runtime   string
	checkout  string
	agent     string // the agentIdentity orca shows once ready
	ready     bool
	created   bool
	closed    bool
	exited    *int
	sendReply func() orca.Output
	override  map[string]func(args []string) (orca.Output, error)
}

const (
	handleA = "term_A"
	incA    = "inc-A"
)

var idA = agenthost.RunID{Host: "orca:local", Terminal: handleA, Incarnation: incA}

func (s *sim) row() map[string]any {
	r := map[string]any{
		"handle": handleA, "ptyId": "pty-A", "incarnationId": incA, "orphaned": false,
		"worktreePath": s.checkout, "connected": true, "writable": true, "executionHostId": "local",
	}
	if s.ready {
		r["agentIdentity"] = s.agent
	}
	return r
}

func (s *sim) Run(_ context.Context, args []string) (orca.Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, slices.Clone(args))
	key := strings.Join(args[:2], " ")
	if f, ok := s.override[key]; ok {
		return f(args)
	}
	alive := s.created && !s.closed
	switch key {
	case "terminal create":
		s.created = true
		return okOut(map[string]any{"terminal": map[string]any{
			"handle": handleA, "ptyId": "pty-A", "incarnationId": incA, "executionHostId": "local",
		}}, s.runtime), nil
	case "terminal show":
		if !alive || flag(args, "--terminal") != handleA {
			return failOut(orca.CodeStale, nil), nil
		}
		return okOut(map[string]any{"terminal": s.row()}, s.runtime), nil
	case "terminal wait":
		if !alive || flag(args, "--terminal") != handleA {
			return failOut(orca.CodeStale, nil), nil
		}
		if s.exited != nil {
			return okOut(map[string]any{"terminal": map[string]any{"handle": handleA, "exitCode": *s.exited}}, s.runtime), nil
		}
		return failOut(orca.CodeTimeout, nil), nil
	case "terminal close":
		if !alive {
			return failOut(orca.CodeStale, nil), nil
		}
		s.closed = true
		return okOut(map[string]any{"closed": true}, s.runtime), nil
	case "terminal list":
		rows := []any{}
		if alive && s.exited == nil {
			rows = append(rows, s.row())
		}
		return okOut(map[string]any{
			"terminals": rows, "totalCount": len(rows), "truncated": false,
			"hostScope": map[string]any{"hostIds": []string{"local"}, "omittedHostIds": []string{}},
		}, s.runtime), nil
	case "terminal send":
		if s.sendReply != nil {
			return s.sendReply(), nil
		}
		return okOut(map[string]any{"send": map[string]any{"accepted": true, "bytesWritten": 10}}, s.runtime), nil
	}
	return failOut("unscripted", nil), nil
}

func (s *sim) verbs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		out = append(out, strings.Join(c[:2], " "))
	}
	return out
}

func (s *sim) allArgs() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func flag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// world is one orca host over a sim, with a fake clock that Sleep moves.
type world struct {
	t        *testing.T
	sim      *sim
	host     *orca.Host
	now      time.Time
	root     string
	checkout string
	claude   string
	codex    string
}

func newWorld(t *testing.T, mods ...func(*orca.Config)) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{t: t, now: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC), root: root}
	w.checkout = filepath.Join(root, "work")
	if err := os.MkdirAll(w.checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	w.claude, w.codex = t.TempDir(), t.TempDir()
	w.sim = &sim{runtime: "rt-1", checkout: w.checkout, agent: "claude", ready: true, override: map[string]func([]string) (orca.Output, error){}}
	cfg := orca.Config{
		Runner: w.sim, CheckoutRoot: root, ClaudeRoot: w.claude, CodexRoot: w.codex,
		ReadyWindow: 10 * time.Second, ReceiptWindow: 30 * time.Second, PollInterval: time.Second,
		Now:   func() time.Time { return w.now },
		Sleep: func(context.Context, time.Duration) error { w.now = w.now.Add(time.Second); return nil },
	}
	for _, m := range mods {
		m(&cfg)
	}
	h, err := orca.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.host = h
	return w
}

func (w *world) prompt(text string) string {
	w.t.Helper()
	p := filepath.Join(w.t.TempDir(), "prompt.txt")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) spec(h agenthost.Harness) agenthost.StartSpec {
	return agenthost.StartSpec{
		Kind: agenthost.KindStage, Checkout: w.checkout, Harness: h, Model: "claude-sonnet-4-5-20250929",
		Effort: "high", PromptFile: w.prompt("hello"),
	}
}

func (w *world) start(h agenthost.Harness) agenthost.Session {
	w.t.Helper()
	w.sim.agent = string(h)
	s, err := w.host.Start(context.Background(), w.spec(h))
	if err != nil {
		w.t.Fatalf("Start: %v", err)
	}
	return s
}

// claudeTranscript writes records to the session's Claude transcript.
func (w *world) claudeTranscript(name string, records ...map[string]any) {
	w.t.Helper()
	dir := filepath.Join(w.claude, orca.TranscriptName(w.checkout))
	writeJSONL(w.t, filepath.Join(dir, name), records)
}

func writeJSONL(t *testing.T, path string, records []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range records {
		line, _ := json.Marshal(r)
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
