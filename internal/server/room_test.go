package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/room"
)

const roomManifest = "secrets:\n  - name: API_KEY\n    source: op://vault/item/field\n"

func newRoomWorld(t *testing.T) *humanWorld {
	t.Helper()
	w := newHumanWorld(t, roomManifest)
	if err := w.srv.Secrets.Set(context.Background(), "alpha", "API_KEY", strings.NewReader(humanSecret)); err != nil {
		t.Fatal(err)
	}
	if err := w.srv.RefreshRedactor(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w
}

// openRoom opens a room as the coordinator and returns it.
func (w *humanWorld) openRoom(kind, item, reason string) map[string]any {
	w.t.Helper()
	args := []string{"room", "open", "--kind", kind, "--project", "alpha", "--reason", reason, "--score", "4"}
	if item != "" {
		args = append(args, "--item", item)
	}
	env, code := w.coordinator(args...)
	if code != 0 {
		w.t.Fatalf("room open = %d %v", code, env)
	}
	return env["data"].(map[string]any)["room"].(map[string]any)
}

func (w *humanWorld) room(id string) room.Room {
	w.t.Helper()
	r, err := w.srv.Rooms.Get(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func hasEnv(t *testing.T, checkout string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(checkout, brief.EnvFile))
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(data), "API_KEY=")
}

// A run token that opens a room is refused, and so is one that closes a
// room. Only the coordinator does either.
func TestOnlyTheCoordinatorOpensAndClosesARoom(t *testing.T) {
	w := newRoomWorld(t)
	item := todoTicket(t, w.bx)
	co := w.runToken("run-1", item, machine.StageImplementer)
	if env, _ := w.as(co, "room", "open", "--kind", "review", "--project", "alpha", "--item", item, "--reason", "r", "--score", "4"); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("a run opened a room: %v", env)
	}
	r := w.openRoom("review", item, "the reviewer and the author disagree")
	id := r["room"].(string)
	if r["state"] != room.Live {
		t.Fatalf("room = %v", r)
	}
	roomCo := r["checkout"].(string)
	if env, _ := w.as(roomCo, "room", "close", id); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("the room closed itself: %v", env)
	}
	if env, _ := w.as(co, "room", "close", id); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("a run closed a room: %v", env)
	}
	env, code := w.coordinator("room", "close", id)
	if code != 0 || w.room(id).State != room.Closed || w.room(id).Outcome != room.OutcomeAbandoned {
		t.Fatalf("close = %d %v", code, env)
	}
	if hasEnv(t, roomCo) || w.count(`SELECT count(*) FROM holds`) != 0 {
		t.Fatal("a closed room kept its .env or its hold")
	}
	if _, err := os.Stat(roomCo); err != nil {
		t.Fatalf("the closed room's checkout is gone: %v", err)
	}
}

// A room's hold blocks new admission on its item, and a running lease
// finishes first: the room waits in opening, and starts once the lease is
// free.
func TestRoomHoldWaitsForTheRunningLease(t *testing.T) {
	w := newRoomWorld(t)
	ctx := context.Background()
	item := todoTicket(t, w.bx)
	if _, err := w.bx.Update(ctx, item, beadsx.UpdateSpec{IfStatus: machine.Todo, Status: machine.InReview}); err != nil {
		t.Fatal(err)
	}
	st, ref := w.srv.Sched.Dispatch(ctx, "alpha", machine.StageCodeReviewer)
	if ref != nil || st == nil {
		t.Fatalf("dispatch = %+v %+v", st, ref)
	}
	r := w.openRoom("review", item, "the operator asked to see the diff")
	id := r["room"].(string)
	if r["state"] != room.Opening || r["checkout"] != nil && r["checkout"] != "" {
		t.Fatalf("the room started over a running lease: %v", r)
	}
	if l, _ := w.srv.Machine.Lease(ctx, "alpha", item); l.Owner != st.RunID {
		t.Fatalf("the room revoked the lease: %+v", l)
	}
	if _, ref := w.srv.Sched.Finish(ctx, st.RunID, "done"); ref != nil {
		t.Fatalf("finish = %+v", ref)
	}
	if err := w.srv.Rooms.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.room(id); got.State != room.Live || !hasEnv(t, got.Checkout) {
		t.Fatalf("the room did not start after the lease: %+v", got)
	}
	// The item is free of leases, and still no stage takes it.
	for _, stage := range []string{machine.StageCodeReviewer, machine.StageImplementer} {
		if c, ref := w.srv.Admission.Peek(ctx, "alpha", stage); c != nil || ref != nil {
			t.Fatalf("%s was offered a held item: %+v %+v", stage, c, ref)
		}
	}
	w.coordinator("room", "close", id)
	if c, _ := w.srv.Admission.Peek(ctx, "alpha", machine.StageCodeReviewer); c == nil || c.Item != item {
		t.Fatalf("the closed room still holds the item: %+v", c)
	}
}

