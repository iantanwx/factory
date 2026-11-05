package sched

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/store"
)

// handBack runs the rollback's hand-back over the world's store and
// projects.
func (hw *homeWorld) handBack() {
	hw.t.Helper()
	ps, err := hw.projects()
	if err != nil {
		hw.t.Fatal(err)
	}
	if err := HandBack(context.Background(), hw.st, ps); err != nil {
		hw.t.Fatal(err)
	}
}

// oldRow reads the payload of the coordinator row.
func (hw *homeWorld) oldRow() CoordinatorState {
	hw.t.Helper()
	var cs CoordinatorState
	if err := json.Unmarshal([]byte(hw.str(`SELECT payload FROM schedule WHERE kind = 'coordinator' AND project = ''`)), &cs); err != nil {
		hw.t.Fatal(err)
	}
	return cs
}

func (hw *homeWorld) homeRow(project string) (CoordinatorState, bool) {
	hw.t.Helper()
	if hw.count(`SELECT count(*) FROM schedule WHERE kind = 'home_coordinator' AND project = ?`, project) == 0 {
		return CoordinatorState{}, false
	}
	raw := hw.str(`SELECT payload FROM schedule WHERE kind = 'home_coordinator' AND project = ?`, project)
	var cs CoordinatorState
	if err := json.Unmarshal([]byte(raw), &cs); err != nil {
		hw.t.Fatal(err)
	}
	return cs, true
}

