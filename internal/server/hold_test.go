package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// decisions lists the decisions `loop decision list` answers, keyed by id,
// as the coordinator with args.
func (w *humanWorld) decisions(args ...string) map[string]map[string]any {
	w.t.Helper()
	env, code := w.coordinator(append([]string{"decision", "list"}, args...)...)
	if code != 0 && code != 1 {
		w.t.Fatalf("decision list %v = %d %v", args, code, env)
	}
	out := map[string]map[string]any{}
	data, _ := env["data"].([]any)
	for _, d := range data {
		m := d.(map[string]any)
		out[m["decision"].(string)] = m
	}
	return out
}

// holdsOf lists the inbox entries that name item.
func (w *humanWorld) holdsOf(item string) []map[string]any {
	w.t.Helper()
	var out []map[string]any
	for _, e := range w.entries("") {
		if e["item"] == item {
			out = append(out, e)
		}
	}
	return out
}

// C14, factorio-vlle Work 8: `loop work list` lists no decision, and
// `--kind decision` is no kind of work. `loop decision list` lists it.
func TestC14WorkListListsNoDecision(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	want := map[string]bool{}
	for _, typ := range []string{"task", "epic", "bug"} {
		is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "a " + typ, Type: typ})
		if err != nil {
			t.Fatal(err)
		}
		want[is.ID] = true
	}
	d := w.fileDecision("which store", "--waits-for-a-person", "the operator")

	env, code := w.coordinator("work", "list", "--project", "alpha")
	if code != 0 {
		t.Fatalf("work list = %d %v", code, env)
	}
	got := map[string]bool{}
	for _, it := range env["data"].([]any) {
		got[it.(map[string]any)["item"].(string)] = true
	}
	if got[d] {
		t.Fatalf("work list = %v, names the decision %s", got, d)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("work list = %v, want %s", got, id)
		}
	}
	if env, code := w.coordinator("work", "list", "--project", "alpha", "--kind", "decision"); code != 2 || env["code"] != cli.CodeUsage {
		t.Fatalf("work list --kind decision = %d %v, want USAGE", code, env)
	}

	listed := w.decisions("--project", "alpha", "--status", "pending")
	if e := listed[d]; len(listed) != 1 || e == nil || e["title"] != "which store" || e["status"] != machine.DecisionPending ||
		e["trigger"] != "the first paying user" || e["project"] != "alpha" {
		t.Fatalf("decision list --status pending = %v, want %s", listed, d)
	}
	if listed := w.decisions("--project", "alpha", "--status", "needs_human"); len(listed) != 0 {
		t.Fatalf("decision list --status needs_human = %v, want none", listed)
	}
	if listed := w.decisions(); listed[d] == nil {
		t.Fatalf("decision list of every project = %v, want %s", listed, d)
	}
}

// C15, factorio-vlle Work 8: a decision keeps each move of its table, and
// its record. Each verb runs once through the CLI, and each move the table
// does not start from the decision's state is refused.
func TestC15ADecisionKeepsEachMove(t *testing.T) {
	w := newHumanWorld(t, "")
	status := func(d string) any {
		t.Helper()
		e := w.decisions("--project", "alpha")[d]
		if e == nil {
			t.Fatalf("decision list names no %s", d)
		}
		return e["status"]
	}
	move := func(want string, args ...string) {
		t.Helper()
		if env, code := w.coordinator(append(args[:3:3], append([]string{"--project", "alpha"}, args[3:]...)...)...); code != 0 {
			t.Fatalf("%v = %d %v", args, code, env)
		}
		if got := status(args[2]); got != want {
			t.Fatalf("%v moved to %v, want %s", args, got, want)
		}
	}
	refused := func(args ...string) {
		t.Helper()
		before := status(args[2])
		env, code := w.coordinator(append(args[:3:3], append([]string{"--project", "alpha"}, args[3:]...)...)...)
		if code != 3 || env["code"] != cli.CodeTransitionNotAllowed {
			t.Fatalf("%v = %d %v, want TRANSITION_NOT_ALLOWED", args, code, env)
		}
		if got := status(args[2]); got != before {
			t.Fatalf("a refused %v moved %s from %v to %v", args, args[2], before, got)
		}
	}

	d := w.fileDecision("which store", "--waits-for-a-person", "the operator")
	refused("decision", "defer", d, "--why", "not yet")
	refused("decision", "settle", d, "--note", "too soon")
	move(machine.NeedsHuman, "decision", "fire", d, "--why", "the first user paid")
	refused("decision", "fire", d, "--why", "again")
	move(machine.DecisionPending, "decision", "defer", d, "--why", "the payment was refunded")
	move(machine.NeedsHuman, "decision", "fire", d, "--why", "a second user paid")
	move(machine.Done, "decision", "settle", d, "--note", "postgres")
	for _, args := range [][]string{{"decision", "fire", d, "--why", "x"}, {"decision", "defer", d, "--why", "x"},
		{"decision", "settle", d, "--note", "x"}, {"decision", "moot", d, "--note", "x"}} {
		refused(args...)
	}

	dropped := w.fileDecision("which cache", "--waits-for-a-person", "the operator")
	move(machine.NeedsHuman, "decision", "fire", dropped, "--why", "the cache bill came")
	move(machine.Canceled, "decision", "settle", dropped, "--dropped", "--note", "we keep no cache")

	pending := w.fileDecision("which queue", "--waits-for-a-person", "the operator")
	move(machine.Canceled, "decision", "moot", pending, "--note", "the queue was removed")
	refused("decision", "fire", pending, "--why", "x")

	fired := w.fileDecision("which region", "--waits-for-a-person", "the operator")
	move(machine.NeedsHuman, "decision", "fire", fired, "--why", "the first user abroad")
	move(machine.Canceled, "decision", "moot", fired, "--note", "one region serves both")

	if it := w.show(d); it.Kind != machine.KindDecision || it.Fact(machine.FactAnswer) != "postgres" {
		t.Fatalf("work show %s = %s %v, want the decision's record", d, it.Kind, it.Facts)
	}
}