// A room session crash moves the room to interrupted and removes its .env.
// A reopen uses the same checkout and writes .env again. A room at its
// prompt past the quiet period is idle, loses its .env, shows in the inbox
// and never closes itself.
func TestRoomLifecycleOfIdleAndInterrupted(t *testing.T) {
	w := newRoomWorld(t)
	ctx := context.Background()
	item := todoTicket(t, w.bx)
	r := w.openRoom("review", item, "a design question")
	id := r["room"].(string)
	got := w.room(id)
	if got.Session == nil || !hasEnv(t, got.Checkout) || got.Sessions != 1 {
		t.Fatalf("open room = %+v", got)
	}
	prompt, err := os.ReadFile(filepath.Join(got.Checkout, brief.PromptFile))
	if err != nil || !strings.Contains(string(prompt), "loop-review-room/SKILL.md") || !strings.Contains(string(prompt), "a design question") {
		t.Fatalf("room prompt = %s %v", prompt, err)
	}

	w.host.Exit(got.Session.ID, 1)
	if err := w.srv.Rooms.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	crashed := w.room(id)
	if crashed.State != room.Interrupted || hasEnv(t, crashed.Checkout) {
		t.Fatalf("after the crash: %+v", crashed)
	}
	if e := entryByID(w.entries("coordinator"), "room/"+id); e == nil {
		t.Fatal("the interrupted room is not in the inbox")
	}
	w.openRoom("review", item, "a design question, again")
	back := w.room(id)
	if back.State != room.Live || back.Checkout != got.Checkout || back.Sessions != 2 || !hasEnv(t, back.Checkout) || back.RunID == got.RunID {
		t.Fatalf("reopened = %+v", back)
	}

	w.host.EndTurn(back.Session.ID)
	w.host.Advance(2 * time.Minute)
	for range 3 {
		if err := w.srv.Rooms.Watch(ctx); err != nil {
			t.Fatal(err)
		}
	}
	idle := w.room(id)
	if idle.State != room.Idle || hasEnv(t, idle.Checkout) {
		t.Fatalf("idle = %+v", idle)
	}
	if e := entryByID(w.entries("person"), "room/"+id); e == nil || e["status"] != room.Idle {
		t.Fatalf("the idle room is not in the inbox: %v", e)
	}
	w.openRoom("review", item, "the operator is back")
	again := w.room(id)
	if again.State != room.Live || again.Sessions != 2 || !hasEnv(t, again.Checkout) {
		t.Fatalf("reopened idle = %+v", again)
	}
	var told bool
	for _, n := range w.host.Notifications(again.Session.ID) {
		told = told || strings.Contains(n, "reopened this room")
	}
	if !told {
		t.Fatal("the idle room's session was not told that it reopened")
	}
}

