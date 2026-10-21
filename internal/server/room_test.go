package server

import (
	"context"
	"fmt"
	"os"
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
	args := []string{"room", "open", "--kind", kind, "--project", "alpha", "--reason", reason}
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
	if env, _ := w.as(co, "room", "open", "--kind", "review", "--project", "alpha", "--item", item, "--reason", "r"); env["code"] != cli.CodeCapabilityDenied {
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
	env, _ := w.coordinator("room", "open", "--kind", "intake", "--project", "alpha", "--reason", "the operator has a new idea")
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
