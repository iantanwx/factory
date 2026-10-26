package orca_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/orca"
)

// ---- Start ----

func TestStartPromptNeverInArgs(t *testing.T) {
	for _, hs := range []agenthost.Harness{agenthost.Claude, agenthost.Codex} {
		t.Run(string(hs), func(t *testing.T) {
			w := newWorld(t)
			w.sim.agent = string(hs)
			spec := w.spec(hs)
			marker := "SECRET-PROMPT-MARKER"
			spec.PromptFile = w.prompt("It's a \"prompt\" with `ticks`, $HOME and\n" + marker + "\n# comment")
			if _, err := w.host.Start(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			var launch []string
			for _, args := range w.sim.allArgs() {
				for _, a := range args {
					if strings.Contains(a, marker) {
						t.Fatalf("prompt text reached orca argv: %q", args)
					}
				}
				if strings.Join(args[:2], " ") == "agent launch" {
					launch = args
				}
			}
			for f, want := range map[string]string{
				"--agent": string(hs), "--worktree": "path:" + w.checkout, "--model": "claude-sonnet-4-5-20250929",
				"--effort": "high", "--surface": "terminal", "--prompt": agenthost.PromptInstruction(spec.PromptFile),
			} {
				if got := flag(launch, f); got != want {
					t.Fatalf("launch %s = %q, want %q: %q", f, got, want, launch)
				}
			}
		})
	}
}

// A launch that started a chat, or that did not hand the prompt over,
// starts no run the loop can drive.
func TestStartRefusesALaunchItCannotDrive(t *testing.T) {
	for name, result := range map[string]map[string]any{
		"chat": {"outcome": map[string]any{"kind": "structured", "handle": handleA},
			"prompt": map[string]any{"outcome": "handed-to-terminal"}},
		"prompt lost": {"outcome": map[string]any{"kind": "terminal", "handle": handleA},
			"prompt": map[string]any{"outcome": "failed"}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.sim.override["agent launch"] = func([]string) (orca.Output, error) {
				w.sim.created = true
				return okOut(result, w.sim.runtime), nil
			}
			if _, err := w.host.Start(context.Background(), w.spec(agenthost.Claude)); err == nil {
				t.Fatal("Start accepted a launch it cannot drive")
			}
		})
	}
}

func TestLaunchArgsOmitAnEmptyEffort(t *testing.T) {
	spec := agenthost.StartSpec{Kind: agenthost.KindRoom, Checkout: "/r/c", Harness: agenthost.Claude, Model: "m",
		PromptFile: "/tmp/it's here.txt"}
	args := orca.LaunchArgs(spec, "/r/c")
	if slices.Contains(args, "--effort") || flag(args, "--prompt") != agenthost.PromptInstruction("/tmp/it's here.txt") {
		t.Fatalf("launch args = %q", args)
	}
}

func TestStartIdentityAndEpoch(t *testing.T) {
	w := newWorld(t)
	s := w.start(agenthost.Claude)
	if s.ID != idA || s.Epoch != "rt-1" || s.Pty != "pty-A" || !s.StartedAt.Equal(time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("session = %+v", s)
	}
	if got := s.ID.String(); got != "orca:local/term_A/inc-A" {
		t.Fatalf("lease owner = %q", got)
	}
	if slices.Contains(w.sim.verbs(), "worktree create") {
		t.Fatal("Start made a checkout")
	}
}

func TestStartNotReady(t *testing.T) {
	w := newWorld(t)
	w.sim.ready = false
	spec := w.spec(agenthost.Claude)
	s, err := w.host.Start(context.Background(), spec)
	if !errors.Is(err, agenthost.ErrNotReady) {
		t.Fatalf("Start = %v; want ErrNotReady", err)
	}
	if s.ID != idA {
		t.Fatalf("not-ready start lost the identity: %+v", s)
	}
	if !slices.Contains(w.sim.verbs(), "terminal close") {
		t.Fatal("a not-ready terminal was not closed")
	}
}

func TestStartWrongAgentIsNotReady(t *testing.T) {
	w := newWorld(t)
	w.sim.agent = "codex"
	spec := w.spec(agenthost.Claude)
	if _, err := w.host.Start(context.Background(), spec); !errors.Is(err, agenthost.ErrNotReady) {
		t.Fatalf("Start = %v; want ErrNotReady", err)
	}
}

func TestStartRefusesOutsideRootWithoutCallingOrca(t *testing.T) {
	w := newWorld(t)
	spec := w.spec(agenthost.Claude)
	spec.Checkout = filepath.Join(w.root, "..", "elsewhere")
	if _, err := w.host.Start(context.Background(), spec); !errors.Is(err, agenthost.ErrOutsideRoot) {
		t.Fatalf("Start = %v; want ErrOutsideRoot", err)
	}
	if len(w.sim.allArgs()) != 0 {
		t.Fatal("an outside-root start reached orca")
	}
}

func assistant(dir, model string, at time.Time) map[string]any {
	return map[string]any{"type": "assistant", "cwd": dir, "timestamp": ts(at),
		"message": map[string]any{"model": model, "content": []any{map[string]any{"type": "text"}}}}
}

func TestDroppedModelUnverifiedAndEscalatedDoesNotStart(t *testing.T) {
	w := newWorld(t)
	// Orca drops the model argument: the harness runs its default model.
	w.claudeTranscript("s1.jsonl", assistant(w.checkout, "claude-default-model", w.now.Add(time.Second)))
	s := w.start(agenthost.Claude)
	rc, err := w.host.ModelReceipt(context.Background(), s)
	if err != nil || rc.Status != agenthost.Unverified || rc.Observed != "claude-default-model" {
		t.Fatalf("receipt = %+v, %v; want unverified", rc, err)
	}
	w.sim.closed, w.sim.created = false, false
	spec := w.spec(agenthost.Claude)
	spec.RequireVerifiedModel = true
	if _, err := w.host.Start(context.Background(), spec); !errors.Is(err, agenthost.ErrModelUnverified) {
		t.Fatalf("escalated Start = %v; want ErrModelUnverified", err)
	}
	if !w.sim.closed {
		t.Fatal("an escalated role with an unverified model was left running")
	}
}

func TestEscalatedWithNoRecordTimesOutUnverified(t *testing.T) {
	w := newWorld(t)
	spec := w.spec(agenthost.Claude)
	spec.RequireVerifiedModel = true
	if _, err := w.host.Start(context.Background(), spec); !errors.Is(err, agenthost.ErrModelUnverified) {
		t.Fatalf("Start = %v; want ErrModelUnverified", err)
	}
}

func TestReceiptVerified(t *testing.T) {
	w := newWorld(t)
	w.claudeTranscript("s1.jsonl",
		assistant(w.checkout, "<synthetic>", w.now.Add(time.Second)),
		assistant(w.checkout, "claude-sonnet-4-5-20250929", w.now.Add(2*time.Second)))
	spec := w.spec(agenthost.Claude)
	spec.RequireVerifiedModel = true
	s, err := w.host.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("escalated Start with the right model = %v", err)
	}
	rc, _ := w.host.ModelReceipt(context.Background(), s)
	if rc.Status != agenthost.Verified {
		t.Fatalf("receipt = %+v", rc)
	}
}