// `room apply` with no operator statement in its decision file is refused.
// A file that cites one answers the escalated item, which resumes, and the
// room reads applied until the coordinator closes it.
func TestRoomApplyNeedsAnOperatorStatement(t *testing.T) {
	w := newRoomWorld(t)
	item := todoTicket(t, w.bx)
	co := w.runToken("run-1", item, machine.StageImplementer)
	env, _ := w.as(co, "escalation", "raise", "--score", "4", "--body", "taste")
	id := env["data"].(map[string]any)["escalation"].(map[string]any)["escalation"]
	fixer := w.runToken("fix-1", "", "fixer")
	panelFile(t, fixer, "preference", nil)
	if _, code := w.as(fixer, "escalation", "route", fmt.Sprint(id), "--file", "panel.json"); code != 0 {
		t.Fatal("route failed")
	}
	r := w.openRoom("decision", item, "the escalation needs the operator's taste")
	rid, roomCo := r["room"].(string), r["checkout"].(string)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(roomCo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("none.json", `{"decision": "use the left layout"}`)
	if env, code := w.as(roomCo, "room", "apply", rid, "--file", "none.json"); code != 3 || env["code"] != cli.CodeNoOperatorStatement {
		t.Fatalf("apply with no statement = %d %v", code, env)
	}
	write("leak.json", `{"decision": "use `+humanSecret+`", "statement": "I said so"}`)
	if env, code := w.as(roomCo, "room", "apply", rid, "--file", "leak.json"); code != 3 || env["code"] != cli.CodeSecretInOutput {
		t.Fatalf("apply with a secret = %d %v", code, env)
	}
	if w.show(item).Status != machine.Escalated {
		t.Fatal("a refused apply moved the item")
	}
	write("ok.json", `{"decision": "use the left layout", "statement": "Left. The right one hides the total."}`)
	env, code := w.as(roomCo, "room", "apply", rid, "--file", "ok.json")
	if code != 0 || env["data"].(map[string]any)["answered"] != true {
		t.Fatalf("apply = %d %v", code, env)
	}
	if it := w.show(item); it.Status != machine.Todo || !strings.Contains(it.Fact(machine.FactAnswer), "Left. The right one hides the total.") {
		t.Fatalf("item = %s %v", it.Status, it.Facts)
	}
	if got := w.room(rid); got.State != room.Applied {
		t.Fatalf("room = %+v", got)
	}
	w.coordinator("room", "close", rid)
	if got := w.room(rid); got.State != room.Closed || got.Outcome != room.OutcomeApplied {
		t.Fatalf("closed = %+v", got)
	}
	if strings.Contains(w.run.logs.String(), humanSecret) {
		t.Fatal("a log line holds the secret value")
	}
}

// setIntakeCap rewrites host config with a new intake cap, as the
// operator does. The server reads it at the next call.
func (w *humanWorld) setIntakeCap(n int) {
	w.t.Helper()
	cfg, err := config.LoadHost(w.srv.HostPath)
	if err != nil {
		w.t.Fatal(err)
	}
	e := cfg.Throughput["intake"]
	e.Cap = n
	cfg.Throughput["intake"] = e
	if err := config.SaveHost(w.srv.HostPath, cfg); err != nil {
		w.t.Fatal(err)
	}
}

// KTD30: the intake cap counts the project's epics in counted document
// states. A new intake room at the cap is refused with CAP_FULL, and no
// room opens. A raised cap in host config applies with no restart.
func TestIntakeRoomOpensOnlyUnderTheIntakeCap(t *testing.T) {
	w := newRoomWorld(t)
	ctx := context.Background()
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "Widgets", Type: "epic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: machine.PRDReview}); err != nil {
		t.Fatal(err)
	}
	w.setIntakeCap(1)
	env, _ := w.coordinator("room", "open", "--kind", "intake", "--project", "alpha", "--reason", "the operator has a new idea", "--score", "4")
	if env["code"] != cli.CodeCapFull || !strings.Contains(fmt.Sprint(env["hint"]), "throughput.intake") {
		t.Fatalf("an intake room at the cap = %v", env)
	}
	if n := w.count(`SELECT count(*) FROM rooms`); n != 0 {
		t.Fatalf("a refused intake room left %d rooms", n)
	}
	// The same arguments replay the stored refusal (KTD13), so the second
	// open states its reason again in new words.
	w.setIntakeCap(2)
	if r := w.openRoom("intake", "", "the operator raised the intake cap"); r["state"] != room.Live {
		t.Fatalf("an intake room under the raised cap = %v", r)
	}
}

