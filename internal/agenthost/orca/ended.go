package orca

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
)

// Ended applies KTD8. A session ended when orca proves its process exited,
// or when every transcript of the session shows its last turn ended with
// nothing pending, and the session then wrote nothing for the quiet period.
// A Claude session whose agent wrote only a usage limit ends at once
// (limit.go): it did no work, and it can do none before the reset.
// Every doubt answers "not ended": Unknown liveness, an unreadable
// transcript, a transcript of another checkout, a tool call still running.
//
// The transcript fields are the harness vendors' mechanism, measured in
// alpha (agent-finished.ts, codex-finished.ts). A field that moves makes
// this answer "not ended", which keeps the session.
func (h *Host) Ended(ctx context.Context, s agenthost.Session) (agenthost.EndReport, error) {
	st, err := h.Settle(ctx, s.ID)
	if err != nil {
		return agenthost.EndReport{Reason: "liveness unknown: " + err.Error(), Settlement: &st}, err
	}
	rep, err := h.ended(s, st)
	rep.Settlement = &st
	return rep, err
}

// ended is Ended after a liveness read that succeeded.
func (h *Host) ended(s agenthost.Session, st agenthost.Settlement) (agenthost.EndReport, error) {
	now := h.cfg.Now()
	if st.Liveness == agenthost.Exited {
		return agenthost.EndReport{Ended: true, Cause: agenthost.EndExited, Since: now, Reason: st.Reason}, nil
	}
	ts, err := h.transcripts(s)
	if err != nil {
		return agenthost.EndReport{Reason: err.Error()}, err
	}
	if len(ts) == 0 {
		return agenthost.EndReport{Reason: "no transcript of this session names its checkout"}, nil
	}
	if s.Harness == agenthost.Claude {
		if lim := limitRun(ts, s.Checkout); lim != nil {
			since := lim.At
			if since.IsZero() || since.After(now) {
				since = now
			}
			return agenthost.EndReport{Ended: true, Cause: agenthost.EndLimit, Since: since, Limit: lim, Reason: limitReason(lim)}, nil
		}
	}
	var since time.Time
	for _, t := range ts {
		f := finished(s.Harness, t.text, s.Checkout)
		if !f.finished {
			return agenthost.EndReport{Reason: filepath.Base(t.path) + ": " + f.reason}, nil
		}
		since = latest(since, f.since, t.childNewest)
	}
	if since.After(now) {
		return agenthost.EndReport{Reason: "it waits for a wake or a monitor that fires at " + since.UTC().Format(time.RFC3339)}, nil
	}
	idle := now.Sub(since)
	if idle < h.cfg.QuietPeriod {
		return agenthost.EndReport{Reason: fmt.Sprintf("its last turn ended %s ago, and the quiet period is %s",
			idle.Round(time.Second), h.cfg.QuietPeriod)}, nil
	}
	return agenthost.EndReport{
		Ended: true, Cause: agenthost.EndQuiet, Since: since,
		Reason: fmt.Sprintf("its last turn ended with nothing pending, and it stayed quiet for %s", idle.Round(time.Second)),
	}, nil
}

func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// ---- Transcript discovery ----

// maxTranscript bounds one read. A larger file reads as unreadable.
const maxTranscript = 64 << 20

// startSkew admits a transcript whose newest record is this much older than
// the recorded start, for clock skew between the server and the harness.
const startSkew = time.Minute

type transcript struct {
	path        string
	text        []byte
	newest      time.Time
	childNewest time.Time
}