func TestReceiptIgnoresTranscriptBeforeStart(t *testing.T) {
	w := newWorld(t)
	w.claudeTranscript("old.jsonl", assistant(w.checkout, "claude-sonnet-4-5-20250929", w.now.Add(-2*time.Hour)))
	s := w.start(agenthost.Claude)
	rc, _ := w.host.ModelReceipt(context.Background(), s)
	if rc.Status != agenthost.Unverified {
		t.Fatalf("an older session's transcript verified this one: %+v", rc)
	}
}

func TestCodexReceipt(t *testing.T) {
	w := newWorld(t)
	s := w.start(agenthost.Codex)
	s.Model = "gpt-5-codex"
	writeJSONL(t, filepath.Join(w.codex, w.now.Format("2006/01/02"), "rollout-1.jsonl"), []map[string]any{
		{"type": "session_meta", "timestamp": ts(w.now), "payload": map[string]any{"id": "sess", "cwd": w.checkout}},
		{"type": "turn_context", "timestamp": ts(w.now), "payload": map[string]any{"cwd": w.checkout, "model": "gpt-5-codex"}},
	})
	rc, err := w.host.ModelReceipt(context.Background(), s)
	if err != nil || rc.Status != agenthost.Verified {
		t.Fatalf("codex receipt = %+v, %v", rc, err)
	}
}

// ---- Inventory ----

func TestInventoryCapturedListIsComplete(t *testing.T) {
	w := newWorld(t)
	w.sim.override["terminal list"] = func([]string) (orca.Output, error) { return fixture(t, "terminal_list.json"), nil }
	inv, err := w.host.Inventory(context.Background())
	if err != nil || !inv.Complete || len(inv.Rows) != 2 || inv.Epoch != "00000001-0000-4000-8000-000000000001" {
		t.Fatalf("Inventory = %+v, %v", inv, err)
	}
	r := inv.Rows[0]
	if r.ID.String() != "orca:local/term_0000000c-0000-4000-8000-00000000000c/0000000e-0000-4000-8000-00000000000e" || r.Agent != "claude" {
		t.Fatalf("row = %+v", r)
	}
	gone := agenthost.Session{ID: agenthost.RunID{Host: "orca:local", Terminal: "term_gone", Incarnation: "inc_gone"}, Epoch: inv.Epoch}
	if got := inv.Lookup(gone); got != agenthost.Absent {
		t.Fatalf("Lookup of an unlisted session on a complete inventory = %s", got)
	}
}

func TestIncompleteInventoryNeverAbsent(t *testing.T) {
	base := func() map[string]any {
		var env map[string]any
		b, _ := os.ReadFile(filepath.Join("testdata", "terminal_list.json"))
		_ = json.Unmarshal(b, &env)
		return env
	}
	mutate := func(f func(res map[string]any)) func(t *testing.T) (orca.Output, error) {
		return func(*testing.T) (orca.Output, error) {
			env := base()
			f(env["result"].(map[string]any))
			b, _ := json.Marshal(env)
			return orca.Output{Stdout: b}, nil
		}
	}
	cases := map[string]func(t *testing.T) (orca.Output, error){
		"truncated": func(t *testing.T) (orca.Output, error) { return fixture(t, "terminal_list_truncated.json"), nil },
		"omitted host": mutate(func(r map[string]any) {
			r["hostScope"] = map[string]any{"hostIds": []any{"local"}, "omittedHostIds": []any{"ssh:x"}}
		}),
		"truncated flag alone": mutate(func(r map[string]any) { r["truncated"] = true }),
		"no host scope":        mutate(func(r map[string]any) { delete(r, "hostScope") }),
		"count differs":        mutate(func(r map[string]any) { r["totalCount"] = 3 }),
		"no total":             mutate(func(r map[string]any) { delete(r, "totalCount") }),
		"no incarnation": mutate(func(r map[string]any) {
			r["terminals"].([]any)[0].(map[string]any)["incarnationId"] = ""
		}),
		"repeated handle": mutate(func(r map[string]any) {
			ts := r["terminals"].([]any)
			ts[1].(map[string]any)["handle"] = ts[0].(map[string]any)["handle"]
		}),
		"refused":        func(*testing.T) (orca.Output, error) { return failOut("runtime_unavailable", nil), nil },
		"malformed JSON": func(*testing.T) (orca.Output, error) { return orca.Output{Stdout: []byte("{not json")}, nil },
		"empty stdout":   func(*testing.T) (orca.Output, error) { return orca.Output{Code: 2}, nil },
		"missing CLI": func(*testing.T) (orca.Output, error) {
			return orca.Output{}, &os.PathError{Op: "exec", Path: "orca", Err: os.ErrNotExist}
		},
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.sim.override["terminal list"] = func([]string) (orca.Output, error) { return out(t) }
			inv, _ := w.host.Inventory(context.Background())
			if inv.Complete {
				t.Fatalf("inventory reads complete: %+v", inv)
			}
			gone := agenthost.Session{ID: agenthost.RunID{Host: "orca:local", Terminal: "term_gone", Incarnation: "inc_gone"},
				Epoch: "00000001-0000-4000-8000-000000000001"}
			if got := inv.Lookup(gone); got == agenthost.Absent {
				t.Fatal("an incomplete inventory reported a run absent")
			}
		})
	}
}

// The restart probe: the same handle, PTY and incarnation answered after
// orca-serve restarted under a new runtime id.
func TestRestartProbeReadopts(t *testing.T) {
	w := newWorld(t)
	w.sim.override["terminal create"] = func([]string) (orca.Output, error) { return fixture(t, "probe_create.json"), nil }
	w.sim.override["terminal show"] = func([]string) (orca.Output, error) { return fixture(t, "probe_show_before.json"), nil }
	w.sim.override["terminal wait"] = func([]string) (orca.Output, error) { return fixture(t, "terminal_wait_timeout.json"), nil }
	w.sim.agent = ""
	probeDir := "/home/operator/orca/workspaces/example/probe"
	w2 := newWorld(t, func(c *orca.Config) { c.CheckoutRoot = filepath.Dir(probeDir); c.Runner = w.sim })
	// The probe ran a heartbeat, not an agent, so read the identity from
	// the create receipt rather than through Start's readiness wait.
	var created struct {
		Result struct {
			Terminal struct{ Handle, IncarnationID, PtyID string } `json:"terminal"`
		} `json:"result"`
		Meta struct{ RuntimeID string } `json:"_meta"`
	}
	b, _ := os.ReadFile(filepath.Join("testdata", "probe_create.json"))
	if err := json.Unmarshal(b, &created); err != nil {
		t.Fatal(err)
	}
	s := agenthost.Session{
		ID:    agenthost.RunID{Host: "orca:local", Terminal: created.Result.Terminal.Handle, Incarnation: created.Result.Terminal.IncarnationID},
		Epoch: created.Meta.RuntimeID, Pty: created.Result.Terminal.PtyID, Checkout: probeDir,
	}
	w.sim.override["terminal show"] = func([]string) (orca.Output, error) { return fixture(t, "probe_show_after.json"), nil }
	st, err := w2.host.Settle(context.Background(), s.ID)
	if err != nil || st.Liveness != agenthost.Live {
		t.Fatalf("Settle after the orca-serve restart = %+v, %v; want live", st, err)
	}
	var after struct {
		Result struct{ Terminal map[string]any } `json:"result"`
		Meta   struct{ RuntimeID string }        `json:"_meta"`
	}
	b, _ = os.ReadFile(filepath.Join("testdata", "probe_show_after.json"))
	_ = json.Unmarshal(b, &after)
	w.sim.override["terminal list"] = func([]string) (orca.Output, error) {
		return okOut(map[string]any{
			"terminals": []any{after.Result.Terminal}, "totalCount": 1, "truncated": false,
			"hostScope": map[string]any{"hostIds": []string{"local"}, "omittedHostIds": []string{}},
		}, after.Meta.RuntimeID), nil
	}
	inv, err := w2.host.Inventory(context.Background())
	if err != nil || !inv.Complete || inv.Epoch == s.Epoch {
		t.Fatalf("Inventory = %+v, %v", inv, err)
	}
	if got := inv.Lookup(s); got != agenthost.Present {
		t.Fatalf("Lookup across the restart = %s; want present", got)
	}
	moved, ok := inv.Readopted(s)
	if !ok || moved.Epoch != "00000009-0000-4000-8000-000000000009" {
		t.Fatalf("Readopted = %+v, %v", moved, ok)
	}
}

