package orca_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
)

// refusal is one assistant record that Claude Code marked as an API error,
// shaped as the transcripts on this host hold them on 2026-10-10.
func refusal(dir string, at time.Time, text, kind string, extra map[string]any) map[string]any {
	r := map[string]any{"type": "assistant", "cwd": dir, "timestamp": ts(at), "isApiErrorMessage": true, "error": kind,
		"message": map[string]any{"model": "<synthetic>", "content": []any{map[string]any{"type": "text", "text": text}}}}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func prompt(dir string, at time.Time) map[string]any {
	return map[string]any{"type": "user", "cwd": dir, "timestamp": ts(at), "message": map[string]any{"role": "user", "content": "go"}}
}

// A session whose agent wrote only a usage limit, and no work, ends at
// once with the limit and its reset. It waits for no quiet period, and a
// server error, a run that worked and a transcript of another directory
// are no limit.
func TestEndedClaudeLimitOnly(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	weekly := func(d string, at time.Time) map[string]any {
		return refusal(d, at, "You've hit your weekly limit · resets Oct 24, 3am (Asia/Singapore)", "rate_limit", map[string]any{
			"apiErrorStatus": 429,
			"apiErrorParams": map[string]any{"rate_limit_info": map[string]any{"status": "rejected", "resetsAt": 1793041200, "rateLimitType": "seven_day"}},
		})
	}
	server := func(d string, at time.Time) map[string]any {
		return refusal(d, at, "API Error: 503 upstream connect error", "server_error", map[string]any{"apiErrorStatus": 503})
	}
	cases := []struct {
		name    string
		files   map[string]func(d string) []map[string]any
		limit   string
		resetAt time.Time
		reason  string
	}{
		{"weekly limit with its reset", map[string]func(string) []map[string]any{"s1.jsonl": func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), weekly(d, t0.Add(2*time.Second)), turnEnd(d, t0.Add(3*time.Second))}
		}}, "weekly limit", time.Unix(1793041200, 0), ""},
		{"API key limit names its reset in the text", map[string]func(string) []map[string]any{"s1.jsonl": func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)),
				refusal(d, t0.Add(2*time.Second), "API Error: 400 You have reached your specified API usage limits. You will regain access on 2026-11-01 at 00:00 UTC.", "unknown", nil),
				turnEnd(d, t0.Add(3*time.Second))}
		}}, "specified API usage limits", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), ""},
		{"Fable limit names no reset", map[string]func(string) []map[string]any{"s1.jsonl": func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)),
				refusal(d, t0.Add(2*time.Second), "You've reached your Fable limit. Run /usage-credits to continue.", "rate_limit", nil),
				turnEnd(d, t0.Add(3*time.Second))}
		}}, "Fable limit", time.Time{}, ""},
		{"a limit outranks a server error", map[string]func(string) []map[string]any{"s1.jsonl": func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), server(d, t0.Add(2*time.Second)), weekly(d, t0.Add(3*time.Second)), turnEnd(d, t0.Add(4*time.Second))}
		}}, "weekly limit", time.Unix(1793041200, 0), ""},
		{"a run that worked before its limit is no limit run", map[string]func(string) []map[string]any{"s1.jsonl": func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), assistant(d, "m", t0.Add(2*time.Second)), weekly(d, t0.Add(3*time.Second)), turnEnd(d, t0.Add(4*time.Second))}
		}}, "", time.Time{}, "usage limit"},
		{"a server error alone is no limit", map[string]func(string) []map[string]any{"s1.jsonl": func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), server(d, t0.Add(2*time.Second)), turnEnd(d, t0.Add(3*time.Second))}
		}}, "", time.Time{}, "server error"},
		{"one session that worked makes the run a run that worked", map[string]func(string) []map[string]any{
			"s1.jsonl": func(d string) []map[string]any {
				return []map[string]any{prompt(d, t0.Add(time.Second)), weekly(d, t0.Add(2*time.Second)), turnEnd(d, t0.Add(3*time.Second))}
			},
			"s2.jsonl": func(d string) []map[string]any {
				return []map[string]any{prompt(d, t0.Add(4*time.Second)), assistant(d, "m", t0.Add(5*time.Second)), turnEnd(d, t0.Add(6*time.Second))}
			},
		}, "", time.Time{}, ""},
		{"a transcript of another directory is no limit", map[string]func(string) []map[string]any{"s1.jsonl": func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), weekly("/elsewhere", t0.Add(2*time.Second)), turnEnd(d, t0.Add(3*time.Second))}
		}}, "", time.Time{}, "another directory"},
		{"a transcript larger than a refused run holds is work", map[string]func(string) []map[string]any{"s1.jsonl": func(d string) []map[string]any {
			big := prompt(d, t0.Add(time.Second))
			big["message"] = map[string]any{"role": "user", "content": strings.Repeat("x", 600<<10)}
			return []map[string]any{big, weekly(d, t0.Add(2*time.Second)), turnEnd(d, t0.Add(3*time.Second))}
		}}, "", time.Time{}, "usage limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			s := w.start(agenthost.Claude)
			for name, records := range c.files {
				w.claudeTranscript(name, records(w.checkout)...)
			}
			// One minute on: far inside the quiet period.
			w.now = t0.Add(time.Minute)
			r, err := w.host.Ended(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			if c.limit == "" {
				if r.Ended || r.Limit != nil {
					t.Fatalf("Ended = %+v; want a session that is kept, with no limit", r)
				}
				if !strings.Contains(r.Reason, c.reason) {
					t.Fatalf("reason %q lacks %q", r.Reason, c.reason)
				}
				return
			}
			if !r.Ended || r.Cause != agenthost.EndLimit || r.Limit == nil {
				t.Fatalf("Ended = %+v; want ended by a usage limit", r)
			}
			if !strings.Contains(r.Limit.Message, c.limit) || !r.Limit.ResetAt.Equal(c.resetAt) {
				t.Fatalf("limit = %+v; want %q resetting %s", *r.Limit, c.limit, c.resetAt)
			}
			if r.Limit.At.IsZero() || r.Since.After(w.now) {
				t.Fatalf("limit at %s, since %s", r.Limit.At, r.Since)
			}
		})
	}
}
