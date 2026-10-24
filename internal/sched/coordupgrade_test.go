package sched

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
)

// C-bug-1: an upgrade never closes a live coordinator session. The server
// tells the live session about the new release once, and starts a session
// under the new release only after the old one stops being live.
func TestCoordinatorUpgradeKeepsALiveSession(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Projects = func() ([]config.Project, error) {
		return []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}}, nil
	}
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.cfg.Release = "v1"
	w.rebuild()
	if err := w.s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	w.tick()
	first := w.s.Coordinator()
	if first.Session == nil || first.Release != "v1" {
		t.Fatalf("first coordinator = %+v", first)
	}

	w.cfg.Release = "v2"
	w.rebuild()
	w.tick()
	got := w.s.Coordinator()
	if got.RunID != first.RunID || got.Session == nil || got.Session.ID != first.Session.ID {
		t.Fatalf("the upgrade replaced a live coordinator: %+v; first %+v", got, first)
	}
	if st, _ := w.host.Settle(ctx, first.Session.ID); st.Liveness != agenthost.Live {
		t.Fatalf("the upgrade closed the live coordinator session: liveness %s", st.Liveness)
	}
	if !w.liveToken(first.RunID) {
		t.Fatal("the upgrade revoked the live coordinator's token")
	}
	notes := w.host.Notifications(first.Session.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], "v2") {
		t.Fatalf("notifications after the upgrade = %q, want one that names v2", notes)
	}

	w.tick()
	if notes := w.host.Notifications(first.Session.ID); len(notes) != 1 {
		t.Fatalf("a second tick sent another message: %q", notes)
	}

	w.host.Exit(first.Session.ID, 0)
	w.tick()
	next := w.s.Coordinator()
	if next.Session == nil || next.RunID == first.RunID || next.Release != "v2" || next.Checkout != first.Checkout {
		t.Fatalf("after the old session exited = %+v; first %+v", next, first)
	}
	if w.liveToken(first.RunID) || !w.liveToken(next.RunID) {
		t.Fatalf("tokens: old live %v, new live %v", w.liveToken(first.RunID), w.liveToken(next.RunID))
	}
}

// C-bug-1: a refused message is not recorded, so the next tick under the
// new release sends it again. The refusal never closes the session.
func TestCoordinatorUpgradeRetriesARefusedMessage(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Projects = func() ([]config.Project, error) {
		return []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}}, nil
	}
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.cfg.Release = "v1"
	w.rebuild()
	if err := w.s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	w.tick()
	first := w.s.Coordinator()
	if first.Session == nil {
		t.Fatalf("first coordinator = %+v", first)
	}
	w.tick()
	if got := w.s.Coordinator(); got.RunID != first.RunID {
		t.Fatalf("the same release restarted the coordinator: %+v", got)
	}
	if notes := w.host.Notifications(first.Session.ID); len(notes) != 0 {
		t.Fatalf("the same release messaged the coordinator: %q", notes)
	}

	w.host.RefuseNotify(first.Session.ID, true)
	w.cfg.Release = "v2"
	w.cfg.Pack = brief.Pack{Version: "v2", FS: fstest.MapFS{
		"loop-coordinator/SKILL.md": {Data: []byte("# coordinator runbook v2\n")},
	}}
	w.rebuild()
	w.tick()
	if got := w.s.Coordinator(); got.RunID != first.RunID || got.Session == nil || got.NotifiedRelease != "" {
		t.Fatalf("after a refused message = %+v; first %+v", got, first)
	}
	if st, _ := w.host.Settle(ctx, first.Session.ID); st.Liveness != agenthost.Live {
		t.Fatalf("a refused message closed the coordinator: liveness %s", st.Liveness)
	}

	w.host.RefuseNotify(first.Session.ID, false)
	w.tick()
	notes := w.host.Notifications(first.Session.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], "v2") {
		t.Fatalf("notifications after the refusal ended = %q, want one that names v2", notes)
	}
	if got := w.s.Coordinator(); got.RunID != first.RunID || got.NotifiedRelease != "v2" {
		t.Fatalf("after the message = %+v", got)
	}
	// The message tells the session to read its runbook again, and the
	// runbook in its checkout is the new release's.
	runbook := brief.Runbook(agenthost.Claude, CoordinatorStage)
	if !strings.Contains(notes[0], "Read "+runbook+" again.") {
		t.Fatalf("the message does not tell the session to read %s again: %q", runbook, notes[0])
	}
	if data, err := os.ReadFile(filepath.Join(first.Checkout, runbook)); err != nil || string(data) != "# coordinator runbook v2\n" {
		t.Fatalf("the checkout's runbook after the upgrade = %q, %v", data, err)
	}
}

// C-bug-1: an upgrade leaves a session whose liveness reads unknown
// untouched. It is not closed, its token stays, and it gets no message.
func TestCoordinatorUpgradeLeavesAnUnknownSession(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Projects = func() ([]config.Project, error) {
		return []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}}, nil
	}
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.cfg.Release = "v1"
	w.rebuild()
	if err := w.s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	w.tick()
	first := w.s.Coordinator()
	if first.Session == nil || first.Release != "v1" {
		t.Fatalf("first coordinator = %+v", first)
	}

	// The handle goes stale and the inventory is incomplete, so the
	// session reads unknown and never absent.
	w.host.MakeStale(first.Session.ID)
	w.host.SetInventoryIncomplete(true)
	w.cfg.Release = "v2"
	w.rebuild()
	w.tick()
	if st, _ := w.host.Settle(ctx, first.Session.ID); st.Liveness != agenthost.Unknown {
		t.Fatalf("the session reads %s, want unknown", st.Liveness)
	}
	got := w.s.Coordinator()
	if got.RunID != first.RunID || got.Session == nil || got.Session.ID != first.Session.ID || got.Release != "v1" {
		t.Fatalf("the upgrade replaced an unknown coordinator: %+v; first %+v", got, first)
	}
	if !w.liveToken(first.RunID) {
		t.Fatal("the upgrade revoked the unknown coordinator's token")
	}
	if notes := w.host.Notifications(first.Session.ID); len(notes) != 0 {
		t.Fatalf("the upgrade messaged an unknown coordinator: %q", notes)
	}
}
