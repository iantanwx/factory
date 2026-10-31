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
	"github.com/iantanwx/loop/internal/store"
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
	if entryByID(w.entries(""), "hold/alpha/"+d) != nil {
		t.Fatal("a pending decision waits on a person")
	}
	moot := w.fileDecision("which queue", "--waits-for-a-person", "the operator")
	if env, code := w.coordinator("decision", "moot", moot, "--project", "alpha", "--note", "the queue was removed"); code != 0 || w.show(moot).Status != machine.Canceled {
		t.Fatalf("moot from pending = %d %v", code, env)
	}
	if _, code := w.coordinator("decision", "fire", d, "--project", "alpha", "--why", "the first user paid"); code != 0 {
		t.Fatal("fire failed")
	}
	e := entryByID(w.entries("person"), "hold/alpha/"+d)
	if e == nil || !strings.Contains(e["text"].(string), "<!--sdlc:agent run=coord-1-->\nthe first paying user\n<!--/sdlc:agent-->") {
		t.Fatalf("fired decision entry = %v", e)
	}
	if env, code := w.coordinator("inbox", "answer", "hold/alpha/"+d, "--body", "postgres"); code != 0 {
		t.Fatalf("answer = %d %v", code, env)
	}
	if it := w.show(d); it.Status != machine.Done || it.Fact(machine.FactAnswer) != "postgres" {
		t.Fatalf("answered = %s %v", it.Status, it.Facts)
	}
	dropped := w.fileDecision("which cache", "--waits-for-a-person", "the operator")
	w.coordinator("decision", "fire", dropped, "--project", "alpha", "--why", "the cache bill came")
	if env, code := w.coordinator("decision", "settle", dropped, "--project", "alpha", "--dropped", "--note", "we keep no cache"); code != 0 || w.show(dropped).Status != machine.Canceled {
		t.Fatalf("a dropped decision = %d %v, %s", code, env, w.show(dropped).Status)
	}
}