// C-bug-1: each `room open --kind intake` with no --item under the intake
// cap opens a new room, and an open room's reason never changes. Only an
// idle or interrupted room is reopened, and only on its own item.
func TestEachItemlessIntakeOpenIsANewRoom(t *testing.T) {
	w := newRoomWorld(t)
	first := w.openRoom("intake", "", "the operator has an idea about widgets")
	if first["state"] != room.Live {
		t.Fatalf("first intake room = %v", first)
	}
	env, code := w.coordinator("room", "open", "--kind", "intake", "--project", "alpha", "--reason", "the operator has an idea about gadgets", "--score", "4")
	if code != 0 {
		t.Fatalf("second intake open = %d %v", code, env)
	}
	data := env["data"].(map[string]any)
	second := data["room"].(map[string]any)
	if data["reopened"] == true || second["room"] == first["room"] {
		t.Fatalf("the second itemless intake open reopened live room %v: %v", first["room"], env)
	}
	if got := w.room(first["room"].(string)); got.Reason != "the operator has an idea about widgets" {
		t.Fatalf("the live room's reason changed to %q", got.Reason)
	}
	if n := w.count(`SELECT count(*) FROM rooms WHERE kind = 'intake' AND item = '' AND closed_at IS NULL`); n != 2 {
		t.Fatalf("open itemless intake rooms = %d, want 2", n)
	}
}

// C-bug-1: a second itemless intake open goes through the intake cap like
// the first. An open itemless intake room does not wave it past a full cap.
func TestASecondItemlessIntakeOpenMeetsTheIntakeCap(t *testing.T) {
	w := newRoomWorld(t)
	ctx := context.Background()
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "Widgets", Type: "epic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: machine.PRDReview}); err != nil {
		t.Fatal(err)
	}
	w.setIntakeCap(2)
	first := w.openRoom("intake", "", "the operator has an idea about widgets")
	w.setIntakeCap(1)
	env, _ := w.coordinator("room", "open", "--kind", "intake", "--project", "alpha", "--reason", "the operator has an idea about gadgets", "--score", "4")
	if env["code"] != cli.CodeCapFull {
		t.Fatalf("a second itemless intake open at the cap = %v", env)
	}
	if n := w.count(`SELECT count(*) FROM rooms`); n != 1 {
		t.Fatalf("rooms after a refused second intake open = %d, want 1", n)
	}
	if got := w.room(first["room"].(string)); got.Reason != "the operator has an idea about widgets" {
		t.Fatalf("the live room's reason changed to %q", got.Reason)
	}
}

// A room receives only the secrets its room's stage receives, each in the
// .env of the entry's directory (parity 9.3, 11.2). An implementer secret
// with no stored value does not stop an intake room.
func TestRoomReceivesOnlyItsStagesSecrets(t *testing.T) {
	w := newHumanWorld(t, roomManifest+"  - name: INTAKE_KEY\n    dir: apps/web\n    stages: [intake-room]\n"+
		"  - name: IMPL_KEY\n    stages: [implementer]\n")
	ctx := context.Background()
	for name, v := range map[string]string{"API_KEY": humanSecret, "INTAKE_KEY": "intake_value_1"} {
		if err := w.srv.Secrets.Set(ctx, "alpha", name, strings.NewReader(v)); err != nil {
			t.Fatal(err)
		}
	}
	r := w.openRoom("intake", "", "the operator wants widgets")
	if r["state"] != room.Live {
		t.Fatalf("room = %v", r)
	}
	co := r["checkout"].(string)
	root, err := os.ReadFile(filepath.Join(co, brief.EnvFile))
	if err != nil || string(root) != "API_KEY='"+humanSecret+"'\n" {
		t.Fatalf(".env = %v", err)
	}
	web, err := os.ReadFile(filepath.Join(co, "apps/web", brief.EnvFile))
	if err != nil || string(web) != "INTAKE_KEY='intake_value_1'\n" {
		t.Fatalf("apps/web/.env = %q, %v", web, err)
	}
	w.coordinator("room", "close", r["room"].(string))
	if _, err := os.Stat(filepath.Join(co, "apps/web", brief.EnvFile)); !os.IsNotExist(err) {
		t.Fatalf("a closed room kept apps/web/.env: %v", err)
	}
}