func TestEpochChangeAloneNeverAbsent(t *testing.T) {
	inv := agenthost.Inventory{Complete: true, Epoch: "rt-2"}
	s := agenthost.Session{ID: idA, Epoch: "rt-1"}
	if got := inv.Lookup(s); got != agenthost.PresenceUnknown {
		t.Fatalf("Lookup across epochs with no re-read = %s; want unknown", got)
	}
	inv.Rows = []agenthost.Row{{ID: agenthost.RunID{Host: "orca:local", Terminal: "term_B", Incarnation: incA}}}
	s.Epoch = "rt-2"
	if got := inv.Lookup(s); got != agenthost.PresenceUnknown {
		t.Fatalf("Lookup with the incarnation under another handle = %s; want unknown", got)
	}
	inv.Rows = []agenthost.Row{{ID: agenthost.RunID{Host: "orca:local", Terminal: "term_B", Incarnation: "inc-B"}, Pty: "pty-A"}}
	s.Pty = "pty-A"
	if got := inv.Lookup(s); got != agenthost.PresenceUnknown {
		t.Fatalf("Lookup with the PTY under another handle = %s; want unknown", got)
	}
}

// ---- Settle and Stop ----

func TestSettle(t *testing.T) {
	ctx := context.Background()
	t.Run("live", func(t *testing.T) {
		w := newWorld(t)
		w.start(agenthost.Claude)
		st, err := w.host.Settle(ctx, idA)
		if err != nil || st.Liveness != agenthost.Live {
			t.Fatalf("Settle = %+v, %v", st, err)
		}
	})
	t.Run("captured live terminal", func(t *testing.T) {
		w := newWorld(t)
		w.sim.override["terminal show"] = func([]string) (orca.Output, error) { return fixture(t, "terminal_show_agent.json"), nil }
		w.sim.override["terminal wait"] = func([]string) (orca.Output, error) { return fixture(t, "terminal_wait_timeout.json"), nil }
		id := agenthost.RunID{Host: "orca:local", Terminal: "term_0000000c-0000-4000-8000-00000000000c", Incarnation: "0000000e-0000-4000-8000-00000000000e"}
		st, err := w.host.Settle(ctx, id)
		if err != nil || st.Liveness != agenthost.Live {
			t.Fatalf("Settle = %+v, %v", st, err)
		}
	})
	t.Run("exited", func(t *testing.T) {
		w := newWorld(t)
		w.start(agenthost.Claude)
		code := 0
		w.sim.exited = &code
		st, err := w.host.Settle(ctx, idA)
		if err != nil || st.Liveness != agenthost.Exited || st.ExitCode == nil || *st.ExitCode != 0 {
			t.Fatalf("Settle = %+v, %v", st, err)
		}
	})
	unknownCases := map[string]struct {
		mod  func(w *world)
		want error
	}{
		"captured stale handle": {func(w *world) {
			w.sim.override["terminal show"] = func([]string) (orca.Output, error) { return fixture(t, "terminal_show_stale.json"), nil }
		}, agenthost.ErrStaleHandle},
		"closed terminal": {func(w *world) { w.sim.closed = true }, agenthost.ErrStaleHandle},
		"another incarnation": {func(w *world) {
			w.sim.override["terminal show"] = func([]string) (orca.Output, error) {
				r := w.sim.row()
				r["incarnationId"] = "inc-other"
				return okOut(map[string]any{"terminal": r}, "rt-1"), nil
			}
		}, agenthost.ErrStaleHandle},
		"missing CLI": {func(w *world) {
			w.sim.override["terminal show"] = func([]string) (orca.Output, error) { return orca.Output{}, os.ErrNotExist }
		}, agenthost.ErrHostUnavailable},
		"non-zero exit without JSON": {func(w *world) {
			w.sim.override["terminal show"] = func([]string) (orca.Output, error) {
				return orca.Output{Code: 1, Stderr: []byte("boom")}, nil
			}
		}, agenthost.ErrHostUnavailable},
		"malformed JSON": {func(w *world) {
			w.sim.override["terminal show"] = func([]string) (orca.Output, error) { return orca.Output{Stdout: []byte(`{"ok":tru`)}, nil }
		}, agenthost.ErrHostUnavailable},
		"ok with non-zero exit": {func(w *world) {
			w.sim.override["terminal show"] = func([]string) (orca.Output, error) {
				o := okOut(map[string]any{"terminal": w.sim.row()}, "rt-1")
				o.Code = 3
				return o, nil
			}
		}, agenthost.ErrHostUnavailable},
		"wait refused": {func(w *world) {
			w.sim.override["terminal wait"] = func([]string) (orca.Output, error) { return failOut("runtime_unavailable", nil), nil }
		}, agenthost.ErrHostUnavailable},
		"disconnected": {func(w *world) {
			w.sim.override["terminal show"] = func([]string) (orca.Output, error) {
				r := w.sim.row()
				r["connected"] = false
				return okOut(map[string]any{"terminal": r}, "rt-1"), nil
			}
		}, agenthost.ErrHostUnavailable},
	}
	for name, c := range unknownCases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.start(agenthost.Claude)
			c.mod(w)
			st, err := w.host.Settle(ctx, idA)
			if st.Liveness != agenthost.Unknown || !errors.Is(err, c.want) {
				t.Fatalf("Settle = %+v, %v; want unknown, %v", st, err, c.want)
			}
		})
	}
}

