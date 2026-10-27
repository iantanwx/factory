package admission

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

func (w *world) pause(project, stage, reason string) {
	w.t.Helper()
	if _, err := Pause(context.Background(), w.st, StagePause{Project: project, Stage: stage, Reason: reason, By: "coordinator:c-1"}); err != nil {
		w.t.Fatalf("pause %s/%s: %v", project, stage, err)
	}
}

func (w *world) paused(project, stage string) bool {
	w.t.Helper()
	ok, err := w.a.Paused(context.Background(), project, stage)
	if err != nil {
		w.t.Fatal(err)
	}
	return ok
}

// C6: a paused stage admits nothing in its project. Peek and PeekAs find
// nothing, and Admit refuses with CAP_FULL, rule stage_paused, and
// reserves no run. The stage of another project still admits. A resume
// admits again.
func TestC6PausedStageAdmitsNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	w.pause(prj, machine.StageImplementer, "hold for the upgrade")

	if c, ref := w.a.Peek(ctx, prj, machine.StageImplementer); c != nil || ref != nil {
		t.Fatalf("peek of a paused stage = %+v %+v, want nothing", c, ref)
	}
	if c, ref := w.a.PeekAs(ctx, prj, machine.StageImplementer, "run-9"); c != nil || ref != nil {
		t.Fatalf("peek as a run of a paused stage = %+v %+v, want nothing", c, ref)
	}
	_, ref := w.admit(machine.StageImplementer, id)
	if ref == nil || ref.Code != cli.CodeCapFull || ref.Rule != "stage_paused" {
		t.Fatalf("admit of a paused stage = %+v, want CAP_FULL stage_paused", ref)
	}
	if n := w.count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("the refused admission reserved %d runs", n)
	}
	if c, ref := w.a.Peek(ctx, "beta", machine.StageImplementer); c == nil || ref != nil || c.Item != id {
		t.Fatalf("peek in beta = %+v %+v, want %s", c, ref, id)
	}
	if c, ref := w.a.Peek(ctx, prj, machine.StageCodeReviewer); ref != nil {
		t.Fatalf("peek of another stage = %+v %+v", c, ref)
	}
	if _, err := Resume(ctx, w.st, prj, machine.StageImplementer, "coordinator:c-1"); err != nil {
		t.Fatal(err)
	}
	w.mustAdmit(machine.StageImplementer, id)
}

// C9: a pause of every project holds the stage in each project, and a
// pause of one project holds it there only. A resume deletes the row of
// its own scope only. Each pause and each resume writes one audit row that
// names the project, the stage and the reason.
func TestC9PauseScopesStayApart(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.pause(prj, machine.StageImplementer, "alpha only")
	if !w.paused(prj, machine.StageImplementer) || w.paused("beta", machine.StageImplementer) {
		t.Fatal("a pause of alpha does not hold alpha alone")
	}
	w.pause("", machine.StageImplementer, "every project")
	if !w.paused("beta", machine.StageImplementer) {
		t.Fatal("a pause of every project does not hold beta")
	}
	got, err := Resume(ctx, w.st, prj, machine.StageImplementer, "coordinator:c-2")
	if err != nil || got.Project != prj || got.Reason != "alpha only" {
		t.Fatalf("resume of alpha = %+v, %v", got, err)
	}
	if !w.paused(prj, machine.StageImplementer) {
		t.Fatal("a resume of alpha ended the pause of every project")
	}
	if _, err := Resume(ctx, w.st, prj, machine.StageImplementer, "coordinator:c-2"); !errors.Is(err, ErrNotPaused) {
		t.Fatalf("a second resume of alpha = %v, want ErrNotPaused", err)
	}
	if _, err := Resume(ctx, w.st, "", machine.StageImplementer, "coordinator:c-2"); err != nil {
		t.Fatal(err)
	}
	if w.paused(prj, machine.StageImplementer) || w.paused("beta", machine.StageImplementer) {
		t.Fatal("the stage stays paused after both resumes")
	}
	type row struct{ actor, project, action, detail string }
	var rows []row
	if err := w.st.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.Query(`SELECT actor, project, action, detail FROM audit ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.actor, &r.project, &r.action, &r.detail); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return rs.Err()
	}); err != nil {
		t.Fatal(err)
	}
	want := []row{
		{"coordinator:c-1", prj, "stage_paused", `{"project":"alpha","stage":"implementer","reason":"alpha only"}`},
		{"coordinator:c-1", "", "stage_paused", `{"project":"","stage":"implementer","reason":"every project"}`},
		{"coordinator:c-2", prj, "stage_resumed", `{"project":"alpha","stage":"implementer","reason":"alpha only"}`},
		{"coordinator:c-2", "", "stage_resumed", `{"project":"","stage":"implementer","reason":"every project"}`},
	}
	if len(rows) != len(want) {
		t.Fatalf("audit = %+v, want %+v", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("audit row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// C8: a second pause of one scope refuses, names the pause that holds,
// and writes no row and no audit row.
func TestC8SecondPauseOfOneScopeRefuses(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.pause(prj, machine.StageImplementer, "the first reason")
	held, err := Pause(ctx, w.st, StagePause{Project: prj, Stage: machine.StageImplementer, Reason: "a second reason", By: "coordinator:c-1"})
	if !errors.Is(err, ErrAlreadyPaused) || held.Reason != "the first reason" {
		t.Fatalf("second pause = %+v, %v; want ErrAlreadyPaused and the first reason", held, err)
	}
	if n := w.count(`SELECT count(*) FROM stage_pauses`); n != 1 {
		t.Fatalf("%d pause rows, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM audit`); n != 1 {
		t.Fatalf("%d audit rows, want 1", n)
	}
}

// C7: a pause reads back with its reason after the store closes and opens
// again with the migrations applied.
func TestC7PauseSurvivesAStoreReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loop.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	wrote, err := Pause(ctx, st, StagePause{Project: prj, Stage: machine.StageImplementer, Reason: "hold for the upgrade", By: "coordinator:c-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var got []StagePause
	if err := st.Read(ctx, func(tx *sql.Tx) error {
		got, err = Pauses(ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Project != prj || got[0].Stage != machine.StageImplementer || got[0].Reason != "hold for the upgrade" ||
		got[0].By != "coordinator:c-1" || !got[0].At.Equal(wrote.At) {
		t.Fatalf("pauses after the reopen = %+v, want %+v", got, wrote)
	}
}
