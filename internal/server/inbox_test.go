package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

// fileDecision files a decision as the coordinator and returns its id.
// An identical call replays its first answer, so each title differs.
func (w *humanWorld) fileDecision(title string, args ...string) string {
	w.t.Helper()
	env, code := w.coordinator(append([]string{"decision", "file", "--project", "alpha", "--title", title, "--trigger", "the first paying user"}, args...)...)
	if code != 0 {
		w.t.Fatalf("decision file = %d %v", code, env)
	}
	return env["data"].(map[string]any)["decision"].(string)
}

// The decision verbs keep the cases of decision-states.test.ts (alpha):
// a filed decision waits in pending and is no inbox entry; only fire sends
// it to a person; a waiting decision closes in one move; moot is no
// answer, and only an answer reads as done.
func TestDecisionVerbsKeepTheDecisionStates(t *testing.T) {
	w := newHumanWorld(t, "")
	d := w.fileDecision("which store", "--by", "2000-01-01", "--body", "the evidence")
	if it := w.show(d); it.Status != machine.DecisionPending || it.Fact("trigger") != "the first paying user" {
		t.Fatalf("filed = %s %v", it.Status, it.Facts)
	}
	if entryByID(w.entries(""), "decision/alpha/"+d) != nil {
		t.Fatal("a pending decision waits on a person")
	}
	moot := w.fileDecision("which queue")
	if env, code := w.coordinator("decision", "moot", moot, "--project", "alpha"); code != 0 || w.show(moot).Status != machine.Canceled {
		t.Fatalf("moot from pending = %d %v", code, env)
	}
	if _, code := w.coordinator("decision", "fire", d, "--project", "alpha"); code != 0 {
		t.Fatal("fire failed")
	}
	e := entryByID(w.entries("person"), "decision/alpha/"+d)
	if e == nil || !strings.Contains(e["text"].(string), "<!--sdlc:agent run=coord-1-->\nthe first paying user\n<!--/sdlc:agent-->") {
		t.Fatalf("fired decision entry = %v", e)
	}
	if env, code := w.coordinator("inbox", "answer", "decision/alpha/"+d, "--body", "postgres"); code != 0 {
		t.Fatalf("answer = %d %v", code, env)
	}
	if it := w.show(d); it.Status != machine.Done || it.Fact(machine.FactAnswer) != "postgres" {
		t.Fatalf("answered = %s %v", it.Status, it.Facts)
	}
	dropped := w.fileDecision("which cache")
	w.coordinator("decision", "fire", dropped, "--project", "alpha")
	if env, code := w.coordinator("decision", "settle", dropped, "--project", "alpha", "--dropped"); code != 0 || w.show(dropped).Status != machine.Canceled {
		t.Fatalf("a dropped decision = %d %v, %s", code, env, w.show(dropped).Status)
	}
}

