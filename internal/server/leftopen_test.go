package server

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/store"
)

// C26 in part: the work a previous coordinator of alpha left open names
// alpha's unanswered question and its open room. An entry of beta, and a
// question about it, are not alpha's.
func TestC26TheLeftOpenListNamesTheProjectsQuestionsAndRooms(t *testing.T) {
	w := newHumanWorldOf(t, "", "iantanwx/alpha")
	ctx := context.Background()
	for _, p := range []string{"alpha", "beta"} {
		if err := config.StoreAlerter(w.srv.Store).Raise(ctx, config.Alert{Project: p, Kind: sched.AlertRunUnknown, DedupeKey: "u-" + p,
			Subject: "a run's liveness is unknown"}); err != nil {
			t.Fatal(err)
		}
	}
	alerts := map[string]string{}
	for _, e := range w.entries("") {
		if e["kind"] == "alert" {
			alerts[e["project"].(string)] = e["entry"].(string)
		}
	}
	questions := map[string]string{}
	for p, entry := range alerts {
		env, code := w.coordinator("inbox", "prepare", "--body", "look at the run? options: yes, costs an hour; no", "--entry", entry)
		if code != 0 {
			t.Fatalf("inbox prepare = %d %v", code, env)
		}
		questions[p] = env["data"].(map[string]any)["entry"].(string)
	}
	room := "room/" + w.openRoom("brainstorm", "", "the operator wants widgets")["room"].(string)
	// Each entry rang already, so only the question and the room are left
	// open of alpha's.
	if err := w.srv.Store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(`DELETE FROM schedule WHERE kind = 'home_notify' AND project = 'alpha'`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO schedule (project, kind, due_at, created_at, updated_at) VALUES ('alpha', 'home_notify', 9000000000000, 0, 0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	got, err := w.srv.leftOpen(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(got))
	for i, line := range got {
		ids[i], _, _ = strings.Cut(line, " ")
	}
	slices.Sort(ids)
	want := []string{questions["alpha"], room}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("left open of alpha = %q, want %q", got, want)
	}
}
