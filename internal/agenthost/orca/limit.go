package orca

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
)

// The usage-limit read, ported from refusedOnly in alpha's
// scripts/sdlc/usage-limit.ts and readRefusal in run-refusals.ts. A Claude
// session whose agent wrote only API errors, one of them a usage limit,
// did no work and never will before the reset. Orca's quota reading misses
// it when Claude Code is proxied (quota.go), so the transcript is the one
// reading that holds in every setup.
//
// The fields are Claude Code's mechanism, measured in alpha on 2026-10-12:
// an assistant record with isApiErrorMessage true, and either error
// rate_limit or a text that names a limit. The reset is in
// apiErrorParams.rate_limit_info.resetsAt as epoch seconds, or in an API
// key's text as "regain access on 2026-10-01 at 00:00 UTC". A field that
// moves makes this read no limit, which is the reading before this file.
// Every unknown answers "no limit", so a fault here never invents one.

// onlyBytes is the largest transcript a refused run holds. Measured in
// alpha on 2026-10-12 over 603 limit runs: each held one transcript of
// 148,382 to 197,914 bytes, the prompt and attachments Claude Code writes
// before the model answers. A larger transcript holds work.
const onlyBytes = 512 << 10

var (
	limitWord   = regexp.MustCompile(`(?i)\blimit`)
	regainUTC   = regexp.MustCompile(`regain access on (\d{4}-\d{2}-\d{2}) at (\d{2}:\d{2}) UTC`)
	statusInAPI = regexp.MustCompile(`^API Error: (\d{3})\b`)
)

// limitRun is the newest usage limit of a session whose every transcript
// holds only API refusals, or nil. One transcript that worked, or a child
// agent that ran, makes the session one that worked.
func limitRun(ts []transcript, dir string) *agenthost.Limit {
	var lim *agenthost.Limit
	for _, t := range ts {
		l, ok := refusedOnly(t.text, filepath.Clean(dir))
		if !ok || !t.childNewest.IsZero() {
			return nil
		}
		if l != nil && (lim == nil || l.At.After(lim.At)) {
			lim = l
		}
	}
	return lim
}

// refusedOnly reads one Claude transcript. ok is true when the agent wrote
// at least one usage limit or server error, every assistant record is an
// API error, and every working directory is inside dir. The limit is the
// newest one, or nil when the agent wrote only server errors.
func refusedOnly(text []byte, dir string) (*agenthost.Limit, bool) {
	if len(text) > onlyBytes {
		return nil, false
	}
	var lim *agenthost.Limit
	refused := false
	for line := range bytes.Lines(text) {
		var r map[string]any
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if cwd := str(r["cwd"]); cwd != "" && !inside(cwd, dir) {
			return nil, false
		}
		if str(r["type"]) != "assistant" {
			continue
		}
		if r["isApiErrorMessage"] != true {
			return nil, false
		}
		msg := textOf(r)
		at, _ := stamp(str(r["timestamp"]))
		switch {
		case isLimit(r, msg):
			lim = &agenthost.Limit{Message: msg, At: at, ResetAt: resetOf(r, msg)}
			refused = true
		case isServerError(r, msg):
			refused = true
		}
	}
	return lim, refused
}

// textOf joins the text of one assistant record's content.
func textOf(r map[string]any) string {
	m, _ := r["message"].(map[string]any)
	content, _ := m["content"].([]any)
	var parts []string
	for _, c := range content {
		if c, ok := c.(map[string]any); ok {
			parts = append(parts, str(c["text"]))
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func isLimit(r map[string]any, msg string) bool {
	return str(r["error"]) == "rate_limit" || limitWord.MatchString(msg)
}

// isServerError reads a 5xx status, or a type the server owns when the
// record states no status.
func isServerError(r map[string]any, msg string) bool {
	if status, ok := statusOf(r, msg); ok {
		return status >= 500 && status <= 599
	}
	e := str(r["error"])
	return e == "server_error" || e == "overloaded_error"
}

// isTransient reads a server error that is no usage limit: a 5xx, 529 or
// overloaded_error. Claude Code gives up on it once its own retries ran
// out, and the same request can succeed later (F-29).
func isTransient(r map[string]any) bool {
	msg := textOf(r)
	return !isLimit(r, msg) && isServerError(r, msg)
}

func statusOf(r map[string]any, msg string) (int, bool) {
	if f, ok := r["apiErrorStatus"].(float64); ok {
		return int(f), true
	}
	if m := statusInAPI.FindStringSubmatch(msg); m != nil {
		n, err := strconv.Atoi(m[1])
		return n, err == nil
	}
	return 0, false
}

// resetOf reads the reset one limit record states, or zero.
func resetOf(r map[string]any, msg string) time.Time {
	params, _ := r["apiErrorParams"].(map[string]any)
	info, _ := params["rate_limit_info"].(map[string]any)
	if s, ok := info["resetsAt"].(float64); ok && s > 0 {
		return time.UnixMilli(int64(s * 1000))
	}
	if m := regainUTC.FindStringSubmatch(msg); m != nil {
		if t, err := time.Parse("2006-01-02 15:04", m[1]+" "+m[2]); err == nil {
			return t
		}
	}
	return time.Time{}
}

// refusalOf names the API error one assistant record holds, so a kept
// session's reason tells a usage limit from a server error.
func refusalOf(r map[string]any) string {
	msg := textOf(r)
	switch {
	case isLimit(r, msg):
		return "a usage limit"
	case isServerError(r, msg):
		status, ok := statusOf(r, msg)
		if !ok {
			return "a server error (" + safeStatus(str(r["error"])) + ")"
		}
		return fmt.Sprintf("a server error (HTTP %d %s)", status, safeStatus(str(r["error"])))
	}
	return "an API error"
}

// limitReason is the evidence that ended a limited session.
func limitReason(l *agenthost.Limit) string {
	reset := "the message names no reset time"
	if !l.ResetAt.IsZero() {
		reset = "it resets " + l.ResetAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("its agent wrote only a usage limit and no work, %q, and %s", l.Message, reset)
}