func TestStop(t *testing.T) {
	ctx := context.Background()
	t.Run("live session closes and reads exited", func(t *testing.T) {
		w := newWorld(t)
		w.start(agenthost.Claude)
		st, err := w.host.Stop(ctx, idA)
		if err != nil || st.Liveness != agenthost.Exited || !w.sim.closed {
			t.Fatalf("Stop = %+v, %v", st, err)
		}
	})
	t.Run("stale handle is never closed", func(t *testing.T) {
		w := newWorld(t)
		w.start(agenthost.Claude)
		other := idA
		other.Incarnation = "inc-other"
		st, err := w.host.Stop(ctx, other)
		if st.Liveness == agenthost.Exited || !errors.Is(err, agenthost.ErrExitUnproven) || w.sim.closed {
			t.Fatalf("Stop = %+v, %v, closed=%v", st, err, w.sim.closed)
		}
	})
	t.Run("refused close is unproven", func(t *testing.T) {
		w := newWorld(t)
		w.start(agenthost.Claude)
		w.sim.override["terminal close"] = func([]string) (orca.Output, error) { return failOut("terminal_close_failed", nil), nil }
		st, err := w.host.Stop(ctx, idA)
		if st.Liveness != agenthost.Unknown || !errors.Is(err, agenthost.ErrExitUnproven) {
			t.Fatalf("Stop = %+v, %v", st, err)
		}
	})
	t.Run("still listed after close is unproven", func(t *testing.T) {
		w := newWorld(t)
		w.start(agenthost.Claude)
		w.sim.override["terminal close"] = func([]string) (orca.Output, error) { return okOut(map[string]any{}, "rt-1"), nil }
		st, err := w.host.Stop(ctx, idA)
		if st.Liveness != agenthost.Unknown || !errors.Is(err, agenthost.ErrExitUnproven) {
			t.Fatalf("Stop = %+v, %v", st, err)
		}
	})
}

// ---- Ended (KTD8) ----

func turnEnd(dir string, at time.Time) map[string]any {
	return map[string]any{"type": "system", "subtype": "turn_duration", "cwd": dir, "timestamp": ts(at)}
}

func TestEndedClaude(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		records func(dir string) []map[string]any
		after   time.Duration
		ended   bool
		reason  string
	}{
		{"ended turn after the quiet period", func(d string) []map[string]any {
			return []map[string]any{assistant(d, "m", t0.Add(time.Second)), turnEnd(d, t0.Add(2*time.Second))}
		}, 31 * time.Minute, true, ""},
		{"ended turn inside the quiet period", func(d string) []map[string]any {
			return []map[string]any{assistant(d, "m", t0.Add(time.Second)), turnEnd(d, t0.Add(2*time.Second))}
		}, 29 * time.Minute, false, "quiet period"},
		{"tool call still running", func(d string) []map[string]any {
			return []map[string]any{assistant(d, "m", t0.Add(time.Second))}
		}, 3 * time.Hour, false, "has not ended"},
		{"background command without notification", func(d string) []map[string]any {
			return []map[string]any{
				{"type": "user", "cwd": d, "timestamp": ts(t0.Add(time.Second)), "toolUseResult": map[string]any{"backgroundTaskId": "bg1"}},
				turnEnd(d, t0.Add(2*time.Second)),
			}
		}, 3 * time.Hour, false, "bg1"},
		{"background command notified", func(d string) []map[string]any {
			return []map[string]any{
				{"type": "user", "cwd": d, "timestamp": ts(t0.Add(time.Second)), "toolUseResult": map[string]any{"backgroundTaskId": "bg1"}},
				{"type": "queue-operation", "operation": "enqueue", "timestamp": ts(t0.Add(2 * time.Second)), "content": "<task-id>bg1</task-id><status>completed</status>"},
				turnEnd(d, t0.Add(3*time.Second)),
			}
		}, 31 * time.Minute, true, ""},
		{"background command stopped", func(d string) []map[string]any {
			return []map[string]any{
				{"type": "user", "cwd": d, "timestamp": ts(t0.Add(time.Second)), "toolUseResult": map[string]any{"backgroundTaskId": "bg1"}},
				{"type": "user", "cwd": d, "timestamp": ts(t0.Add(2 * time.Second)), "toolUseResult": map[string]any{"task_id": "bg1", "message": "Successfully stopped task bg1"}},
				turnEnd(d, t0.Add(3*time.Second)),
			}
		}, 31 * time.Minute, true, ""},
		{"pending background agents", func(d string) []map[string]any {
			e := turnEnd(d, t0.Add(time.Second))
			e["pendingBackgroundAgentCount"] = 2
			return []map[string]any{e}
		}, 3 * time.Hour, false, "background agent"},
		{"api error", func(d string) []map[string]any {
			a := assistant(d, "m", t0.Add(time.Second))
			a["isApiErrorMessage"] = true
			return []map[string]any{a, turnEnd(d, t0.Add(2*time.Second))}
		}, 3 * time.Hour, false, "API error"},
		{"usage limit resume queued", func(d string) []map[string]any {
			return []map[string]any{
				{"type": "system", "subtype": "informational", "cwd": d, "timestamp": ts(t0.Add(time.Second)), "content": "Limit hit, continuing automatically later"},
				turnEnd(d, t0.Add(2*time.Second)),
			}
		}, 3 * time.Hour, false, "usage limit"},
		{"persistent monitor", func(d string) []map[string]any {
			return []map[string]any{
				{"type": "user", "cwd": d, "timestamp": ts(t0.Add(time.Second)), "toolUseResult": map[string]any{"taskId": "m1", "timeoutMs": 1000, "persistent": true}},
				turnEnd(d, t0.Add(2*time.Second)),
			}
		}, 3 * time.Hour, false, "persistent monitor"},
		{"wake moves the end", func(d string) []map[string]any {
			return []map[string]any{
				{"type": "user", "cwd": d, "timestamp": ts(t0.Add(time.Second)), "toolUseResult": map[string]any{"scheduledFor": t0.Add(time.Hour).UnixMilli()}},
				turnEnd(d, t0.Add(2*time.Second)),
			}
		}, 50 * time.Minute, false, "wake"},
		{"killed notice and bookkeeping do not reopen the turn", func(d string) []map[string]any {
			return []map[string]any{
				turnEnd(d, t0.Add(time.Second)),
				{"type": "queue-operation", "operation": "enqueue", "timestamp": ts(t0.Add(10 * time.Minute)), "content": "<status>killed</status>"},
				{"type": "pr-link", "timestamp": ts(t0.Add(2 * time.Second))},
				{"type": "system", "subtype": "away_summary", "cwd": d, "timestamp": ts(t0.Add(3 * time.Second))},
			}
		}, 31 * time.Minute, true, ""},
		{"another directory", func(string) []map[string]any {
			return []map[string]any{turnEnd("/elsewhere", t0.Add(time.Second))}
		}, 3 * time.Hour, false, "another directory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			s := w.start(agenthost.Claude)
			w.claudeTranscript("s1.jsonl", c.records(w.checkout)...)
			w.now = t0.Add(c.after)
			r, err := w.host.Ended(ctx, s)
			if err != nil || r.Ended != c.ended {
				t.Fatalf("Ended = %+v, %v; want ended=%v", r, err, c.ended)
			}
			if c.ended && r.Cause != agenthost.EndQuiet {
				t.Fatalf("cause = %s", r.Cause)
			}
			if !c.ended && !strings.Contains(r.Reason, c.reason) {
				t.Fatalf("reason %q lacks %q", r.Reason, c.reason)
			}
		})
	}
}

