package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// C15, spec Work 8: every decision move records its text. `decision fire`
// and `decision defer` with no --why, and `decision settle` and
// `decision moot` with no --note, refuse with USAGE and move nothing.
// settle stores its note as answer, and a drop or a moot as close_note. The
// machine refuses an answer move that carries no answer.
func TestC15EveryDecisionMoveRecordsItsText(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	usage := func(want string, args ...string) {
		t.Helper()
		d := args[2]
		before := w.show(d).Status
		if env, code := w.coordinator(args...); code != 2 || env["code"] != cli.CodeUsage {
			t.Fatalf("%v = %d %v", args, code, env)
		}
		if it := w.show(d); it.Status != before || it.Status != want {
			t.Fatalf("a refused %v moved %s from %s", args, d, before)
		}
	}
	done := func(args ...string) machine.Item {
		t.Helper()
		if env, code := w.coordinator(args...); code != 0 {
			t.Fatalf("%v = %d %v", args, code, env)
		}
		return w.show(args[2])
	}

	d := w.fileDecision("which store", "--waits-for-a-person", "the operator")
	usage(machine.DecisionPending, "decision", "fire", d, "--project", "alpha")
	usage(machine.DecisionPending, "decision", "fire", d, "--project", "alpha", "--why", "  ")
	if it := done("decision", "fire", d, "--project", "alpha", "--why", "the first user paid"); it.Status != machine.NeedsHuman ||
		it.Fact("why") != "the first user paid" {
		t.Fatalf("fired = %s %v", it.Status, it.Facts)
	}
	usage(machine.NeedsHuman, "decision", "defer", d, "--project", "alpha")
	if it := done("decision", "defer", d, "--project", "alpha", "--why", "the payment was refunded"); it.Status != machine.DecisionPending ||
		it.Fact("why") != "the payment was refunded" {
		t.Fatalf("deferred = %s %v", it.Status, it.Facts)
	}
	done("decision", "fire", d, "--project", "alpha", "--why", "a second user paid")
	if r := w.srv.Machine.Apply(ctx, machine.Request{Project: "alpha", Item: d, Verb: machine.VerbAnswer,
		Actor: machine.Server("test"), ExpectedStatus: machine.NeedsHuman}); r.Refusal == nil || r.Refusal.Code != cli.CodeUsage {
		t.Fatalf("an answer move with no answer = %+v", r.Refusal)
	}
	usage(machine.NeedsHuman, "decision", "settle", d, "--project", "alpha")
	if it := done("decision", "settle", d, "--project", "alpha", "--note", "postgres"); it.Status != machine.Done ||
		it.Fact(machine.FactAnswer) != "postgres" || it.Fact("close_note") != "" {
		t.Fatalf("settled = %s %v", it.Status, it.Facts)
	}

	dropped := w.fileDecision("which cache", "--waits-for-a-person", "the operator")
	done("decision", "fire", dropped, "--project", "alpha", "--why", "the cache bill came")
	usage(machine.NeedsHuman, "decision", "settle", dropped, "--project", "alpha", "--dropped")
	if it := done("decision", "settle", dropped, "--project", "alpha", "--dropped", "--note", "we keep no cache"); it.Status != machine.Canceled ||
		it.Fact("close_note") != "we keep no cache" || it.Fact(machine.FactAnswer) != "" {
		t.Fatalf("dropped = %s %v", it.Status, it.Facts)
	}

	moot := w.fileDecision("which queue", "--waits-for-a-person", "the operator")
	usage(machine.DecisionPending, "decision", "moot", moot, "--project", "alpha")
	if it := done("decision", "moot", moot, "--project", "alpha", "--note", "the queue was removed"); it.Status != machine.Canceled ||
		it.Fact("close_note") != "the queue was removed" {
		t.Fatalf("moot = %s %v", it.Status, it.Facts)
	}
}

