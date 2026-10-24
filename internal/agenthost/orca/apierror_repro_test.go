package orca_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
)

// C-bug-1: a Claude session that did work, then ended its turn on a
// transient model API error (503, 529, overloaded), does not stay kept
// forever. Past the quiet period, Ended reports it ended, so the server can
// resume it or settle it, and its slot frees.
func TestEndedClaudeTransientAPIErrorAfterWorkDoesNotKeepForever(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 24, 12, 54, 0, 0, time.UTC)
	cases := map[string]func(d string, at time.Time) map[string]any{
		"503": func(d string, at time.Time) map[string]any {
			return refusal(d, at, "API Error: 503 All accounts are temporarily unavailable", "server_error", map[string]any{"apiErrorStatus": 503})
		},
		"529 overloaded": func(d string, at time.Time) map[string]any {
			return refusal(d, at, `API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, "server_error", map[string]any{"apiErrorStatus": 529})
		},
	}
	for name, apiError := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			s := w.start(agenthost.Claude)
			d := w.checkout
			w.claudeTranscript("s1.jsonl",
				prompt(d, t0.Add(time.Second)),
				assistant(d, "m", t0.Add(2*time.Second)),
				apiError(d, t0.Add(3*time.Second)),
				turnEnd(d, t0.Add(4*time.Second)))
			// Two hours and twenty minutes on, as in F-29: far past the
			// 30-minute quiet period.
			w.now = t0.Add(140 * time.Minute)
			r, err := w.host.Ended(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			if !r.Ended {
				t.Fatalf("Ended = %+v; a session idle at its prompt on %s for 2h20m is kept forever", r, name)
			}
		})
	}
}

// C-bug-1: the report of a turn that ended on a transient API error names
// the error and its time at once, inside the quiet period, so the server
// can resume the session before its run budget. Past the quiet period the
// session ends with the API-error cause. First is the first error after
// the agent's last work, so a retry that failed again keeps it, and work
// between two errors moves it. A usage limit and a client error are no
// transient error: they keep today's reading.
func TestEndedClaudeReportsTransientAPIError(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 24, 12, 54, 0, 0, time.UTC)
	e503 := func(d string, at time.Time) map[string]any {
		return refusal(d, at, "API Error: 503 All accounts are temporarily unavailable", "server_error", map[string]any{"apiErrorStatus": 503})
	}
	overloaded := func(d string, at time.Time) map[string]any {
		return refusal(d, at, "API Error: Overloaded", "overloaded_error", nil)
	}
	cases := []struct {
		name    string
		records func(d string) []map[string]any
		after   time.Duration
		ended   bool
		status  int
		first   time.Time
		at      time.Time
		reason  string
	}{
		{"503 inside the quiet period", func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), assistant(d, "m", t0.Add(2*time.Second)),
				e503(d, t0.Add(3*time.Second)), turnEnd(d, t0.Add(4*time.Second))}
		}, time.Minute, false, 503, t0.Add(3 * time.Second), t0.Add(3 * time.Second), "server error (HTTP 503"},
		{"503 past the quiet period", func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), assistant(d, "m", t0.Add(2*time.Second)),
				e503(d, t0.Add(3*time.Second)), turnEnd(d, t0.Add(4*time.Second))}
		}, 31 * time.Minute, true, 503, t0.Add(3 * time.Second), t0.Add(3 * time.Second), "server error (HTTP 503"},
		{"overloaded with no status", func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), overloaded(d, t0.Add(2*time.Second)), turnEnd(d, t0.Add(3*time.Second))}
		}, time.Minute, false, 0, t0.Add(2 * time.Second), t0.Add(2 * time.Second), "overloaded_error"},
		{"a resume that failed again keeps the first error", func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), assistant(d, "m", t0.Add(2*time.Second)),
				e503(d, t0.Add(3*time.Second)), turnEnd(d, t0.Add(4*time.Second)),
				prompt(d, t0.Add(5*time.Minute)), e503(d, t0.Add(6*time.Minute)), turnEnd(d, t0.Add(6*time.Minute+time.Second))}
		}, 7 * time.Minute, false, 503, t0.Add(3 * time.Second), t0.Add(6 * time.Minute), "server error"},
		{"work between two errors moves the first error", func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), e503(d, t0.Add(2*time.Second)), turnEnd(d, t0.Add(3*time.Second)),
				prompt(d, t0.Add(5*time.Minute)), assistant(d, "m", t0.Add(6*time.Minute)),
				e503(d, t0.Add(7*time.Minute)), turnEnd(d, t0.Add(7*time.Minute+time.Second))}
		}, 8 * time.Minute, false, 503, t0.Add(7 * time.Minute), t0.Add(7 * time.Minute), "server error"},
		{"a usage limit after work is no transient error", func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), assistant(d, "m", t0.Add(2*time.Second)),
				refusal(d, t0.Add(3*time.Second), "You've hit your weekly limit", "rate_limit", map[string]any{"apiErrorStatus": 429}),
				turnEnd(d, t0.Add(4*time.Second))}
		}, 3 * time.Hour, false, 0, time.Time{}, time.Time{}, "usage limit"},
		{"a client error is no transient error", func(d string) []map[string]any {
			return []map[string]any{prompt(d, t0.Add(time.Second)), assistant(d, "m", t0.Add(2*time.Second)),
				refusal(d, t0.Add(3*time.Second), "API Error: 400 invalid request", "invalid_request", map[string]any{"apiErrorStatus": 400}),
				turnEnd(d, t0.Add(4*time.Second))}
		}, 3 * time.Hour, false, 0, time.Time{}, time.Time{}, "an API error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			s := w.start(agenthost.Claude)
			w.claudeTranscript("s1.jsonl", c.records(w.checkout)...)
			w.now = t0.Add(c.after)
			r, err := w.host.Ended(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			if r.Ended != c.ended || !strings.Contains(r.Reason, c.reason) {
				t.Fatalf("Ended = %+v; want ended %v with a reason that names %q", r, c.ended, c.reason)
			}
			if c.first.IsZero() {
				if r.APIError != nil {
					t.Fatalf("APIError = %+v; want none", *r.APIError)
				}
				return
			}
			if r.APIError == nil {
				t.Fatalf("Ended = %+v; want a transient API error", r)
			}
			e := *r.APIError
			if e.Status != c.status || !e.First.Equal(c.first) || !e.At.Equal(c.at) || e.Message == "" {
				t.Fatalf("APIError = %+v; want status %d, first %s, at %s", e, c.status, c.first, c.at)
			}
			if c.ended && (r.Cause != agenthost.EndAPIError || r.Since.Before(c.at)) {
				t.Fatalf("Ended = %+v; want cause %s since the last turn", r, agenthost.EndAPIError)
			}
		})
	}
}