func TestEndedChildWriteRestartsQuiet(t *testing.T) {
	w := newWorld(t)
	s := w.start(agenthost.Claude)
	t0 := w.now
	w.claudeTranscript("s1.jsonl", turnEnd(w.checkout, t0.Add(time.Second)))
	w.claudeTranscript("s1/subagents/agent-1.jsonl", assistant(w.checkout, "m", t0.Add(20*time.Minute)))
	w.now = t0.Add(31 * time.Minute)
	if r, _ := w.host.Ended(context.Background(), s); r.Ended {
		t.Fatalf("a child's write did not restart the quiet period: %+v", r)
	}
	w.now = t0.Add(51 * time.Minute)
	if r, _ := w.host.Ended(context.Background(), s); !r.Ended {
		t.Fatalf("Ended = %+v", r)
	}
}

func TestEndedProcessExit(t *testing.T) {
	w := newWorld(t)
	s := w.start(agenthost.Claude)
	code := 1
	w.sim.exited = &code
	r, err := w.host.Ended(context.Background(), s)
	if err != nil || !r.Ended || r.Cause != agenthost.EndExited {
		t.Fatalf("Ended = %+v, %v", r, err)
	}
}

func TestEndedUnknownLivenessNeverEnds(t *testing.T) {
	w := newWorld(t)
	s := w.start(agenthost.Claude)
	w.claudeTranscript("s1.jsonl", turnEnd(w.checkout, w.now.Add(time.Second)))
	w.now = w.now.Add(3 * time.Hour)
	w.sim.override["terminal show"] = func([]string) (orca.Output, error) { return fixture(t, "terminal_show_stale.json"), nil }
	r, err := w.host.Ended(context.Background(), s)
	if r.Ended || !errors.Is(err, agenthost.ErrStaleHandle) {
		t.Fatalf("Ended = %+v, %v", r, err)
	}
}

func TestEndedThenCloseKeepsCheckout(t *testing.T) {
	w := newWorld(t)
	s := w.start(agenthost.Claude)
	w.claudeTranscript("s1.jsonl", turnEnd(w.checkout, w.now.Add(time.Second)))
	w.now = w.now.Add(time.Hour)
	if r, _ := w.host.Ended(context.Background(), s); !r.Ended {
		t.Fatalf("Ended = %+v", r)
	}
	if err := w.host.Close(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	for _, v := range w.sim.verbs() {
		if strings.HasPrefix(v, "worktree") {
			t.Fatalf("Close called %s", v)
		}
	}
	if _, err := os.Stat(w.checkout); err != nil {
		t.Fatal(err)
	}
}

func TestEndedCodex(t *testing.T) {
	ctx := context.Background()
	rollout := func(w *world, records ...map[string]any) {
		head := map[string]any{"type": "session_meta", "timestamp": ts(w.now), "payload": map[string]any{"id": "sess", "cwd": w.checkout}}
		writeJSONL(t, filepath.Join(w.codex, w.now.Format("2006/01/02"), "rollout-1.jsonl"), append([]map[string]any{head}, records...))
	}
	ev := func(at time.Time, payload map[string]any) map[string]any {
		return map[string]any{"type": "event_msg", "timestamp": ts(at), "payload": payload}
	}
	t.Run("completed turn ends after quiet", func(t *testing.T) {
		w := newWorld(t)
		s := w.start(agenthost.Codex)
		rollout(w, ev(w.now, map[string]any{"type": "task_started", "turn_id": "t1"}),
			ev(w.now.Add(time.Second), map[string]any{"type": "token_count"}),
			ev(w.now.Add(2*time.Second), map[string]any{"type": "task_complete", "turn_id": "t1"}))
		w.now = w.now.Add(31 * time.Minute)
		if r, err := w.host.Ended(ctx, s); err != nil || !r.Ended {
			t.Fatalf("Ended = %+v, %v", r, err)
		}
	})
	t.Run("tool call running", func(t *testing.T) {
		w := newWorld(t)
		s := w.start(agenthost.Codex)
		rollout(w, ev(w.now, map[string]any{"type": "task_started", "turn_id": "t1"}),
			map[string]any{"type": "response_item", "timestamp": ts(w.now.Add(time.Second)), "payload": map[string]any{"type": "function_call"}})
		w.now = w.now.Add(3 * time.Hour)
		if r, err := w.host.Ended(ctx, s); err != nil || r.Ended {
			t.Fatalf("Ended = %+v, %v", r, err)
		}
	})
	t.Run("command still running after the turn completed", func(t *testing.T) {
		w := newWorld(t)
		s := w.start(agenthost.Codex)
		item := func(at time.Time, payload map[string]any) map[string]any {
			return map[string]any{"type": "response_item", "timestamp": ts(at), "payload": payload}
		}
		rollout(w, ev(w.now, map[string]any{"type": "task_started", "turn_id": "t1"}),
			item(w.now.Add(time.Second), map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "c1",
				"input": `text(await tools.exec_command({cmd:"pnpm test",yield_time_ms:1000}));`}),
			item(w.now.Add(2*time.Second), map[string]any{"type": "custom_tool_call_output", "call_id": "c1", "output": []any{
				map[string]any{"type": "input_text", "text": "Script completed\nWall time 1.0 seconds\nOutput:\n"},
				map[string]any{"type": "input_text", "text": `{"session_id":75846,"output":"running"}`},
			}}),
			ev(w.now.Add(3*time.Second), map[string]any{"type": "task_complete", "turn_id": "t1"}))
		w.now = w.now.Add(3 * time.Hour)
		r, err := w.host.Ended(ctx, s)
		if err != nil || r.Ended || !strings.Contains(r.Reason, "Codex has a command launch without its native terminal outcome") {
			t.Fatalf("Ended = %+v, %v", r, err)
		}
	})
	t.Run("other checkout is not this session", func(t *testing.T) {
		w := newWorld(t)
		s := w.start(agenthost.Codex)
		writeJSONL(t, filepath.Join(w.codex, w.now.Format("2006/01/02"), "rollout-2.jsonl"), []map[string]any{
			{"type": "session_meta", "timestamp": ts(w.now), "payload": map[string]any{"id": "x", "cwd": "/elsewhere"}},
		})
		w.now = w.now.Add(3 * time.Hour)
		if r, _ := w.host.Ended(ctx, s); r.Ended || !strings.Contains(r.Reason, "no transcript") {
			t.Fatalf("Ended = %+v", r)
		}
	})
}