// C16, factorio-vlle Work 8: a fired decision is one inbox hold, with id
// hold/<project>/<item>. A pending one is no entry. A question prepared on
// decision/<project>/<item> before the migration names the hold after it.
func TestC16AFiredDecisionIsAnInboxHold(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	d := w.fileDecision("which store", "--waits-for-a-person", "the operator")
	if es := w.holdsOf(d); len(es) != 0 {
		t.Fatalf("a pending decision has inbox entries %v", es)
	}
	w.coordinator("decision", "fire", d, "--project", "alpha", "--why", "the first user paid")
	es := w.holdsOf(d)
	if len(es) != 1 || es[0]["kind"] != "hold" || es[0]["entry"] != "hold/alpha/"+d || es[0]["awaits"] != "person" {
		t.Fatalf("a fired decision's entries = %v, want one hold hold/alpha/%s", es, d)
	}

	// A store at the schema before the migration, with a question on the
	// old entry id.
	if err := w.srv.Store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(`INSERT INTO questions (entry, body, created_at) VALUES (?, 'ship on Friday? yes or no', 0)`, "decision/alpha/"+d); err != nil {
			return err
		}
		return rewindSchema(tx, 18)
	}); err != nil {
		t.Fatal(err)
	}
	w.restart()
	if n := w.count(`SELECT count(*) FROM questions WHERE entry = ?`, "hold/alpha/"+d); n != 1 {
		t.Fatalf("questions on hold/alpha/%s after the migration = %d, want 1", d, n)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE entry LIKE 'decision/%'`); n != 0 {
		t.Fatalf("questions on a decision entry after the migration = %d, want 0", n)
	}
}

// C17, factorio-vlle Work 8: a decision filed with --by stays pending,
// and is no hold, while decision-timers runs on the last minute of the day
// before. The first run on the date fires it into a hold. A decision with
// no --by date stays pending after both runs.
func TestC17ADecisionDoesNotFireBeforeItsTrigger(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	date := w.clock.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 2)
	by := w.fileDecision("which store", "--by", date.Format("2006-01-02"))
	waits := w.fileDecision("which cache", "--waits-for-a-person", "the operator")
	tickAt := func(at time.Time) {
		t.Helper()
		w.clock.Advance(at.Sub(w.clock.Now()))
		if err := w.srv.Decisions.Tick(ctx, "alpha"); err != nil {
			t.Fatal(err)
		}
	}

	tickAt(date.Add(-time.Minute))
	if p := w.decisions("--project", "alpha", "--status", "pending"); p[by] == nil || p[waits] == nil {
		t.Fatalf("pending on the last minute of the day before = %v, want %s and %s", p, by, waits)
	}
	if es := w.holdsOf(by); len(es) != 0 {
		t.Fatalf("a decision before its date has inbox entries %v", es)
	}

	tickAt(date)
	if f := w.decisions("--project", "alpha", "--status", "needs_human"); len(f) != 1 || f[by] == nil {
		t.Fatalf("fired on the first minute of the date = %v, want %s only", f, by)
	}
	if es := w.holdsOf(by); len(es) != 1 || es[0]["entry"] != "hold/alpha/"+by {
		t.Fatalf("a decision on its date = %v, want hold/alpha/%s", es, by)
	}
	if p := w.decisions("--project", "alpha", "--status", "pending"); len(p) != 1 || p[waits] == nil {
		t.Fatalf("pending after both runs = %v, want %s only", p, waits)
	}
}

// rewindSchema sets the store back to schema version, so the next start
// runs each later migration again. A migration that adds a column cannot
// run twice, so the rewind drops each such column first.
func rewindSchema(tx *store.Tx, version int) error {
	if _, err := tx.Exec(`ALTER TABLE runs DROP COLUMN design_skill`); err != nil { // 0022
		return err
	}
	_, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version))
	return err
}