// An idle room whose session still runs gets back only its stage's
// secrets on reopen, each in the .env of the entry's directory (parity
// 9.3, 11.2). An implementer secret with no stored value does not stop
// the reopen. A directory the manifest gained while the room sat idle is
// excluded too, so git sees no new file.
func TestReopenedIdleRoomReceivesOnlyItsStagesSecrets(t *testing.T) {
	const manifest = roomManifest + "  - name: WEB_KEY\n    dir: apps/web\n    stages: [intake-room]\n" +
		"  - name: IMPL_KEY\n    stages: [implementer]\n"
	w := newHumanWorld(t, manifest)
	ctx := context.Background()
	for name, v := range map[string]string{"API_KEY": humanSecret, "WEB_KEY": "web_value_1", "API2_KEY": "api_value_2"} {
		if err := w.srv.Secrets.Set(ctx, "alpha", name, strings.NewReader(v)); err != nil {
			t.Fatal(err)
		}
	}
	r := w.openRoom("intake", "", "the operator wants widgets")
	id, co := r["room"].(string), r["checkout"].(string)
	w.host.EndTurn(w.room(id).Session.ID)
	w.host.Advance(2 * time.Minute)
	for range 3 {
		if err := w.srv.Rooms.Watch(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if idle := w.room(id); idle.State != room.Idle || hasEnv(t, co) {
		t.Fatalf("idle = %+v", idle)
	}
	snap, err := w.opts.Sched.ProjectConfig(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	grown, err := config.ParseManifest([]byte(manifest + "  - name: API2_KEY\n    dir: apps/api\n    stages: [intake-room]\n"))
	if err != nil {
		t.Fatal(err)
	}
	*snap.Manifest = *grown

	w.openRoom("intake", "", "the operator is back")
	again := w.room(id)
	if again.State != room.Live || again.Sessions != 1 {
		t.Fatalf("reopened idle = %+v", again)
	}
	for dir, want := range map[string]string{"": "API_KEY='" + humanSecret + "'\n", "apps/web": "WEB_KEY='web_value_1'\n",
		"apps/api": "API2_KEY='api_value_2'\n"} {
		got, err := os.ReadFile(filepath.Join(co, dir, brief.EnvFile))
		if err != nil || string(got) != want {
			t.Fatalf("%s/.env holds the wrong names (%d bytes): %v", dir, len(got), err)
		}
	}
	out, err := exec.Command("git", "-C", co, "status", "--porcelain").Output()
	if err != nil || len(out) != 0 {
		t.Fatalf("git status after the reopen = %q, %v", out, err)
	}
}

// C-bug-1: a room's session names its work. The terminal title and the
// first line of the first message the host launched carry the room kind,
// the item id when the room has one, and the reason, on one line.
func TestRoomSessionNamesItsWork(t *testing.T) {
	w := newRoomWorld(t)
	item := todoTicket(t, w.bx)
	for _, c := range []struct{ kind, item, reason, want string }{
		{"review", item, "the reviewer and\nthe author disagree", "the reviewer and the author disagree"},
		{"intake", "", "the operator has a new idea", "the operator has a new idea"},
	} {
		before := len(w.host.Launches())
		r := w.openRoom(c.kind, c.item, c.reason)
		if r["state"] != room.Live {
			t.Fatalf("room = %v", r)
		}
		l := w.host.Launches()
		if len(l) != before+1 {
			t.Fatalf("launches = %+v", l[before:])
		}
		title, msg := l[before].Spec.Title, l[before].Argv[len(l[before].Argv)-1]
		first, _, _ := strings.Cut(msg, "\n")
		want := []string{c.kind + " room", c.want}
		if c.item != "" {
			want = append(want, c.item)
		}
		for _, s := range want {
			if !strings.Contains(title, s) {
				t.Errorf("terminal title %q does not name %q", title, s)
			}
			if !strings.Contains(first, s) {
				t.Errorf("first line of the first message %q does not name %q", first, s)
			}
		}
		if strings.ContainsAny(title, "\n\r") {
			t.Errorf("terminal title %q is not one line", title)
		}
		w.coordinator("room", "close", r["room"].(string))
	}
}

// Parity 1.32: an architect room serves one architecture epic, so the
// operator and an agent write its ADR together. It needs the epic, and an
// item in any other state is refused before any room opens. Its session
// reads the architect room's runbook, and the epic stays in architecture:
// only the ADR's merge moves it.
func TestArchitectRoomServesAnArchitectureEpic(t *testing.T) {
	w := newRoomWorld(t)
	open := func(args ...string) map[string]any {
		env, _ := w.coordinator(append([]string{"room", "open", "--kind", "architect", "--project", "alpha",
			"--reason", "the operator wants to decide the widget store", "--score", "4"}, args...)...)
		return env
	}
	if env := open(); env["code"] != cli.CodeUsage || env["rule"] != "no_item" {
		t.Fatalf("an architect room with no item = %v", env)
	}
	ticket := todoTicket(t, w.bx)
	if env := open("--item", ticket); env["code"] != cli.CodeTransitionNotAllowed || env["rule"] != "not_architecture_epic" {
		t.Fatalf("an architect room on a ticket = %v", env)
	}
	review, res := w.srv.Machine.Create(context.Background(), machine.CreateRequest{Project: "alpha", Kind: machine.KindEpic,
		Title: "Widget feed", Description: "Feed the widgets.", Verb: machine.VerbPRDSubmit,
		Params: map[string]string{"pr": "7"}, Actor: machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("create the epic in review: %+v", *res.Refusal)
	}
	if st := w.show(review).Status; st != machine.PRDReview {
		t.Fatalf("the epic in review is in %s", st)
	}
	if env := open("--item", review); env["code"] != cli.CodeTransitionNotAllowed || env["rule"] != "not_architecture_epic" {
		t.Fatalf("an architect room on an epic in %s = %v", machine.PRDReview, env)
	}
	if n := w.count(`SELECT count(*) FROM rooms`); n != 0 {
		t.Fatalf("a refused architect room left %d rooms", n)
	}

	epic, res := w.srv.Machine.Create(context.Background(), machine.CreateRequest{Project: "alpha", Kind: machine.KindEpic,
		Title: "Widget store", Description: "Decide where widgets live.", Verb: machine.VerbCreateArchitecture,
		Actor: machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("create the architecture epic: %+v", *res.Refusal)
	}
	r := w.openRoom("architect", epic, "the operator wants to decide the widget store")
	if r["state"] != room.Live || r["kind"] != "architect" {
		t.Fatalf("architect room = %v", r)
	}
	got := w.room(r["room"].(string))
	prompt, err := os.ReadFile(filepath.Join(got.Checkout, brief.PromptFile))
	if err != nil || !strings.Contains(string(prompt), "loop-architect-room/SKILL.md") {
		t.Fatalf("architect room prompt = %s %v", prompt, err)
	}
	if st := w.show(epic).Status; st != machine.Architecture {
		t.Fatalf("opening the room moved the epic to %s", st)
	}
}
