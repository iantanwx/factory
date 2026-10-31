package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// C-bug-1 (F-65): while an old_stage_names alert is open, a config read
// that raises it again with the same text counts up on that alert and
// rings no one. The coordinator does not answer this alert, because the
// next clean read clears it, so a ring at each read is noise.
func TestOldStageNamesRepeatWhileOpenRingsNoOne(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	alerts := config.StoreAlerter(w.srv.Store)
	renames := []config.Rename{{Source: ".loop/config.yaml", Old: "prd-author", New: config.StageUnattendedBrainstorm}}
	if err := config.ReportRenames(ctx, alerts, "alpha", renames); err != nil {
		t.Fatal(err)
	}
	env, _ := w.coordinator("inbox", "wait")
	cursor := env["data"].(map[string]any)["cursor"].(string)
	var entry string
	for _, e := range w.entries("") {
		if e["status"] == config.AlertOldStageNames {
			entry = e["entry"].(string)
		}
	}
	if entry == "" {
		t.Fatal("the first read opened no old_stage_names alert")
	}

	for range 9 {
		w.clock.Advance(90 * time.Second)
		if err := config.ReportRenames(ctx, alerts, "alpha", renames); err != nil {
			t.Fatal(err)
		}
	}
	if env, code := w.coordinator("inbox", "wait", "--since", cursor); code != 4 {
		t.Fatalf("a repeat of the open old_stage_names alert with the same text rang the coordinator: %d %v", code, env)
	}
	var open []map[string]any
	for _, e := range w.entries("") {
		if e["status"] == config.AlertOldStageNames {
			open = append(open, e)
		}
	}
	if len(open) != 1 || open[0]["entry"] != entry || !strings.Contains(open[0]["text"].(string), "Raised 10 times.") {
		t.Fatalf("open old_stage_names alerts = %v, want one, %s, raised 10 times", open, entry)
	}

	// A clean read clears the alert. A new alert after it rings once.
	if err := config.ReportRenames(ctx, alerts, "alpha", nil); err != nil {
		t.Fatal(err)
	}
	env, _ = w.coordinator("inbox", "wait", "--since", cursor)
	cursor = env["data"].(map[string]any)["cursor"].(string)
	w.clock.Advance(time.Minute)
	if err := config.ReportRenames(ctx, alerts, "alpha", renames); err != nil {
		t.Fatal(err)
	}
	env, code := w.coordinator("inbox", "wait", "--since", cursor)
	es, _ := env["data"].(map[string]any)["entries"].([]any)
	if code != 0 || len(es) != 1 || es[0].(map[string]any)["entry"] == entry {
		t.Fatalf("an old name after the clean read did not open and ring a new alert: %d %v", code, env)
	}
}