// A --by date fires the decision once on that day. A person who defers it
// by hand is not overruled by the same date.
func TestDecisionTimersFireTheByDateOnce(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	today := w.clock.Now().UTC().Format("2006-01-02")
	d := w.fileDecision("which store", "--by", today)
	later := w.fileDecision("which queue", "--by", w.clock.Now().UTC().Add(72*time.Hour).Format("2006-01-02"))
	if err := w.srv.Decisions.Tick(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if w.show(d).Status != machine.NeedsHuman || w.show(later).Status != machine.DecisionPending {
		t.Fatalf("after the tick: %s, %s", w.show(d).Status, w.show(later).Status)
	}
	if _, code := w.coordinator("decision", "defer", d, "--project", "alpha"); code != 0 {
		t.Fatal("defer failed")
	}
	if err := w.srv.Decisions.Tick(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if w.show(d).Status != machine.DecisionPending {
		t.Fatal("the timer fired the same --by date twice")
	}
	// The job is registered with the scheduler.
	if err := w.srv.Sched.RegisterJob("decision-timers", func(context.Context, string) error { return nil }); err == nil ||
		!strings.Contains(err.Error(), "twice") {
		t.Fatalf("decision-timers is not registered: %v", err)
	}
}

// `inbox defer --until` hides the entry until that time. A deferred
// decision goes back to pending, and decision-timers fires it at that
// time, also across a server restart. The time is in SQLite, and beads
// never holds a deferred status.
func TestInboxDeferResumesAtItsTimeAcrossARestart(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	d := w.fileDecision("which store")
	w.coordinator("decision", "fire", d, "--project", "alpha")
	env, _ := w.coordinator("inbox", "prepare", "--body", "ship on Friday? options: yes, costs a weekend; no, costs a week",
		"--entry", "decision/alpha/"+d)
	q := env["data"].(map[string]any)["entry"].(string)
	until := w.clock.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for _, id := range []string{"decision/alpha/" + d, q} {
		if env, code := w.coordinator("inbox", "defer", id, "--until", until); code != 0 {
			t.Fatalf("defer %s = %d %v", id, code, env)
		}
	}
	if it := w.show(d); it.Status != machine.DecisionPending {
		t.Fatalf("deferred decision = %s", it.Status)
	}
	if es := w.entries(""); entryByID(es, q) != nil || entryByID(es, "decision/alpha/"+d) != nil {
		t.Fatalf("deferred entries show: %v", es)
	}
	if env, _ := w.coordinator("inbox", "defer", q, "--until", "2000-01-01T00:00:00Z"); env["code"] != cli.CodeUsage {
		t.Fatalf("a past --until = %v", env)
	}

	w.restart()
	w.clock.Advance(2 * time.Hour)
	if err := w.srv.Decisions.Tick(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if it := w.show(d); it.Status != machine.NeedsHuman {
		t.Fatalf("the decision did not return: %s", it.Status)
	}
	es := w.entries("person")
	dq, de := entryByID(es, q), entryByID(es, "decision/alpha/"+d)
	if dq == nil || de == nil || dq["link"] != "decision/alpha/"+d {
		t.Fatalf("entries after the defer = %v", es)
	}
	if env, code := w.coordinator("inbox", "answer", q, "--body", "yes"); code != 0 {
		t.Fatalf("answer the question = %d %v", code, env)
	}
	if body, answer, err := w.srv.Inbox.Question(ctx, 1); err != nil || answer != "yes" || !strings.Contains(body, "Friday") {
		t.Fatalf("question = %q %q %v", body, answer, err)
	}
}

// `inbox wait --since` answers exit 4 when nothing new arrives inside the
// wait budget, and answers at once when an entry arrives.
func TestInboxWaitLongPolls(t *testing.T) {
	w := newHumanWorld(t, "")
	env, _ := w.coordinator("inbox", "prepare", "--body", "first")
	if env["exit"] != float64(0) {
		t.Fatalf("prepare = %v", env)
	}
	env, code := w.coordinator("inbox", "wait")
	data := env["data"].(map[string]any)
	cursor, _ := data["cursor"].(string)
	if code != 0 || cursor == "" {
		t.Fatalf("first wait = %d %v", code, env)
	}
	started := time.Now()
	env, code = w.coordinator("inbox", "wait", "--since", cursor)
	if code != 4 || env["data"].(map[string]any)["cursor"] != cursor || time.Since(started) > 5*time.Second {
		t.Fatalf("idle wait = %d %v after %s", code, env, time.Since(started))
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = config.StoreAlerter(w.srv.Store).Raise(context.Background(),
			config.Alert{Kind: sched.AlertRunOverBudget, DedupeKey: "x", Subject: "a run is past its budget"})
		w.srv.Inbox.Changed()
	}()
	env, code = w.coordinator("inbox", "wait", "--since", cursor)
	es, _ := env["data"].(map[string]any)["entries"].([]any)
	if code != 0 || len(es) != 1 || es[0].(map[string]any)["kind"] != "alert" || es[0].(map[string]any)["awaits"] != "coordinator" {
		t.Fatalf("woken wait = %d %v", code, env)
	}
}

// A sign-off waits on a person, and no inbox answer replaces the merge. An
// alert waits on a person only when only the operator can act; an answer
// closes it.
func TestInboxSaysWhoEachEntryWaitsOn(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	ep, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "the PRD", Type: "epic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, ep.ID, beadsx.UpdateSpec{IfStatus: ep.Status, Status: machine.PRDSignoff}); err != nil {
		t.Fatal(err)
	}
	alerts := config.StoreAlerter(w.srv.Store)
	_ = alerts.Raise(ctx, config.Alert{Project: "alpha", Kind: sched.AlertMissingSecret, DedupeKey: "m", Subject: "API_KEY has no value"})
	_ = alerts.Raise(ctx, config.Alert{Project: "alpha", Kind: sched.AlertRunUnknown, DedupeKey: "u", Subject: "a run's liveness is unknown"})
	es := w.entries("")
	sign := entryByID(es, "signoff/alpha/"+ep.ID)
	if sign == nil || sign["awaits"] != "person" || !strings.Contains(sign["text"].(string), "PRD passed review") {
		t.Fatalf("sign-off = %v", sign)
	}
	if env, _ := w.coordinator("inbox", "answer", "signoff/alpha/"+ep.ID, "--body", "ok"); env["code"] != cli.CodeTransitionNotAllowed {
		t.Fatalf("an inbox answer signed off: %v", env)
	}
	byKind := map[string]string{}
	for _, e := range es {
		if e["kind"] == "alert" {
			byKind[e["status"].(string)] = e["awaits"].(string) + " " + e["entry"].(string)
		}
	}
	if !strings.HasPrefix(byKind[sched.AlertMissingSecret], "person ") || !strings.HasPrefix(byKind[sched.AlertRunUnknown], "coordinator ") {
		t.Fatalf("alerts = %v", byKind)
	}
	unknown := strings.Fields(byKind[sched.AlertRunUnknown])[1]
	if _, code := w.coordinator("inbox", "answer", unknown, "--body", "the run was fine"); code != 0 || entryByID(w.entries(""), unknown) != nil {
		t.Fatal("the answered alert stays open")
	}
}

// The operator's shell types a message into the coordinator's terminal,
// and the coordinator itself may not.
func TestCoordinatorSendRingsTheCoordinator(t *testing.T) {
	w := newHumanWorld(t, "")
	if _, err := rpc.EnsureAdminCredential(w.dir); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	env, code, _ := call(t, w.dir, 5*time.Second, nil, "coordinator", "send", "--body", "file three tickets for the review gaps")
	if code != 0 {
		t.Fatalf("coordinator send from the host = %d %v", code, env)
	}
	w.waitNotified("file three tickets for the review gaps")
	if env, _ := w.coordinator("coordinator", "send", "--body", "hello"); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("the coordinator's own send = %v", env)
	}
}