// C13: a decision names its notice. `decision file` takes exactly one of
// --by and --waits-for-a-person, and neither or both refuse with USAGE and
// file nothing. A decision that waits for a person holds waits_for, makes
// no inbox entry while it is pending, and decision-timers never fires it.
func TestDecisionFileNamesItsNotice(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	for _, notice := range [][]string{nil, {"--by", "2000-01-01", "--waits-for-a-person", "the operator"}} {
		args := append([]string{"decision", "file", "--project", "alpha", "--title", "which store", "--trigger", "the first paying user"}, notice...)
		if env, code := w.coordinator(args...); code != 2 || env["code"] != cli.CodeUsage {
			t.Fatalf("decision file %v = %d %v", notice, code, env)
		}
	}
	if its, ref := w.srv.Machine.List(ctx, "alpha", machine.ListFilter{Kind: machine.KindDecision}); ref != nil || len(its) != 0 {
		t.Fatalf("a refused decision file filed %v %v", its, ref)
	}
	byDate := w.fileDecision("which queue", "--by", "2000-01-01")
	if it := w.show(byDate); it.Status != machine.DecisionPending || it.Fact("by") != "2000-01-01" || it.Fact("waits_for") != "" {
		t.Fatalf("filed with --by = %s %v", it.Status, it.Facts)
	}
	waits := w.fileDecision("which cache", "--waits-for-a-person", "the operator reads the cost report")
	if it := w.show(waits); it.Status != machine.DecisionPending || it.Fact("waits_for") != "the operator reads the cost report" || it.Fact("by") != "" {
		t.Fatalf("filed with --waits-for-a-person = %s %v", it.Status, it.Facts)
	}
	if entryByID(w.entries(""), "hold/alpha/"+waits) != nil {
		t.Fatal("a pending decision that waits for a person makes an inbox entry")
	}
	w.clock.Advance(100 * 365 * 24 * time.Hour)
	if err := w.srv.Decisions.Tick(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if w.show(byDate).Status != machine.NeedsHuman || w.show(waits).Status != machine.DecisionPending {
		t.Fatalf("a hundred years on: --by %s, --waits-for-a-person %s", w.show(byDate).Status, w.show(waits).Status)
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
	if _, code := w.coordinator("decision", "defer", d, "--project", "alpha", "--why", "the store is not chosen yet"); code != 0 {
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

// A code-reviewer's spec defect pages a person now, as a code pass with
// `epic` escalates the epic (code-reviewer.md:1098-1130, alpha). A run may
// not fire a decision it does not hold, so `decision file --now` files and
// fires it in one write: the decision waits on a person in the inbox, and
// the coordinator hears of it. --now with --by names two triggers.
func TestDecisionFiledNowWaitsOnAPersonAtOnce(t *testing.T) {
	w := newHumanWorld(t, "")
	item := todoTicket(t, w.bx)
	rev := w.runToken("rev-1", item, machine.StageCodeReviewer)
	waits := w.fileDecisionAs(rev, "the spec names no retry limit")
	if env, _ := w.as(rev, "decision", "fire", waits, "--why", "the spec names no retry limit"); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("a run fired a decision it does not hold: %v", env)
	}
	env, code := w.as(rev, "decision", "file", "--title", "the spec names no backoff", "--trigger", "the code-reviewer passed a ticket on a spec defect",
		"--body", "spec section 4.2 names no backoff", "--waits-for-a-person", "the operator", "--now")
	if code != 0 {
		t.Fatalf("decision file --now = %d %v", code, env)
	}
	data := env["data"].(map[string]any)
	d := data["decision"].(string)
	if data["status"] != machine.NeedsHuman || w.show(d).Status != machine.NeedsHuman {
		t.Fatalf("filed now = %v, stored %s", data, w.show(d).Status)
	}
	e := entryByID(w.entries("person"), "hold/alpha/"+d)
	if e == nil || !strings.Contains(e["text"].(string), "<!--sdlc:agent run=rev-1-->\nthe code-reviewer passed a ticket on a spec defect\n<!--/sdlc:agent-->") {
		t.Fatalf("the decision filed now = %v", e)
	}
	w.waitNotified("hold/alpha/" + d)
	if env, _ := w.as(rev, "decision", "file", "--title", "the spec names no jitter", "--trigger", "launch", "--now",
		"--by", w.clock.Now().UTC().Format("2006-01-02")); env["code"] != cli.CodeUsage {
		t.Fatalf("--now with --by = %v", env)
	}
}

// fileDecisionAs files a decision from checkout with no --now, and checks
// that it waits in pending with no inbox entry.
func (w *humanWorld) fileDecisionAs(checkout, title string) string {
	w.t.Helper()
	env, code := w.as(checkout, "decision", "file", "--title", title, "--trigger", "the first paying user", "--waits-for-a-person", "the operator")
	if code != 0 {
		w.t.Fatalf("decision file = %d %v", code, env)
	}
	d := env["data"].(map[string]any)["decision"].(string)
	if w.show(d).Status != machine.DecisionPending || entryByID(w.entries(""), "hold/alpha/"+d) != nil {
		w.t.Fatalf("a decision filed with no --now = %s", w.show(d).Status)
	}
	return d
}

// `inbox defer --until` hides the entry until that time. A deferred
// decision goes back to pending, and decision-timers fires it at that
// time, also across a server restart. The time is in SQLite, and beads
// never holds a deferred status.
func TestInboxDeferResumesAtItsTimeAcrossARestart(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	d := w.fileDecision("which store", "--waits-for-a-person", "the operator")
	w.coordinator("decision", "fire", d, "--project", "alpha", "--why", "the first user paid")
	env, _ := w.coordinator("inbox", "prepare", "--body", "ship on Friday? options: yes, costs a weekend; no, costs a week",
		"--entry", "hold/alpha/"+d)
	q := env["data"].(map[string]any)["entry"].(string)
	until := w.clock.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for _, id := range []string{"hold/alpha/" + d, q} {
		if env, code := w.coordinator("inbox", "defer", id, "--until", until, "--reason", "the operator decides on Friday"); code != 0 {
			t.Fatalf("defer %s = %d %v", id, code, env)
		}
	}
	if it := w.show(d); it.Status != machine.DecisionPending {
		t.Fatalf("deferred decision = %s", it.Status)
	}
	if es := w.entries(""); entryByID(es, q) != nil || entryByID(es, "hold/alpha/"+d) != nil {
		t.Fatalf("deferred entries show: %v", es)
	}
	if env, _ := w.coordinator("inbox", "defer", q, "--until", "2000-01-01T00:00:00Z", "--reason", "the past"); env["code"] != cli.CodeUsage {
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
	dq, de := entryByID(es, q), entryByID(es, "hold/alpha/"+d)
	if dq == nil || de == nil || dq["link"] != "hold/alpha/"+d {
		t.Fatalf("entries after the defer = %v", es)
	}
	if env, code := w.coordinator("inbox", "answer", q, "--body", "yes"); code != 0 {
		t.Fatalf("answer the question = %d %v", code, env)
	}
	if body, answer, err := w.srv.Inbox.Question(ctx, 1); err != nil || answer != "yes" || !strings.Contains(body, "Friday") {
		t.Fatalf("question = %q %q %v", body, answer, err)
	}
}

// C16, spec Work 7: `inbox defer` requires --reason. An entry that is no
// decision is absent until --until, then returns with defer_reason equal
// to the reason. A reason that holds the value of a secret is refused, and
// each refusal defers nothing. A defer row written before the reason
// existed holds {}, and its entry returns with no defer_reason.
func TestC16ADeferredEscalationReturnsWithItsReason(t *testing.T) {
	w := newHumanWorld(t, "")
	w.srv.Redactor.SetLive("test/value", humanSecret)
	id, old := "escalation/alpha/"+w.ticketIn(machine.Escalated), "escalation/alpha/"+w.ticketIn(machine.Escalated)
	until := w.clock.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if env, code := w.coordinator("inbox", "defer", id, "--until", until); code != 2 || env["code"] != cli.CodeUsage {
		t.Fatalf("a defer with no --reason = %d %v", code, env)
	}
	if env, code := w.coordinator("inbox", "defer", id, "--until", until, "--reason", "the key is "+humanSecret); code != 3 ||
		env["code"] != cli.CodeSecretInOutput {
		t.Fatalf("a reason that holds a secret = %d %v", code, env)
	}
	if entryByID(w.entries(""), id) == nil || w.count(`SELECT count(*) FROM schedule WHERE kind = 'inbox_defer'`) != 0 {
		t.Fatal("a refused defer deferred the entry")
	}
	const reason = "the operator rotates the deploy key on Friday"
	for _, e := range []string{id, old} {
		if env, code := w.coordinator("inbox", "defer", e, "--until", until, "--reason", reason); code != 0 {
			t.Fatalf("defer %s = %d %v", e, code, env)
		}
	}
	if err := w.srv.Store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE schedule SET payload = '{}' WHERE kind = 'inbox_defer' AND stage = ?`, old)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if es := w.entries(""); entryByID(es, id) != nil || entryByID(es, old) != nil {
		t.Fatalf("a deferred entry shows before its time: %v", es)
	}

	w.clock.Advance(2 * time.Hour)
	es := w.entries("")
	if e := entryByID(es, id); e == nil || e["defer_reason"] != reason {
		t.Fatalf("the returned entry = %v", e)
	}
	if e := entryByID(es, old); e == nil || e["defer_reason"] != nil {
		t.Fatalf("the entry of a defer row with no reason = %v", e)
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
	// A server with no GitHub cannot merge the document, so the answer
	// moves nothing. The acceptance suite merges one (factorio-8qm).
	if env, _ := w.coordinator("inbox", "answer", "signoff/alpha/"+ep.ID, "--body", "ok"); env["code"] != cli.CodeNotImplemented {
		t.Fatalf("an inbox answer with no GitHub: %v", env)
	}
	if got := w.show(ep.ID).Status; got != machine.PRDSignoff {
		t.Fatalf("a refused sign-off moved the epic to %s", got)
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

// `inbox note` records the root cause of an open alert, and the alert stays
// open. A repeat of that alert after the finding rings no one: its entry
// keeps the time it had at the note. An alert with no finding still rings
// on each repeat, except run_start_failed (F-41), and a later note replaces
// the finding.
func TestInboxNoteHoldsAFindingAndARepeatDoesNotRing(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	alerts := config.StoreAlerter(w.srv.Store)
	crash := config.Alert{Project: "alpha", Kind: sched.AlertStartFailed, DedupeKey: "start", Subject: "implementer run r-1 did not start",
		Body: "disk full"}
	quiet := config.Alert{Project: "alpha", Kind: sched.AlertRunUnknown, DedupeKey: "unknown", Subject: "a run's liveness is unknown"}
	for _, a := range []config.Alert{crash, quiet} {
		if err := alerts.Raise(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	var noted, plain string
	for _, e := range w.entries("") {
		switch e["status"] {
		case sched.AlertStartFailed:
			noted = e["entry"].(string)
		case sched.AlertRunUnknown:
			plain = e["entry"].(string)
		}
		if e["found"] != nil {
			t.Fatalf("an alert with no note holds a finding: %v", e)
		}
	}
	env, _ := w.coordinator("inbox", "wait")
	cursor := env["data"].(map[string]any)["cursor"].(string)

	w.clock.Advance(time.Minute)
	env, code := w.coordinator("inbox", "note", noted, "--body", "the checkout disk is full; the operator frees /var")
	if code != 0 || env["data"].(map[string]any)["entry"] != noted || env["data"].(map[string]any)["found"] != true {
		t.Fatalf("inbox note = %d %v", code, env)
	}
	if env, code := w.coordinator("inbox", "wait", "--since", cursor); code != 4 {
		t.Fatalf("the note rang the coordinator: %d %v", code, env)
	}
	e := entryByID(w.entries(""), noted)
	if e == nil || e["found"] != true ||
		!strings.Contains(e["text"].(string), "<!--sdlc:agent run=coord-1-->\nthe checkout disk is full; the operator frees /var\n<!--/sdlc:agent-->") {
		t.Fatalf("the noted alert = %v", e)
	}

	w.clock.Advance(time.Minute)
	for _, a := range []config.Alert{crash, crash} {
		if err := alerts.Raise(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if env, code := w.coordinator("inbox", "wait", "--since", cursor); code != 4 {
		t.Fatalf("a repeat after the finding rang: %d %v", code, env)
	}
	e = entryByID(w.entries(""), noted)
	if e == nil || e["found"] != true || !strings.Contains(e["text"].(string), "Raised 3 times, 2 of them after the finding.") {
		t.Fatalf("the repeated alert = %v", e)
	}

	if err := alerts.Raise(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	env, code = w.coordinator("inbox", "wait", "--since", cursor)
	es, _ := env["data"].(map[string]any)["entries"].([]any)
	if code != 0 || len(es) != 1 || es[0].(map[string]any)["entry"] != plain {
		t.Fatalf("a repeat with no finding did not ring: %d %v", code, env)
	}

	if _, code := w.coordinator("inbox", "note", noted, "--body", "a guess: the disk fills from old checkouts"); code != 0 {
		t.Fatal("a second note was refused")
	}
	text := entryByID(w.entries(""), noted)["text"].(string)
	if strings.Contains(text, "frees /var") || !strings.Contains(text, "old checkouts") || !strings.Contains(text, "Raised 3 times, 0 of them after the finding.") {
		t.Fatalf("the second note = %q", text)
	}

	for _, bad := range []struct{ entry, code string }{
		{"question/1", cli.CodeUsage}, {"alert/x", cli.CodeUsage}, {"alert/99", cli.CodeNotFound},
	} {
		if env, _ := w.coordinator("inbox", "note", bad.entry, "--body", "x"); env["code"] != bad.code {
			t.Fatalf("note %s = %v", bad.entry, env)
		}
	}
	if _, code := w.coordinator("inbox", "answer", noted, "--body", "freed the disk"); code != 0 {
		t.Fatal("the noted alert does not close")
	}
	if env, _ := w.coordinator("inbox", "note", noted, "--body", "late"); env["code"] != cli.CodeNotFound {
		t.Fatalf("a note on a closed alert = %v", env)
	}
}