// C14, spec Work 8: every fire records why. A fire by decision-timers on a
// --by date records a why that names the date, and the fired entry's text
// holds it. A fire by `decision fire` records the caller's --why, and
// clears the defer_reason of an earlier fire.
func TestC14EveryFireRecordsWhy(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	by := w.clock.Now().UTC().Add(48 * time.Hour).Format("2006-01-02")
	d := w.fileDecision("which store", "--by", by)
	if err := w.srv.Decisions.Tick(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if w.show(d).Status != machine.DecisionPending {
		t.Fatal("decision-timers fired before the --by date")
	}
	w.clock.Advance(72 * time.Hour)
	if err := w.srv.Decisions.Tick(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	it := w.show(d)
	why := it.Fact("why")
	if it.Status != machine.NeedsHuman || !strings.Contains(why, "--by date "+by) || it.Fact("defer_reason") != "" {
		t.Fatalf("fired by the date = %s %v", it.Status, it.Facts)
	}
	e := entryByID(w.entries("person"), "hold/alpha/"+d)
	if e == nil || !strings.Contains(e["text"].(string), why) {
		t.Fatalf("the fired entry does not show why %q: %v", why, e)
	}

	until := w.clock.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if env, code := w.coordinator("inbox", "defer", "hold/alpha/"+d, "--until", until, "--reason", "the operator decides on Friday"); code != 0 {
		t.Fatalf("defer = %d %v", code, env)
	}
	w.clock.Advance(2 * time.Hour)
	if err := w.srv.Decisions.Tick(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if it := w.show(d); it.Fact("defer_reason") != "the operator decides on Friday" {
		t.Fatalf("the fire that ended the defer = %v", it.Facts)
	}
	if env, code := w.coordinator("decision", "defer", d, "--project", "alpha", "--why", "Friday moved"); code != 0 {
		t.Fatalf("decision defer = %d %v", code, env)
	}
	if env, code := w.coordinator("decision", "fire", d, "--project", "alpha", "--why", "the operator is back"); code != 0 {
		t.Fatalf("decision fire = %d %v", code, env)
	}
	if it := w.show(d); it.Fact("why") != "the operator is back" || it.Fact("defer_reason") != "" {
		t.Fatalf("a hand fire = %v", it.Facts)
	}
}

// C16, spec Work 8: a deferred decision entry returns with its reason.
// `inbox defer` records the reason as the why of the defer move. One
// decision-timers tick after the time fires the decision, and the returned
// entry shows defer_reason equal to the reason and a why that names the
// end of the defer.
func TestC16ADeferredDecisionReturnsWithItsReason(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	d := w.fileDecision("which store", "--waits-for-a-person", "the operator")
	w.coordinator("decision", "fire", d, "--project", "alpha", "--why", "the first user paid")
	id := "hold/alpha/" + d
	const reason = "the operator reads the cost report on Friday"
	due := w.clock.Now().Add(time.Hour).UTC()
	if env, code := w.coordinator("inbox", "defer", id, "--until", due.Format(time.RFC3339), "--reason", reason); code != 0 {
		t.Fatalf("defer = %d %v", code, env)
	}
	if it := w.show(d); it.Status != machine.DecisionPending || it.Fact("why") != reason {
		t.Fatalf("deferred = %s %v", it.Status, it.Facts)
	}
	if entryByID(w.entries(""), id) != nil {
		t.Fatal("the deferred decision shows before its time")
	}
	w.clock.Advance(2 * time.Hour)
	if err := w.srv.Decisions.Tick(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	it := w.show(d)
	why := it.Fact("why")
	if it.Status != machine.NeedsHuman || !strings.Contains(why, "inbox defer") || !strings.Contains(why, due.Format(time.RFC3339)) {
		t.Fatalf("returned = %s %v", it.Status, it.Facts)
	}
	e := entryByID(w.entries("person"), id)
	if e == nil || e["defer_reason"] != reason || !strings.Contains(e["text"].(string), why) {
		t.Fatalf("the returned entry = %v", e)
	}
}