func (hw *homeWorld) ringRow(project string) {
	hw.t.Helper()
	if err := hw.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO schedule (project, kind, payload, created_at, updated_at) VALUES (?, 'coordinator_ring', '{}', 0, 0)`, project)
		return err
	}); err != nil {
		hw.t.Fatal(err)
	}
}

// C39: a rollback past this release leaves one coordinator that the
// earlier release rings. A is the first registered project. Its session
// moves into the coordinator row, and its token names no project. B's
// token is revoked, and its row is Revoked and Fresh. The doorbell's old
// cursor moves to the lowest cursor, and the rows the earlier release does
// not read go.
func TestC39HandBackLeavesOneCoordinator(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "loop"}, map[string]bool{"alpha": true, "loop": true})
	hw.tick()
	a, b := hw.mustHome("alpha"), hw.mustHome("loop")
	hw.setCursor(notifyKind, "", 100)
	hw.setCursor(homeNotifyKind, "alpha", 200)
	hw.setCursor(homeNotifyKind, "loop", 300)
	hw.ringRow("")
	hw.ringRow("alpha")
	hw.ringRow("loop")

	hw.handBack()

	if hw.oldRows() != 1 {
		t.Fatalf("%d coordinator rows, want 1", hw.oldRows())
	}
	old := hw.oldRow()
	if old.RunID != a.RunID || old.Checkout != a.Checkout || old.Session == nil || old.Session.ID != a.Session.ID {
		t.Fatalf("the coordinator row = %+v, want alpha's session %+v", old, a)
	}
	if _, ok := hw.homeRow("alpha"); ok {
		t.Fatal("alpha's home_coordinator row stays")
	}
	if !hw.liveToken(a.RunID) || hw.tokenProject(a.RunID) != "" {
		t.Fatalf("alpha's token: live %v, project %q, want live and no project", hw.liveToken(a.RunID), hw.tokenProject(a.RunID))
	}
	cs, ok := hw.homeRow("loop")
	if !ok || !cs.Revoked || !cs.Fresh || cs.RunID != b.RunID {
		t.Fatalf("loop's row = %+v, want Revoked and Fresh", cs)
	}
	if hw.liveToken(b.RunID) {
		t.Fatal("loop's token still verifies")
	}
	if at, ok := hw.cursor(notifyKind, ""); !ok || at != 200 {
		t.Fatalf("inbox_notify = %d %v, want the lowest cursor 200", at, ok)
	}
	if n := hw.count(`SELECT count(*) FROM schedule WHERE kind = 'home_notify'`); n != 0 {
		t.Fatalf("%d home_notify rows stay", n)
	}
	if n := hw.count(`SELECT count(*) FROM schedule WHERE kind = 'coordinator_ring' AND project != ''`); n != 0 {
		t.Fatalf("%d coordinator_ring rows with a project stay", n)
	}
	if n := hw.count(`SELECT count(*) FROM schedule WHERE kind = 'coordinator_ring' AND project = ''`); n != 1 {
		t.Fatalf("%d coordinator_ring rows with no project, want the one that stays", n)
	}
}

// C39: the tick of this release over a handed-back store closes the
// session of the Revoked row, and starts that project's next session
// fresh, with the left-open list. The handed-back session is the old
// session, and it keeps running.
func TestC39TheTickAfterAHandBackStartsTheRevokedProjectFresh(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "loop"}, map[string]bool{"alpha": true, "loop": true})
	hw.s.SetLeftOpen(func(_ context.Context, project string) ([]string, error) {
		return []string{"question/9 of " + project}, nil
	})
	hw.tick()
	a, b := hw.mustHome("alpha"), hw.mustHome("loop")
	hw.handBack()
	hw.tick()

	if hw.live(b.Session) {
		t.Fatal("the session of the Revoked row still runs")
	}
	next := hw.mustHome("loop")
	if next.RunID == b.RunID || !hw.live(next.Session) || next.Revoked || next.Fresh || next.Checkout != b.Checkout {
		t.Fatalf("loop's coordinator after the tick = %+v; before %+v", next, b)
	}
	if !hw.liveToken(next.RunID) || hw.tokenProject(next.RunID) != "loop" {
		t.Fatalf("loop's new token: live %v, project %q", hw.liveToken(next.RunID), hw.tokenProject(next.RunID))
	}
	launches := hw.host.Launches()
	prompt, err := os.ReadFile(launches[len(launches)-1].Spec.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "question/9 of loop") {
		t.Fatalf("the prompt does not name the left-open list:\n%s", prompt)
	}
	if !hw.live(a.Session) || !hw.liveToken(a.RunID) || hw.oldRows() != 1 {
		t.Fatalf("the handed-back session: live %v, token %v, old rows %d", hw.live(a.Session), hw.liveToken(a.RunID), hw.oldRows())
	}
	if got := hw.s.Topology(); got.Old != "alpha" || got.Homes["alpha"] || !got.Homes["loop"] {
		t.Fatalf("topology after the tick = %+v", got)
	}
	if n := hw.count(`SELECT count(*) FROM schedule WHERE kind = 'home_notify'`); n != 0 {
		t.Fatalf("%d home_notify rows; each cursor starts at inbox_notify", n)
	}
}

// C39: when the store already holds a coordinator row, the hand-back keeps
// it, and revokes the token of every home_coordinator row.
func TestC39HandBackKeepsTheCoordinatorRow(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "beta", "gamma"}, map[string]bool{"alpha": true, "beta": true, "gamma": true})
	old := hw.oldSession("gamma")
	hw.tick()
	a, b := hw.mustHome("alpha"), hw.mustHome("beta")

	hw.handBack()

	if hw.oldRows() != 1 || hw.oldRow().RunID != old.RunID {
		t.Fatalf("the coordinator row = %+v, want the old session %s", hw.oldRow(), old.RunID)
	}
	if !hw.liveToken(old.RunID) {
		t.Fatal("the old session's token was revoked")
	}
	for _, cs := range []CoordinatorState{a, b} {
		row, ok := hw.homeRow(cs.Project)
		if !ok || !row.Revoked || !row.Fresh {
			t.Fatalf("%s's row = %+v, want Revoked and Fresh", cs.Project, row)
		}
		if hw.liveToken(cs.RunID) {
			t.Fatalf("%s's token still verifies", cs.Project)
		}
	}
}

// C39: when the first registered project has no home_coordinator row, the
// hand-back moves none, so the earlier release starts its coordinator
// there.
func TestC39HandBackMovesNoRowOfAProjectWithNone(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "beta"}, map[string]bool{"beta": true})
	hw.tick()
	b := hw.mustHome("beta")

	hw.handBack()

	if hw.oldRows() != 0 {
		t.Fatal("the hand-back wrote a coordinator row for a project with no coordinator")
	}
	if row, ok := hw.homeRow("beta"); !ok || !row.Revoked || hw.liveToken(b.RunID) {
		t.Fatalf("beta's row = %+v, token live %v, want Revoked", row, hw.liveToken(b.RunID))
	}
}

// C39 on a host with no loop project: the destination "" has no
// home_notify row, so it counts at the inbox_notify value, and that value
// stays. An entry of "" after it still rings the earlier release's one
// coordinator.
func TestC39HandBackKeepsTheCursorOfAHostWithNoLoopProject(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "beta"}, map[string]bool{"alpha": true, "beta": true})
	hw.tick()
	hw.setCursor(notifyKind, "", 100)
	hw.setCursor(homeNotifyKind, "alpha", 200)
	hw.setCursor(homeNotifyKind, "beta", 300)

	hw.handBack()

	if at, ok := hw.cursor(notifyKind, ""); !ok || at != 100 {
		t.Fatalf("inbox_notify = %d %v, want 100 as it was", at, ok)
	}
}

// C33: an upgrade messages each running coordinator once. Two sessions
// started under v1, and the server now runs v2. After three ticks each
// session got exactly one message that names v2. Once one ends, its next
// session starts under v2 in the same checkout.
func TestC33AnUpgradeMessagesEachCoordinatorOnce(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "loop"}, map[string]bool{"alpha": true, "loop": true})
	hw.tick()
	a, b := hw.mustHome("alpha"), hw.mustHome("loop")
	if a.Release != "v1" || b.Release != "v1" {
		t.Fatalf("fixture: releases %q %q, want v1", a.Release, b.Release)
	}

	hw.cfg.Release = "v2"
	hw.rebuild()
	hw.tick()
	hw.tick()
	hw.tick()
	for _, cs := range []CoordinatorState{a, b} {
		notes := hw.host.Notifications(cs.Session.ID)
		if len(notes) != 1 || !strings.Contains(notes[0], "v2") {
			t.Fatalf("%s's messages = %q, want one that names v2", cs.Project, notes)
		}
		if got := hw.mustHome(cs.Project); got.RunID != cs.RunID || !hw.live(cs.Session) {
			t.Fatalf("the upgrade replaced %s's live coordinator: %+v", cs.Project, got)
		}
	}

	hw.host.Exit(a.Session.ID, 0)
	hw.tick()
	next := hw.mustHome("alpha")
	if next.RunID == a.RunID || next.Release != "v2" || next.Checkout != a.Checkout || !hw.live(next.Session) {
		t.Fatalf("alpha's next coordinator = %+v; first %+v", next, a)
	}
	if notes := hw.host.Notifications(b.Session.ID); len(notes) != 1 {
		t.Fatalf("loop's session got another message: %q", notes)
	}
}
