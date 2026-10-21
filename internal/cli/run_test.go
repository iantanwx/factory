package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// withHandler returns the full surface with one verb implemented.
func withHandler(t *testing.T, path string, h Handler) *Registry {
	t.Helper()
	r := New()
	must(r.SetHandler(path, h))
	return r
}

func TestVerbThatFindsWorkPrintsOneOkEnvelope(t *testing.T) {
	r := withHandler(t, "work peek", func(_ context.Context, inv Invocation) Result {
		return Found(map[string]string{"item": "T-7", "stage": inv.Flags["stage"]})
	})
	env, code := runJSON(t, r, "work", "peek", "--stage", "implement")
	if code != 0 || env["ok"] != true || env["verb"] != "work peek" {
		t.Fatalf("want ok exit 0 for work peek, got %v", env)
	}
	data := env["data"].(map[string]any)
	if data["item"] != "T-7" || data["stage"] != "implement" {
		t.Fatalf("data = %v", data)
	}
	if id, _ := env["request_id"].(string); len(id) != 16 {
		t.Fatalf("request_id = %v", env["request_id"])
	}
	if _, ok := env["server_version"]; !ok {
		t.Fatal("server_version key missing")
	}
}

func TestEmptyResultExits1WithEmptyList(t *testing.T) {
	r := withHandler(t, "work list", func(context.Context, Invocation) Result {
		return List([]string(nil))
	})
	var out bytes.Buffer
	code := r.Run(context.Background(), []string{"work", "list"}, &out, &bytes.Buffer{})
	if code != 1 || !strings.Contains(out.String(), `"data":[]`) {
		t.Fatalf("want exit 1 and data [], got %d %s", code, out.String())
	}
}

func TestUsageFaults(t *testing.T) {
	cases := []struct {
		name string
		args []string
		rule string
	}{
		{"unknown flag", []string{"work", "peek", "--stage", "x", "--bogus"}, "unknown_flag"},
		{"unknown flag before verb", []string{"--bogus", "work", "peek"}, "unknown_flag"},
		{"single-dash flag", []string{"work", "list", "-x"}, "unknown_flag"},
		{"unknown flag beats bad value", []string{"run", "finish", "--outcome", "maybe", "--bogus"}, "unknown_flag"},
		{"unknown flag beats missing flag", []string{"run", "finish", "--bogus"}, "unknown_flag"},
		{"bad enum value", []string{"run", "finish", "--outcome", "maybe"}, "bad_flag_value"},
		{"bad time value", []string{"inbox", "defer", "E-1", "--until", "tomorrow"}, "bad_flag_value"},
		{"bad bool value", []string{"install", "--check=maybe"}, "bad_flag_value"},
		{"bad global bool", []string{"work", "list", "--text=maybe"}, "bad_flag_value"},
		{"empty string value", []string{"work", "peek", "--stage="}, "bad_flag_value"},
		{"missing value", []string{"work", "peek", "--stage"}, "missing_value"},
		{"flag as value", []string{"work", "peek", "--stage", "--json"}, "missing_value"},
		{"repeated flag", []string{"work", "peek", "--stage", "a", "--stage", "b"}, "repeated_flag"},
		{"missing required flag", []string{"run", "finish"}, "missing_flag"},
		{"missing argument", []string{"work", "show"}, "missing_argument"},
		{"extra argument", []string{"work", "show", "T-1", "T-2"}, "extra_argument"},
		{"unknown noun", []string{"wrok", "peek"}, "unknown_noun"},
		{"unknown verb", []string{"work", "teleport"}, "unknown_verb"},
		{"missing verb", []string{"work"}, "missing_verb"},
		{"nothing", nil, "missing_noun"},
		{"json and text", []string{"schema", "--json", "--text"}, "flag_conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			code := New().Run(context.Background(), tc.args, &out, &bytes.Buffer{})
			if code != 2 {
				t.Fatalf("exit %d, want 2: %s", code, out.String())
			}
			if tc.rule == "flag_conflict" {
				return // --text is on the line, so the output is text
			}
			var env map[string]any
			if err := json.Unmarshal(out.Bytes(), &env); err != nil {
				t.Fatalf("not one JSON envelope: %v %s", err, out.String())
			}
			requireKeys(t, env, envelopeKeys...)
			requireKeys(t, env, refusalKeys...)
			if env["code"] != CodeUsage || env["retryable"] != false || env["ok"] != false {
				t.Fatalf("want USAGE retryable false, got %v", env)
			}
			if env["rule"] != tc.rule {
				t.Fatalf("rule = %v, want %s", env["rule"], tc.rule)
			}
			if next := env["allowed_next"].([]any); len(next) == 0 {
				t.Fatalf("a usage fault names a verb to run next: %v", env)
			}
		})
	}
}

