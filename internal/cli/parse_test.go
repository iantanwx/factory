package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A body can start with "--", such as a Markdown rule or a YAML front
// matter. Only a flag of the verb ends a flag that takes a value.
func TestValueThatStartsWithDashes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		body string
	}{
		{"markdown rule", []string{"--body", "--- hi"}, "--- hi"},
		{"not a flag of the verb", []string{"--body", "--bogus"}, "--bogus"},
		{"negative number", []string{"--body", "-1"}, "-1"},
		{"equals form", []string{"--body=--json"}, "--json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got Invocation
			r := withHandler(t, "work note", func(_ context.Context, inv Invocation) Result {
				got = inv
				return Found(map[string]string{})
			})
			args := append([]string{"work", "note", "T-1"}, tc.args...)
			var out bytes.Buffer
			if code := r.Run(context.Background(), args, &out, &bytes.Buffer{}); code != 0 {
				t.Fatalf("exit %d: %s", code, out.String())
			}
			if got.Flags["body"] != tc.body {
				t.Fatalf("body = %q, want %q", got.Flags["body"], tc.body)
			}
		})
	}
}

// A flag of the verb after a flag that takes a value is a missing value, and
// the hint names the --flag=value form.
func TestFlagOfTheVerbIsAMissingValue(t *testing.T) {
	for _, next := range []string{"--json", "--project", "--project=x", "-h", "--"} {
		var out bytes.Buffer
		code := New().Run(context.Background(), []string{"work", "note", "T-1", "--body", next}, &out, &bytes.Buffer{})
		var env map[string]any
		if err := json.Unmarshal(out.Bytes(), &env); err != nil {
			t.Fatalf("%s: not one JSON envelope: %v %s", next, err, out.String())
		}
		hint, _ := env["hint"].(string)
		if code != 2 || env["rule"] != "missing_value" || !strings.Contains(hint, "--body=<value>") {
			t.Fatalf("%s: exit %d, envelope %v", next, code, env)
		}
	}
}

func TestUnknownFlagHintNamesTheEqualsForm(t *testing.T) {
	var out bytes.Buffer
	New().Run(context.Background(), []string{"work", "note", "T-1", "--bogus"}, &out, &bytes.Buffer{})
	if !strings.Contains(out.String(), "--flag=value") {
		t.Fatalf("hint names no --flag=value form: %s", out.String())
	}
}
