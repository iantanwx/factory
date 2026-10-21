package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// sealed prints one result the way Run does and decodes it back.
func sealed(t *testing.T, res Result) (map[string]any, string) {
	t.Helper()
	var out bytes.Buffer
	env := NewRegistry().seal("work peek", "req-1", res)
	if err := writeEnvelope(&out, env, false); err != nil {
		t.Fatalf("writeEnvelope: %v", err)
	}
	raw := out.String()
	if strings.Count(raw, "\n") != 1 || !strings.HasSuffix(raw, "\n") {
		t.Fatalf("want exactly one newline-terminated line, got %q", raw)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, raw)
	}
	return got, raw
}

func requireKeys(t *testing.T, got map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := got[k]; !ok {
			t.Errorf("envelope lacks key %q: %v", k, got)
		}
	}
}

var envelopeKeys = []string{"ok", "exit", "verb", "request_id", "server_version", "data"}

var refusalKeys = []string{
	"code", "rule", "item", "current_status", "expected_status",
	"allowed_next", "hint", "retryable", "retry_after_seconds",
}

func TestFoundPrintsOkEnvelopeWithData(t *testing.T) {
	got, _ := sealed(t, Found(map[string]string{"item": "T-1"}))
	requireKeys(t, got, envelopeKeys...)
	if got["ok"] != true || got["exit"] != float64(0) {
		t.Fatalf("want ok true exit 0, got %v", got)
	}
	if got["verb"] != "work peek" || got["request_id"] != "req-1" {
		t.Fatalf("verb or request_id wrong: %v", got)
	}
	if data, _ := got["data"].(map[string]any); data["item"] != "T-1" {
		t.Fatalf("data wrong: %v", got["data"])
	}
	if _, has := got["code"]; has {
		t.Fatalf("a found result carries no refusal fields: %v", got)
	}
}

func TestEmptyResultPrintsEmptyListWithExit1(t *testing.T) {
	cases := map[string]Result{
		"Nothing":        Nothing(),
		"List(nil)":      List([]string(nil)),
		"List(empty)":    List([]int{}),
		"bare exit 1":    {Exit: ExitNothing},
		"nil slice data": {Exit: ExitNothing, Data: []string(nil)},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			got, raw := sealed(t, res)
			requireKeys(t, got, envelopeKeys...)
			if !strings.Contains(raw, `"data":[]`) {
				t.Fatalf("want data: [], got %s", raw)
			}
			if got["exit"] != float64(1) || got["ok"] != true {
				t.Fatalf("want ok true exit 1, got %v", got)
			}
		})
	}
}

func TestListWithItemsExits0(t *testing.T) {
	got, _ := sealed(t, List([]string{"T-1"}))
	if got["exit"] != float64(0) {
		t.Fatalf("want exit 0, got %v", got["exit"])
	}
}

func TestRefusalTakesExitAndRetryableFromTable(t *testing.T) {
	got, raw := sealed(t, Refuse(Refusal{Code: CodeStatusChanged, Item: "T-1", CurrentStatus: "review"}))
	requireKeys(t, got, envelopeKeys...)
	requireKeys(t, got, refusalKeys...)
	if got["ok"] != false || got["exit"] != float64(3) || got["code"] != CodeStatusChanged {
		t.Fatalf("want ok false exit 3 code STATUS_CHANGED, got %v", got)
	}
	if !strings.Contains(raw, `"allowed_next":[]`) {
		t.Fatalf("allowed_next must be [] when empty: %s", raw)
	}
	if got["data"] != nil {
		t.Fatalf("a refusal carries data null, got %v", got["data"])
	}

	got, _ = sealed(t, Refuse(Refusal{Code: CodeDraining, Retryable: false}))
	if got["exit"] != float64(4) || got["retryable"] != true {
		t.Fatalf("the table decides exit and retryable: %v", got)
	}
}

func TestUnregisteredCodeBecomesInternal(t *testing.T) {
	got, _ := sealed(t, Refuse(Refusal{Code: "NO_SUCH_CODE"}))
	if got["code"] != CodeInternal || got["exit"] != float64(2) {
		t.Fatalf("an unregistered code must not reach the caller: %v", got)
	}
	if !strings.Contains(got["hint"].(string), "NO_SUCH_CODE") {
		t.Fatalf("hint should name the unregistered code: %v", got["hint"])
	}
}

func TestRefusalExitWithoutCodeBecomesInternal(t *testing.T) {
	got, _ := sealed(t, Result{Exit: ExitRefused})
	if got["code"] != CodeInternal {
		t.Fatalf("exit 3 without a refusal must become INTERNAL: %v", got)
	}
}

func TestTextRendersHumanForm(t *testing.T) {
	var out bytes.Buffer
	env := NewRegistry().seal("work peek", "req-1", Refuse(Refusal{Code: CodeUsage, Hint: "unknown flag --x"}))
	if err := writeEnvelope(&out, env, true); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if json.Valid(out.Bytes()) {
		t.Fatalf("--text must not print JSON: %s", text)
	}
	for _, want := range []string{"refused", "exit 2", "code: USAGE", "hint: unknown flag --x"} {
		if !strings.Contains(text, want) {
			t.Errorf("text output lacks %q:\n%s", want, text)
		}
	}
}