func TestUnknownVerbNamesTheNounsVerbs(t *testing.T) {
	env, _ := runJSON(t, New(), "branch", "shove")
	next := env["allowed_next"].([]any)
	if len(next) != 2 || next[0] != "branch push" || next[1] != "branch sync" {
		t.Fatalf("allowed_next = %v", next)
	}
}

func TestHandlerReceivesCanonicalArgsAndFlags(t *testing.T) {
	var got Invocation
	r := withHandler(t, "inbox defer", func(_ context.Context, inv Invocation) Result {
		got = inv
		return Found(nil)
	})
	_, code := runJSON(t, r, "--json", "inbox", "defer", "--until=2026-10-21T10:00:00+02:00", "E-1")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got.Arg("entry") != "E-1" || got.Flags["until"] != "2026-10-21T08:00:00Z" {
		t.Fatalf("invocation = %+v", got)
	}
	if _, has := got.Flags["json"]; has {
		t.Fatalf("output flags never reach the handler: %v", got.Flags)
	}
}

func TestDoubleDashEndsFlags(t *testing.T) {
	var got Invocation
	r := withHandler(t, "work show", func(_ context.Context, inv Invocation) Result {
		got = inv
		return Found(nil)
	})
	if _, code := runJSON(t, r, "work", "show", "--", "--odd-id"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got.Arg("item") != "--odd-id" {
		t.Fatalf("args = %v", got.Args)
	}
}

func TestUnimplementedVerbsRefuseWithNotImplemented(t *testing.T) {
	for _, args := range [][]string{{"work", "list"}, {"status"}, {"doctor"}, {"escalation", "judge", "--finding", "f"}} {
		env, code := runJSON(t, New(), args...)
		if code != 2 || env["code"] != CodeNotImplemented || env["retryable"] != false {
			t.Errorf("%v: want exit 2 NOT_IMPLEMENTED, got %v", args, env)
		}
	}
}

func TestRemoteAnswersServerVerbsOnly(t *testing.T) {
	r := New()
	r.SetRemote(func(_ context.Context, inv Invocation) Result {
		return Found(inv.Verb.RPC)
	})
	if env, _ := runJSON(t, r, "merge", "status"); env["data"] != "/loop.v1.MergeService/Status" {
		t.Fatalf("server verb should go to the remote: %v", env)
	}
	if env, _ := runJSON(t, r, "doctor"); env["code"] != CodeNotImplemented {
		t.Fatalf("a client verb never goes to the remote: %v", env)
	}
	if env, _ := runJSON(t, r, "version"); env["ok"] != true {
		t.Fatalf("a verb with its own handler keeps it: %v", env)
	}
}

func TestVersionPrintsTheBuildVersion(t *testing.T) {
	env, code := runJSON(t, New(), "version")
	if code != 0 || env["data"].(map[string]any)["version"] != Version {
		t.Fatalf("version = %v", env)
	}
}

func TestPanickingHandlerBecomesInternal(t *testing.T) {
	r := withHandler(t, "work list", func(context.Context, Invocation) Result { panic("boom") })
	env, code := runJSON(t, r, "work", "list")
	if code != 2 || env["code"] != CodeInternal || !strings.Contains(env["hint"].(string), "boom") {
		t.Fatalf("got %v", env)
	}
}

func TestTextFlagPrintsHumanForm(t *testing.T) {
	var out bytes.Buffer
	code := New().Run(context.Background(), []string{"version", "--text"}, &out, &bytes.Buffer{})
	if code != 0 || json.Valid(out.Bytes()) || !strings.HasPrefix(out.String(), "ok: version (exit 0)") {
		t.Fatalf("got %d %q", code, out.String())
	}
	out.Reset()
	code = New().Run(context.Background(), []string{"--text", "work", "--bogus"}, &out, &bytes.Buffer{})
	if code != 2 || !strings.Contains(out.String(), "code: USAGE") {
		t.Fatalf("a usage fault honours --text: %d %q", code, out.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errClosed }

var errClosed = errors.New("closed")

func TestWriteFailureExits2AndSaysSoOnStderr(t *testing.T) {
	var errOut bytes.Buffer
	if code := New().Run(context.Background(), []string{"version"}, failingWriter{}, &errOut); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "write output") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}
