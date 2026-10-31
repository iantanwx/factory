package server

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// withStageRenames puts pairs in the rename table that a server start
// reads, for the test.
func withStageRenames(t *testing.T, pairs ...[2]string) {
	t.Helper()
	saved := stageRenames
	stageRenames = func() [][2]string { return pairs }
	t.Cleanup(func() { stageRenames = saved })
}

// C8, done means of the ticket: a stage_pauses row under the old name
// reads under the new name after one start, and a second start changes no
// row.
func TestC8StartRenamesAnOldStageInTheStore(t *testing.T) {
	withStageRenames(t, [2]string{"old-stage", machine.StageImplementer})
	w := newHumanWorld(t, "")
	todoTicket(t, w.bx)
	if err := w.srv.Store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO stage_pauses (project, stage, reason, paused_by, paused_at) VALUES ('alpha', 'old-stage', 'held before the upgrade', 'coordinator:coord-1', 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	w.restart()
	if n := w.count(`SELECT count(*) FROM stage_pauses WHERE stage = 'old-stage'`); n != 0 {
		t.Fatalf("%d pause rows under the old name after a start, want 0", n)
	}
	if n := w.count(`SELECT count(*) FROM stage_pauses WHERE project = 'alpha' AND stage = ? AND reason = 'held before the upgrade' AND paused_at = 1`,
		machine.StageImplementer); n != 1 {
		t.Fatalf("%d pause rows under the new name after a start, want 1", n)
	}
	if !strings.Contains(w.run.logs.String(), `"msg":"renamed old stage names","rows":1`) {
		t.Fatalf("the first start logged no rename of one row:\n%s", w.run.logs.String())
	}
	if env, code := w.coordinator("work", "peek", "--stage", machine.StageImplementer, "--project", "alpha"); code != 1 {
		t.Fatalf("work peek of the paused stage = %d %v, want exit 1", code, env)
	}

	w.restart()
	if strings.Contains(w.run.logs.String(), "renamed old stage names") {
		t.Fatalf("the second start renamed a row:\n%s", w.run.logs.String())
	}
	if n := w.count(`SELECT count(*) FROM stage_pauses WHERE project = 'alpha' AND stage = ? AND reason = 'held before the upgrade' AND paused_at = 1`,
		machine.StageImplementer); n != 1 {
		t.Fatalf("%d pause rows under the new name after a second start, want 1", n)
	}
}