// TranscriptName is Claude Code's directory name for a working directory.
// Ported from transcriptName in alpha's agent-sessions.ts.
func TranscriptName(path string) string {
	return nonAlnum.ReplaceAllString(path, "-")
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// transcripts reads every transcript of the session: the harness's files
// that name the checkout and that wrote at or after the start.
func (h *Host) transcripts(s agenthost.Session) ([]transcript, error) {
	var all []transcript
	var err error
	switch s.Harness {
	case agenthost.Claude:
		all, err = h.claudeTranscripts(s.Checkout)
	case agenthost.Codex:
		all, err = h.codexTranscripts(s.Checkout, s.StartedAt)
	default:
		return nil, fmt.Errorf("%w: harness %q has no transcript reader", agenthost.ErrBadSpec, s.Harness)
	}
	if err != nil {
		return nil, err
	}
	cut := s.StartedAt.Add(-startSkew)
	return slices.DeleteFunc(all, func(t transcript) bool { return t.newest.Before(cut) }), nil
}

func (h *Host) claudeTranscripts(checkout string) ([]transcript, error) {
	dir := filepath.Join(h.cfg.ClaudeRoot, TranscriptName(filepath.Clean(checkout)))
	names, err := jsonl(dir)
	if err != nil {
		return nil, err
	}
	var out []transcript
	for _, n := range names {
		t, err := readTranscript(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		kids, err := jsonl(filepath.Join(dir, strings.TrimSuffix(n, ".jsonl"), "subagents"))
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			c, err := readTranscript(filepath.Join(dir, strings.TrimSuffix(n, ".jsonl"), "subagents", k))
			if err != nil {
				return nil, err
			}
			t.childNewest = latest(t.childNewest, c.newest)
		}
		out = append(out, t)
	}
	return out, nil
}

// codexTranscripts reads the rollouts under CodexRoot/YYYY/MM/DD from the
// day before the start to today whose session_meta names the checkout.
func (h *Host) codexTranscripts(checkout string, started time.Time) ([]transcript, error) {
	checkout = filepath.Clean(checkout)
	var out []transcript
	now := h.cfg.Now()
	day := started.Add(-24 * time.Hour)
	if now.Sub(day) > 31*24*time.Hour {
		day = now.Add(-31 * 24 * time.Hour)
	}
	seen := map[string]bool{}
	for ; !day.After(now.Add(24 * time.Hour)); day = day.Add(24 * time.Hour) {
		for _, loc := range []*time.Location{time.Local, time.UTC} {
			dir := filepath.Join(h.cfg.CodexRoot, day.In(loc).Format("2006/01/02"))
			if seen[dir] {
				continue
			}
			seen[dir] = true
			names, err := jsonl(dir)
			if err != nil {
				return nil, err
			}
			for _, n := range names {
				path := filepath.Join(dir, n)
				if !codexNames(path, checkout) {
					continue
				}
				t, err := readTranscript(path)
				if err != nil {
					return nil, err
				}
				out = append(out, t)
			}
		}
	}
	return out, nil
}

// codexNames reads only the first record of a rollout, session_meta, and
// says whether it names the checkout.
func codexNames(path, checkout string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	line, err := bufio.NewReaderSize(f, 1<<20).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	var r struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	return json.Unmarshal(line, &r) == nil && r.Type == "session_meta" && filepath.Clean(r.Payload.Cwd) == checkout
}

// jsonl lists the .jsonl files of dir in name order. A missing directory
// holds none.
func jsonl(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read transcripts in %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func readTranscript(path string) (transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return transcript{}, fmt.Errorf("read transcript %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	text, err := io.ReadAll(io.LimitReader(f, maxTranscript+1))
	if err != nil {
		return transcript{}, fmt.Errorf("read transcript %s: %w", path, err)
	}
	if len(text) > maxTranscript {
		return transcript{}, fmt.Errorf("transcript %s is larger than %d bytes", path, maxTranscript)
	}
	t := transcript{path: path, text: text}
	for line := range bytes.Lines(text) {
		var r struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(line, &r) == nil {
			if at, ok := stamp(r.Timestamp); ok {
				t.newest = latest(t.newest, at)
			}
		}
	}
	return t, nil
}

func stamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

// ---- The finished rule ----

type finish struct {
	finished bool
	since    time.Time
	reason   string
}

func finished(h agenthost.Harness, text []byte, dir string) finish {
	if h == agenthost.Codex {
		return codexFinished(text, filepath.Clean(dir))
	}
	return claudeFinished(text, filepath.Clean(dir))
}

func inside(cwd, dir string) bool {
	return cwd == dir || strings.HasPrefix(cwd, dir+"/")
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

var (
	killedStatus = regexp.MustCompile(`<status>killed</status>`)
	taskNotice   = regexp.MustCompile(`<task-id>([A-Za-z0-9_-]+)</task-id>`)
	continuing   = regexp.MustCompile(`(?i)continuing automatically`)
	resetWord    = regexp.MustCompile(`(?i)\breset\b`)
)

type claudeScan struct {
	newest        time.Time
	last          map[string]any
	lastAssistant map[string]any
	started       []string
	notified      map[string]bool
	deadline      time.Time
	persistent    bool
	resuming      bool
	resumes       bool
	outside       string
	inside        bool
}

// claudeFinished ports finishedSince in alpha's agent-finished.ts. All six
// must hold: the newest turn record is system/turn_duration; it holds no
// pending background agent; the last assistant record is no API error and
// no usage-limit resume is queued; every background command it started has
// a notification or a successful TaskStop; it started no persistent
// monitor; and every working directory it names is inside the checkout.
// The answer is the newest of: every timestamp, the latest wake, and the
// latest monitor timeout.
func claudeFinished(text []byte, dir string) finish {
	s := claudeScan{notified: map[string]bool{}}
	for line := range bytes.Lines(text) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r map[string]any
		if err := json.Unmarshal(line, &r); err != nil {
			var v any
			if json.Unmarshal(line, &v) == nil {
				continue // valid JSON that is not an object
			}
			return finish{reason: "a line of its transcript does not parse, so a write may be in progress"}
		}
		s.read(r, dir)
	}
	if why := s.keeps(); why != "" {
		return finish{reason: why}
	}
	return finish{finished: true, since: latest(s.newest, s.deadline)}
}

// strs calls f on every string value inside v.
func strs(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case map[string]any:
		for _, e := range x {
			strs(e, f)
		}
	case []any:
		for _, e := range x {
			strs(e, f)
		}
	}
}

func (s *claudeScan) read(r map[string]any, dir string) {
	if cwd := str(r["cwd"]); cwd != "" {
		in := inside(cwd, dir)
		s.inside = s.inside || in
		if !in {
			s.outside = cwd
		}
	}
	typ := str(r["type"])
	if typ == "queue-operation" || typ == "user" {
		// Match decoded strings, not the raw line: a writer may escape '<'
		// as \u003c, and the raw line would then hide the notice.
		strs(r, func(v string) {
			for _, m := range taskNotice.FindAllStringSubmatch(v, -1) {
				s.notified[m[1]] = true
			}
		})
	}
	at, ok := stamp(str(r["timestamp"]))
	killed := typ == "queue-operation" && str(r["operation"]) == "enqueue" && killedStatus.MatchString(str(r["content"]))
	if !ok || killed {
		return
	}
	s.newest = latest(s.newest, at)
	s.result(r, at)
	sub := str(r["subtype"])
	if typ != "pr-link" && (typ != "system" || sub != "away_summary") {
		s.last = r
	}
	if typ == "system" && sub == "informational" {
		content := str(r["content"])
		s.resuming = s.resuming || (continuing.MatchString(content) && !resetWord.MatchString(content))
	}
	if typ == "system" && sub == "turn_duration" {
		s.resumes = s.resuming
		s.resuming = false
	}
	if typ == "assistant" {
		s.lastAssistant = r
	}
}

func (s *claudeScan) result(r map[string]any, at time.Time) {
	out, ok := r["toolUseResult"].(map[string]any)
	if !ok {
		return
	}
	if task := str(out["backgroundTaskId"]); task != "" {
		s.started = append(s.started, task)
	}
	if stopped, ok := out["task_id"].(string); ok && strings.HasPrefix(str(out["message"]), "Successfully stopped task") {
		s.notified[stopped] = true
	}
	if wake, ok := out["scheduledFor"].(float64); ok {
		s.deadline = latest(s.deadline, time.UnixMilli(int64(wake)))
	}
	if _, ok := out["taskId"].(string); ok {
		if timeout, ok := out["timeoutMs"].(float64); ok {
			s.persistent = s.persistent || out["persistent"] == true
			s.deadline = latest(s.deadline, at.Add(time.Duration(timeout)*time.Millisecond))
		}
	}
}

func (s *claudeScan) keeps() string {
	if s.outside != "" || !s.inside {
		if s.outside == "" {
			return "its transcript names no working directory in this checkout"
		}
		return "its transcript names another directory, " + s.outside
	}
	if s.last == nil || str(s.last["type"]) != "system" || str(s.last["subtype"]) != "turn_duration" {
		return "its last turn has not ended"
	}
	if n, ok := s.last["pendingBackgroundAgentCount"].(float64); ok && n > 0 {
		return fmt.Sprintf("its last turn ended with %d background agent(s) still at work", int(n))
	}
	if s.lastAssistant != nil && s.lastAssistant["isApiErrorMessage"] == true {
		return "its last turn ended on " + refusalOf(s.lastAssistant) + ", and Claude Code may resume it"
	}
	if s.resumes {
		return "its last turn ended on a usage limit, and Claude Code may resume it"
	}
	var waiting []string
	for _, t := range s.started {
		if !s.notified[t] {
			waiting = append(waiting, t)
		}
	}
	if len(waiting) > 0 {
		return "background command " + strings.Join(waiting, ", ") + " has sent no notification"
	}
	if s.persistent {
		return "it started a persistent monitor, which can wake it at any time"
	}
	return ""
}

// codexFinished ports codexFinished in alpha's codex-finished.ts. The final
// turn must end in task_complete for the turn that task_started opened, in a
// session whose session_meta names the checkout, and every background
// resource the ledger reads must have settled. Any unrecognized record keeps
// the agent.
func codexFinished(text []byte, dir string) finish {
	resources := newCodexResources()
	var session, turn, reason string
	completed := false
	var newest time.Time
	for line := range bytes.Lines(text) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r struct {
			Type      string         `json:"type"`
			Timestamp string         `json:"timestamp"`
			Payload   map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			reason = "a Codex record is incomplete, so a write may be in progress"
			break
		}
		if r.Payload == nil {
			reason = "a Codex record has no readable payload"
			break
		}
		at, ok := stamp(r.Timestamp)
		if !ok {
			reason = "a Codex record has no readable timestamp"
			break
		}
		newest = latest(newest, at)
		p := r.Payload
		cwd := str(p["cwd"])
		if cwd != "" && !inside(cwd, dir) {
			reason = "the Codex rollout names another checkout"
		}
		ptype := str(p["type"])
		switch {
		case r.Type == "session_meta" && cwd == dir:
			session = str(p["id"])
		case r.Type == "event_msg" && ptype == "task_started":
			turn = str(p["turn_id"])
			completed = false
		case r.Type == "event_msg" && ptype == "task_complete":
			completed = turn != "" && str(p["turn_id"]) == turn
		case r.Type == "response_item":
			completed = false
			if ptype != "message" && ptype != "reasoning" && ptype != "agent_message" {
				resources.response(p)
			}
		case r.Type == "turn_context" || r.Type == "world_state" || r.Type == "inter_agent_communication_metadata":
			completed = false
		case r.Type == "event_msg" && ptype == "item_completed":
			completed = false
			resources.item(p["item"], str(p["thread_id"]), session)
		case r.Type == "event_msg" && ptype != "token_count":
			completed = false
			if ptype == "error" || ptype == "turn_aborted" {
				reason = "Codex reported an error or interrupted turn"
			} else if ptype != "thread_settings_applied" {
				reason = "Codex has an unrecognized lifecycle event"
			}
		case r.Type != "event_msg" && r.Type != "token_usage_record":
			reason = "the Codex rollout contains an unrecognized record"
		}
	}
	if reason == "" {
		reason = resources.pending()
	}
	if reason == "" && (session == "" || !completed) {
		reason = "the Codex session has no proven completed final turn"
	}
	if reason != "" {
		return finish{reason: reason}
	}
	return finish{finished: true, since: newest}
}