// TestCodexScanCachesRolloutHeads proves parity 9.22, ported from
// codex-sessions.ts and codex-census.ts in alpha: a tick re-reads the head of
// no rollout that is unchanged since the last tick, so the cost of a tick
// stops growing with the rollouts of other sessions.
func TestCodexScanCachesRolloutHeads(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*world, agenthost.Session, string) {
		w := newWorld(t)
		s := w.start(agenthost.Codex)
		day := filepath.Join(w.codex, w.now.Format("2006/01/02"))
		for _, n := range []string{"rollout-a.jsonl", "rollout-b.jsonl", "rollout-c.jsonl"} {
			writeJSONL(t, filepath.Join(day, n), []map[string]any{
				{"type": "session_meta", "timestamp": ts(w.now), "payload": map[string]any{"id": n, "cwd": "/elsewhere"}},
			})
		}
		writeJSONL(t, filepath.Join(day, "rollout-own.jsonl"), []map[string]any{
			{"type": "session_meta", "timestamp": ts(w.now), "payload": map[string]any{"id": "sess", "cwd": w.checkout}},
			{"type": "event_msg", "timestamp": ts(w.now), "payload": map[string]any{"type": "task_started", "turn_id": "t1"}},
			{"type": "event_msg", "timestamp": ts(w.now.Add(time.Second)), "payload": map[string]any{"type": "task_complete", "turn_id": "t1"}},
		})
		return w, s, day
	}
	tick := func(t *testing.T, w *world, s agenthost.Session) (int64, agenthost.EndReport) {
		t.Helper()
		before := orca.HeadReads(w.host)
		r, err := w.host.Ended(ctx, s)
		if err != nil {
			t.Fatalf("Ended: %v", err)
		}
		return orca.HeadReads(w.host) - before, r
	}
	t.Run("an unchanged rollout is read once", func(t *testing.T) {
		w, s, day := setup(t)
		orca.SetWall(w.host, func() time.Time { return time.Now().Add(time.Hour) })
		w.now = w.now.Add(31 * time.Minute)
		if n, r := tick(t, w, s); n != 4 || !r.Ended {
			t.Fatalf("first tick read %d heads, report %+v; want 4, ended", n, r)
		}
		if n, r := tick(t, w, s); n != 0 || !r.Ended {
			t.Fatalf("second tick read %d heads, report %+v; want 0, ended", n, r)
		}
		f, err := os.OpenFile(filepath.Join(day, "rollout-b.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("{}\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if n, _ := tick(t, w, s); n != 1 {
			t.Fatalf("tick after a write read %d heads; want 1", n)
		}
	})
	t.Run("a rollout that changed within the last second reads again", func(t *testing.T) {
		w, s, _ := setup(t)
		orca.SetWall(w.host, func() time.Time { return time.Now().Add(-time.Hour) })
		w.now = w.now.Add(31 * time.Minute)
		for i := range 2 {
			if n, _ := tick(t, w, s); n != 4 {
				t.Fatalf("tick %d read %d heads; want 4", i, n)
			}
		}
	})
	t.Run("a cached head still finds the session's own rollout", func(t *testing.T) {
		w, s, day := setup(t)
		orca.SetWall(w.host, func() time.Time { return time.Now().Add(time.Hour) })
		if _, r := tick(t, w, s); r.Ended {
			t.Fatalf("Ended inside the quiet period: %+v", r)
		}
		if err := os.Remove(filepath.Join(day, "rollout-own.jsonl")); err != nil {
			t.Fatal(err)
		}
		w.now = w.now.Add(31 * time.Minute)
		if _, r := tick(t, w, s); r.Ended || !strings.Contains(r.Reason, "no transcript") {
			t.Fatalf("Ended after its rollout was removed: %+v", r)
		}
	})
}

// ---- Notify ----

func TestNotify(t *testing.T) {
	ctx := context.Background()
	t.Run("delivers to an idle coordinator", func(t *testing.T) {
		w := newWorld(t)
		spec := w.spec(agenthost.Claude)
		spec.Kind = agenthost.KindCoordinator
		s, err := w.host.Start(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.host.Notify(ctx, s.ID, "read the inbox"); err != nil {
			t.Fatalf("Notify = %v", err)
		}
		args := w.sim.allArgs()
		last := args[len(args)-1]
		if strings.Join(last[:2], " ") != "terminal send" || flag(last, "--text") != "read the inbox" || !slices.Contains(last, "--enter") {
			t.Fatalf("send args = %q", last)
		}
	})
	refusals := map[string]func() orca.Output{
		"refused with receipt on exit 1": func() orca.Output {
			return failOut("terminal_write_refused", map[string]any{"send": map[string]any{"accepted": false, "bytesWritten": 0}})
		},
		"held for a mobile driver": func() orca.Output {
			return okOut(map[string]any{"send": map[string]any{"accepted": false, "bytesWritten": 0}}, "rt-1")
		},
		"no send receipt":  func() orca.Output { return okOut(map[string]any{}, "rt-1") },
		"malformed output": func() orca.Output { return orca.Output{Code: 1, Stdout: []byte("oops")} },
	}
	for name, reply := range refusals {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.start(agenthost.Claude)
			w.sim.sendReply = reply
			if err := w.host.Notify(ctx, idA, "x"); !errors.Is(err, agenthost.ErrNotifyRefused) {
				t.Fatalf("Notify = %v; want ErrNotifyRefused", err)
			}
		})
	}
	t.Run("stale handle never receives input", func(t *testing.T) {
		w := newWorld(t)
		w.start(agenthost.Claude)
		other := idA
		other.Incarnation = "inc-replacement"
		err := w.host.Notify(ctx, other, "x")
		if !errors.Is(err, agenthost.ErrNotifyRefused) || !errors.Is(err, agenthost.ErrStaleHandle) {
			t.Fatalf("Notify = %v", err)
		}
		if slices.Contains(w.sim.verbs(), "terminal send") {
			t.Fatal("input reached a terminal of another incarnation")
		}
	})
}

// ---- Quota ----

func TestQuotaCaptured(t *testing.T) {
	w := newWorld(t)
	w.sim.override["account list"] = func([]string) (orca.Output, error) { return fixture(t, "account_list.json"), nil }
	q, err := w.host.Quota(context.Background(), agenthost.Claude)
	if err != nil || !q.OK || q.UpdatedAt.IsZero() {
		t.Fatalf("Quota = %+v, %v", q, err)
	}
	want := []agenthost.QuotaWindow{{Name: "fableWeekly", UsedPercent: 20}, {Name: "session", UsedPercent: 10}, {Name: "weekly", UsedPercent: 50}}
	if !slices.Equal(q.Windows, want) {
		t.Fatalf("windows = %+v", q.Windows)
	}
	cq, err := w.host.Quota(context.Background(), agenthost.Codex)
	if err != nil || !cq.OK || len(cq.Windows) != 1 {
		t.Fatalf("codex Quota = %+v, %v", cq, err)
	}
	if _, err := w.host.Quota(context.Background(), "grok"); !errors.Is(err, agenthost.ErrQuotaUnavailable) {
		t.Fatalf("absent provider = %v", err)
	}
	w.sim.override["account list"] = func([]string) (orca.Output, error) { return orca.Output{}, os.ErrNotExist }
	if _, err := w.host.Quota(context.Background(), agenthost.Claude); !errors.Is(err, agenthost.ErrQuotaUnavailable) {
		t.Fatalf("missing CLI = %v", err)
	}
}

