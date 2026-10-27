package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/sched"
)

// C-bug-1 (F-41): while a run_start_failed alert is open, a repeat of the
// same failure counts up on that alert and rings no one. The entry still
// shows the count. Only a failure after the answer opens a new alert, and
// that one rings.
func TestStartFailedRepeatWhileOpenRingsNoOne(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	alerts := config.StoreAlerter(w.srv.Store)
	failed := config.Alert{Project: "alpha", Kind: sched.AlertStartFailed, DedupeKey: sched.AlertStartFailed + ":alpha/implementer",
		Subject: "implementer run r-1 did not start",
		Body:    "sched: read the head of main for project alpha: github: git fetch: exit status 128: remote: Repository not found."}
	if err := alerts.Raise(ctx, failed); err != nil {
		t.Fatal(err)
	}
	env, _ := w.coordinator("inbox", "wait")
	cursor := env["data"].(map[string]any)["cursor"].(string)
	var entry string
	for _, e := range w.entries("") {
		if e["status"] == sched.AlertStartFailed {
			entry = e["entry"].(string)
		}
	}
	if entry == "" {
		t.Fatal("the first failure opened no alert")
	}

	for i := 2; i <= 3; i++ {
		w.clock.Advance(time.Minute)
		failed.Subject = fmt.Sprintf("implementer run r-%d did not start", i)
		if err := alerts.Raise(ctx, failed); err != nil {
			t.Fatal(err)
		}
	}
	if env, code := w.coordinator("inbox", "wait", "--since", cursor); code != 4 {
		t.Fatalf("a repeat of the open run_start_failed alert rang the coordinator: %d %v", code, env)
	}
	var open []map[string]any
	for _, e := range w.entries("") {
		if e["status"] == sched.AlertStartFailed {
			open = append(open, e)
		}
	}
	if len(open) != 1 || open[0]["entry"] != entry || !strings.Contains(open[0]["text"].(string), "Raised 3 times.") {
		t.Fatalf("open run_start_failed alerts = %v, want one, %s, raised 3 times", open, entry)
	}

	if _, code := w.coordinator("inbox", "answer", entry, "--body", "F-39 holds; the repository is renamed"); code != 0 {
		t.Fatal("the answer was refused")
	}
	env, _ = w.coordinator("inbox", "wait", "--since", cursor)
	cursor = env["data"].(map[string]any)["cursor"].(string)
	w.clock.Advance(time.Minute)
	if err := alerts.Raise(ctx, failed); err != nil {
		t.Fatal(err)
	}
	env, code := w.coordinator("inbox", "wait", "--since", cursor)
	es, _ := env["data"].(map[string]any)["entries"].([]any)
	if code != 0 || len(es) != 1 || es[0].(map[string]any)["entry"] == entry {
		t.Fatalf("a failure after the answer did not open and ring a new alert: %d %v", code, env)
	}
}
