package secrets

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var pctLower = regexp.MustCompile(`%[0-9A-F]{2}`)

func encodings(v string) map[string]string {
	j, _ := json.Marshal(v)
	return map[string]string{
		"plain":          v,
		"base64 std":     base64.StdEncoding.EncodeToString([]byte(v)),
		"base64 raw":     base64.RawStdEncoding.EncodeToString([]byte(v)),
		"base64 url":     base64.URLEncoding.EncodeToString([]byte(v)),
		"base64 url raw": base64.RawURLEncoding.EncodeToString([]byte(v)),
		"base64 offset1": base64.StdEncoding.EncodeToString([]byte("x" + v + "tail")),
		"base64 offset2": base64.RawURLEncoding.EncodeToString([]byte("xy" + v)),
		"hex lower":      hex.EncodeToString([]byte(v)),
		"hex upper":      strings.ToUpper(hex.EncodeToString([]byte(v))),
		"query escape":   url.QueryEscape(v),
		"path escape":    url.PathEscape(v),
		"lower percent":  pctLower.ReplaceAllStringFunc(url.QueryEscape(v), strings.ToLower),
		"json":           string(j[1 : len(j)-1]),
	}
}

func TestRedactorFindsEveryForm(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	stored := "sk_live/51H+xyz<&>\"q\"=?"
	appToken := "ghs_AppInstallationToken0123456789"
	runToken := "loop_run_tok_9a8b7c6d5e4f"
	e.set(t, "alpha", "STRIPE_KEY", stored)

	r := NewRedactor()
	if err := r.Refresh(ctx, e.secrets); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	r.SetLive("github_app_token", appToken)
	r.SetLive("run_token:run-1", runToken)

	for who, v := range map[string]string{"alpha/STRIPE_KEY": stored, "github_app_token": appToken, "run_token:run-1": runToken} {
		for form, enc := range encodings(v) {
			text := `{"log":"before ` + enc + ` after"}`
			ok, names := r.Find(text)
			if !ok || len(names) != 1 || names[0] != who {
				t.Fatalf("%s %s: Find = %v %v", who, form, ok, names)
			}
			out := r.Redact(text)
			if !strings.Contains(out, "[REDACTED:"+who+"]") {
				t.Fatalf("%s %s: Redact did not mark the match", who, form)
			}
			if strings.Contains(out, v) || (len(enc) >= MinRedactLen && strings.Contains(out, enc)) {
				t.Fatalf("%s %s: Redact left the value", who, form)
			}
		}
	}

	// A dropped live token is no longer matched.
	r.DropLive("run_token:run-1")
	if ok, _ := r.Find("x " + runToken); ok {
		t.Fatal("a dropped run token still matched")
	}
	if ok, _ := r.Find("an ordinary log line with nothing secret"); ok {
		t.Fatal("a clean line matched")
	}
}

func TestRedactorSkipsShortValuesAndMergesOverlaps(t *testing.T) {
	r := NewRedactor()
	r.SetLive("SHORT", "abc1234") // below MinRedactLen: not matched by design
	r.SetLive("A", "overlapping_secret_one")
	r.SetLive("B", "secret_one_and_more_text")
	if ok, _ := r.Find("value abc1234 here"); ok {
		t.Fatal("a short value matched")
	}
	out := r.Redact("x overlapping_secret_one_and_more_text y")
	if out != "x [REDACTED:A] y" {
		t.Fatalf("Redact = %q", out)
	}
	ok, names := r.Find("x overlapping_secret_one_and_more_text y")
	if !ok || strings.Join(names, ",") != "A,B" {
		t.Fatalf("Find = %v %v", ok, names)
	}
}

func BenchmarkRedactLogLine(b *testing.B) {
	r := NewRedactor()
	for i := 0; i < 100; i++ {
		r.SetLive("S"+strconv.Itoa(i), "secret_value_number_"+strings.Repeat("z", i))
	}
	line := strings.Repeat("an ordinary log line with ids 1234 and paths /a/b/c ", 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Redact(line)
	}
}