// A Claude Code that sends its requests through a proxy spends quota that
// orca's reading does not describe, so Claude has no reading.
func TestQuotaOfAProxiedClaudeIsUnavailable(t *testing.T) {
	w := newWorld(t)
	w.sim.override["account list"] = func([]string) (orca.Output, error) { return fixture(t, "account_list.json"), nil }
	settings := filepath.Join(filepath.Dir(w.claude), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"env": {"ANTHROPIC_BASE_URL": "http://127.0.0.1:8080"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.host.Quota(context.Background(), agenthost.Claude); !errors.Is(err, agenthost.ErrQuotaUnavailable) {
		t.Fatalf("proxied claude = %v", err)
	}
	if cq, err := w.host.Quota(context.Background(), agenthost.Codex); err != nil || !cq.OK {
		t.Fatalf("codex Quota = %+v, %v", cq, err)
	}
}

// ---- Checkouts ----

func TestCreateCheckout(t *testing.T) {
	w := newWorld(t, func(c *orca.Config) { c.Repo = "path:/repo" })
	var got []string
	w.sim.override["worktree create"] = func(args []string) (orca.Output, error) {
		got = args
		return okOut(map[string]any{"worktree": map[string]any{
			"id": "repo::" + filepath.Join(w.root, "tic-1"), "path": filepath.Join(w.root, "tic-1"), "branch": "refs/heads/tic-1",
		}}, "rt-1"), nil
	}
	co, err := w.host.CreateCheckout(context.Background(), agenthost.CheckoutSpec{Name: "tic-1", BaseBranch: "main", Setup: true})
	if err != nil || co.Path != filepath.Join(w.root, "tic-1") {
		t.Fatalf("CreateCheckout = %+v, %v", co, err)
	}
	for _, f := range []string{"--agent", "--prompt", "--model"} {
		if slices.Contains(got, f) {
			t.Fatalf("worktree create passed %s: %q", f, got)
		}
	}
	if flag(got, "--setup") != "run" || flag(got, "--repo") != "path:/repo" || !slices.Contains(got, "--no-parent") {
		t.Fatalf("worktree create args = %q", got)
	}
}

func TestCreateCheckoutOutsideRoot(t *testing.T) {
	w := newWorld(t)
	w.sim.override["worktree create"] = func([]string) (orca.Output, error) {
		return okOut(map[string]any{"worktree": map[string]any{"path": "/somewhere/else"}}, "rt-1"), nil
	}
	if _, err := w.host.CreateCheckout(context.Background(), agenthost.CheckoutSpec{Name: "x"}); !errors.Is(err, agenthost.ErrOutsideRoot) {
		t.Fatalf("CreateCheckout = %v", err)
	}
	if _, err := w.host.CreateCheckout(context.Background(), agenthost.CheckoutSpec{Name: "../x"}); !errors.Is(err, agenthost.ErrBadSpec) {
		t.Fatalf("CreateCheckout bad name = %v", err)
	}
}

func TestRemoveCheckout(t *testing.T) {
	ctx := context.Background()
	t.Run("outside root never reaches orca", func(t *testing.T) {
		w := newWorld(t)
		for _, p := range []string{"/etc", w.root, filepath.Join(w.root, "..", "x"), "relative"} {
			if err := w.host.RemoveCheckout(ctx, p); !errors.Is(err, agenthost.ErrOutsideRoot) {
				t.Fatalf("RemoveCheckout(%s) = %v", p, err)
			}
		}
		if len(w.sim.allArgs()) != 0 {
			t.Fatal("an outside-root removal reached orca")
		}
	})
	t.Run("symlink out of the root", func(t *testing.T) {
		w := newWorld(t)
		link := filepath.Join(w.root, "link")
		if err := os.Symlink(t.TempDir(), link); err != nil {
			t.Fatal(err)
		}
		if err := w.host.RemoveCheckout(ctx, link); !errors.Is(err, agenthost.ErrOutsideRoot) {
			t.Fatalf("RemoveCheckout(link) = %v", err)
		}
	})
	t.Run("live agent keeps it", func(t *testing.T) {
		w := newWorld(t)
		w.start(agenthost.Claude)
		if err := w.host.RemoveCheckout(ctx, w.checkout); !errors.Is(err, agenthost.ErrCheckoutBusy) {
			t.Fatalf("RemoveCheckout = %v", err)
		}
		if slices.Contains(w.sim.verbs(), "worktree rm") {
			t.Fatal("a checkout with a live agent was removed")
		}
	})
	t.Run("a shell closes with it", func(t *testing.T) {
		w := newWorld(t)
		w.sim.created, w.sim.ready = true, false // orca's own shell in the new worktree, with no agent
		w.sim.override["worktree rm"] = func([]string) (orca.Output, error) { return okOut(map[string]any{}, w.sim.runtime), nil }
		if err := w.host.RemoveCheckout(ctx, w.checkout); err != nil {
			t.Fatalf("RemoveCheckout = %v", err)
		}
		v := w.sim.verbs()
		if i, j := slices.Index(v, "terminal close"), slices.Index(v, "worktree rm"); i < 0 || j < i {
			t.Fatalf("calls = %v; want the shell closed before the remove", v)
		}
	})
	t.Run("incomplete inventory keeps it", func(t *testing.T) {
		w := newWorld(t)
		w.sim.override["terminal list"] = func([]string) (orca.Output, error) { return fixture(t, "terminal_list_truncated.json"), nil }
		if err := w.host.RemoveCheckout(ctx, w.checkout); !errors.Is(err, agenthost.ErrCheckoutBusy) {
			t.Fatalf("RemoveCheckout = %v", err)
		}
	})
	t.Run("empty checkout is removed", func(t *testing.T) {
		w := newWorld(t)
		var got []string
		w.sim.override["worktree rm"] = func(args []string) (orca.Output, error) { got = args; return okOut(map[string]any{}, "rt-1"), nil }
		if err := w.host.RemoveCheckout(ctx, w.checkout); err != nil {
			t.Fatal(err)
		}
		if flag(got, "--worktree") != "path:"+w.checkout || !slices.Contains(got, "--force") {
			t.Fatalf("worktree rm args = %q", got)
		}
	})
	t.Run("a worktree orca does not know", func(t *testing.T) {
		w := newWorld(t)
		w.sim.override["worktree rm"] = func([]string) (orca.Output, error) { return failOut("selector_not_found", nil), nil }
		if err := w.host.RemoveCheckout(ctx, w.checkout); !errors.Is(err, agenthost.ErrUnknownCheckout) {
			t.Fatalf("RemoveCheckout = %v", err)
		}
	})
}

// ---- The exec layer ----

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-orca")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecRunner(t *testing.T) {
	ctx := context.Background()
	if _, err := (orca.ExecRunner{Bin: filepath.Join(t.TempDir(), "absent")}).Run(ctx, []string{"terminal", "list"}); err == nil {
		t.Fatal("a missing binary ran")
	}
	out, err := orca.ExecRunner{Bin: script(t, `echo '{"ok":false}'; exit 1`)}.Run(ctx, []string{"x"})
	if err != nil || out.Code != 1 || !strings.Contains(string(out.Stdout), `"ok":false`) {
		t.Fatalf("Run = %+v, %v", out, err)
	}
}

func TestHungOrcaIsCancelledAndUnknown(t *testing.T) {
	h, err := orca.New(orca.Config{Bin: script(t, "exec sleep 30"), CheckoutRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	begin := time.Now()
	st, err := h.Settle(ctx, idA)
	if took := time.Since(begin); took > 5*time.Second {
		t.Fatalf("a hung orca call ran %s after cancel", took)
	}
	if st.Liveness != agenthost.Unknown || !errors.Is(err, agenthost.ErrHostUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Settle = %+v, %v", st, err)
	}
}

// A scheduler tick passes a context with no deadline. The runner's own
// timeout then kills a hung orca, and the call reads as Unknown.
func TestHungOrcaWithNoDeadlineTimesOut(t *testing.T) {
	run := orca.ExecRunner{Bin: script(t, "exec sleep 30"), Timeout: 200 * time.Millisecond}
	h, err := orca.New(orca.Config{Runner: run, CheckoutRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	st, err := h.Settle(context.Background(), idA)
	if took := time.Since(begin); took > 5*time.Second {
		t.Fatalf("a hung orca call ran %s with no deadline", took)
	}
	if st.Liveness != agenthost.Unknown || !errors.Is(err, agenthost.ErrHostUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Settle = %+v, %v", st, err)
	}
}

// A caller's earlier deadline still wins over the runner's timeout.
func TestCallerDeadlineBeatsTheRunnerTimeout(t *testing.T) {
	run := orca.ExecRunner{Bin: script(t, "exec sleep 30"), Timeout: time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if _, err := run.Run(ctx, []string{"terminal", "list"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v", err)
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Fatalf("a hung orca call ran %s after the caller's deadline", took)
	}
}

func TestMissingCLIIsUnknownNeverExited(t *testing.T) {
	h, err := orca.New(orca.Config{Bin: filepath.Join(t.TempDir(), "orca"), CheckoutRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	st, err := h.Settle(context.Background(), idA)
	if st.Liveness != agenthost.Unknown || !errors.Is(err, agenthost.ErrHostUnavailable) {
		t.Fatalf("Settle = %+v, %v", st, err)
	}
	inv, err := h.Inventory(context.Background())
	if inv.Complete || err == nil {
		t.Fatalf("Inventory = %+v, %v", inv, err)
	}
	if _, err := h.Stop(context.Background(), idA); !errors.Is(err, agenthost.ErrExitUnproven) {
		t.Fatalf("Stop = %v", err)
	}
}

func TestNewRefusesBadConfig(t *testing.T) {
	if _, err := orca.New(orca.Config{CheckoutRoot: "/r"}); !errors.Is(err, agenthost.ErrBadSpec) {
		t.Fatalf("no binary = %v", err)
	}
	if _, err := orca.New(orca.Config{Bin: "orca", CheckoutRoot: "rel"}); !errors.Is(err, agenthost.ErrBadSpec) {
		t.Fatalf("relative root = %v", err)
	}
}

// ---- Daemon death ----

// TestDaemonDeathNewIncarnationIsNotTheRun replays the measured daemon
// death (docs/evidence/orca-restart.md): the OOM-killed daemon came back
// with the same handle and PTY, and a fresh shell. Whether orca then shows
// another incarnation is unmeasured. A row with the same handle and another
// incarnation is never the run: liveness reads unknown, the inventory never
// reads present, the run is never re-adopted, and no input or close reaches
// the fresh shell.
func TestDaemonDeathNewIncarnationIsNotTheRun(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.start(agenthost.Claude)
	if s.ID != idA || s.Pty != "pty-A" {
		t.Fatalf("session = %+v", s)
	}
	w.sim.respawned = "inc-fresh-shell"

	st, err := w.host.Settle(ctx, s.ID)
	if st.Liveness != agenthost.Unknown || !errors.Is(err, agenthost.ErrStaleHandle) {
		t.Fatalf("Settle = %+v, %v; want unknown, stale", st, err)
	}
	if r, err := w.host.Ended(ctx, s); r.Ended || err == nil {
		t.Fatalf("Ended = %+v, %v; want not ended with an error", r, err)
	}

	inv, err := w.host.Inventory(ctx)
	if err != nil || !inv.Complete || inv.Epoch != s.Epoch {
		t.Fatalf("Inventory = %+v, %v", inv, err)
	}
	if len(inv.Rows) != 1 || inv.Rows[0].ID.Terminal != s.ID.Terminal || inv.Rows[0].ID == s.ID {
		t.Fatalf("rows = %+v; want the same handle under another incarnation", inv.Rows)
	}
	if got := inv.Lookup(s); got != agenthost.PresenceUnknown {
		t.Fatalf("Lookup with the same handle and PTY under another incarnation = %s; want unknown", got)
	}
	if _, ok := inv.Readopted(s); ok {
		t.Fatal("Readopted took a row of another incarnation as the run")
	}
	if tr := inv.Traces(s); len(tr) != 1 {
		t.Fatalf("Traces = %+v; want the fresh shell", tr)
	}
	// With no recorded PTY nothing ties the fresh shell to the run. The
	// complete inventory of the same epoch then reads the run absent,
	// which is the ended reading, and still never present.
	noPty := s
	noPty.Pty = ""
	if got := inv.Lookup(noPty); got != agenthost.Absent {
		t.Fatalf("Lookup without a PTY = %s; want absent", got)
	}

	if err := w.host.Notify(ctx, s.ID, "x"); !errors.Is(err, agenthost.ErrNotifyRefused) {
		t.Fatalf("Notify = %v; want refused", err)
	}
	if slices.Contains(w.sim.verbs(), "terminal send") {
		t.Fatal("input reached the fresh shell")
	}
	if st, err := w.host.Stop(ctx, s.ID); st.Liveness == agenthost.Exited || !errors.Is(err, agenthost.ErrExitUnproven) || w.sim.closed {
		t.Fatalf("Stop = %+v, %v, closed=%v; want unproven and no close", st, err, w.sim.closed)
	}
}

// C-bug-1: the orca launch hands the agent the same first message as
// HarnessArgv, and its first line names the session's work.
func TestLaunchPromptOpensWithTheTitle(t *testing.T) {
	w := newWorld(t)
	w.sim.agent = string(agenthost.Claude)
	spec := w.spec(agenthost.Claude)
	spec.Title = "loop alpha implementer tk-1: Fix the thing"
	if _, err := w.host.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	var launch []string
	for _, args := range w.sim.allArgs() {
		if strings.Join(args[:2], " ") == "agent launch" {
			launch = args
		}
	}
	argv, err := agenthost.HarnessArgv(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := strings.Cut(flag(launch, "--prompt"), "\n")
	want, _, _ := strings.Cut(argv[len(argv)-1], "\n")
	if got != want || !strings.Contains(got, spec.Title) {
		t.Fatalf("orca first line %q, HarnessArgv first line %q, title %q", got, want, spec.Title)
	}
}
