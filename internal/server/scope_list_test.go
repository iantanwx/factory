package server

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// listed is the value of field in each row that checkout's call lists.
func (w *scopeWorld) listed(checkout, field string, args ...string) []string {
	w.t.Helper()
	env, code := w.as(checkout, args...)
	out := []string{}
	if code == 1 {
		return out
	}
	if code != 0 {
		w.t.Fatalf("%v = %d %v", args, code, env)
	}
	for _, row := range env["data"].([]any) {
		v, _ := row.(map[string]any)[field].(string)
		out = append(out, v)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// C11: alpha's coordinator pauses a stage with no --project, and the stage
// pauses in alpha only, so beta's implementer still starts a run. Its
// resume with no --project ends that pause. The loop project's
// coordinator, with no --project, pauses the stage in alpha and in beta.
func TestC11APauseWithNoProjectActsOnTheCallersProject(t *testing.T) {
	w := newScopeWorld(t)
	paused := func(project string) bool {
		t.Helper()
		ok, err := w.srv.Admission.Paused(context.Background(), project, "implementer")
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if env, code := w.as(w.alpha, "stage", "pause", "implementer", "--reason", "the operator asked"); code != 0 {
		t.Fatalf("stage pause from alpha's coordinator = %d %v", code, env)
	}
	if !paused("alpha") || paused("beta") || paused("loop") {
		t.Fatalf("paused in alpha %v, beta %v, loop %v; want alpha only", paused("alpha"), paused("beta"), paused("loop"))
	}
	if env, code := w.as(w.alpha, "stage", "resume", "implementer"); code != 0 {
		t.Fatalf("stage resume from alpha's coordinator = %d %v", code, env)
	}
	if paused("alpha") {
		t.Fatal("the resume left alpha paused")
	}
	if env, code := w.as(w.lp, "stage", "pause", "implementer", "--reason", "the operator asked"); code != 0 {
		t.Fatalf("stage pause from the loop project's coordinator = %d %v", code, env)
	}
	if !paused("alpha") || !paused("beta") {
		t.Fatalf("paused in alpha %v, beta %v; want both", paused("alpha"), paused("beta"))
	}
	if n := w.count(`SELECT count(*) FROM stage_pauses WHERE project = '' AND stage = 'implementer'`); n != 1 {
		t.Fatalf("pauses of every project = %d, want 1", n)
	}
}

// Spec work entry 4: a list verb lists to alpha's coordinator the rows of
// alpha only. The old session lists every project's rows, as before.
func TestAProjectsCoordinatorListsItsOwnRows(t *testing.T) {
	w := newScopeWorld(t)
	// The world sets up no GitHub, so no hand-off job serves merge status.
	if err := w.srv.RPC.Handle("merge status", mergeStatus(w.srv)); err != nil {
		t.Fatal(err)
	}
	w.exec(`INSERT INTO merge_tracking (project, pr_number, item, state, updated_at) VALUES ('alpha', 1, ?, 'handed', 1), ('beta', 2, ?, 'handed', 1)`,
		w.ticket["alpha"], w.ticket["beta"])
	for _, tc := range []struct {
		args       []string
		alpha, old []string
	}{
		{[]string{"run", "list"}, []string{"alpha"}, []string{"alpha", "beta"}},
		{[]string{"fault", "list"}, []string{"alpha"}, []string{"alpha", "beta"}},
		{[]string{"room", "list"}, []string{}, []string{"beta"}},
		{[]string{"merge", "status"}, []string{"alpha"}, []string{"alpha", "beta"}},
		{[]string{"stage", "list"}, []string{"alpha"}, []string{"", "alpha", "beta", "loop"}},
		{[]string{"project", "list"}, []string{"alpha"}, []string{"alpha", "beta", "loop"}},
	} {
		name := strings.Join(tc.args, " ")
		field := "project"
		if name == "project list" {
			field = "id"
		}
		if got := w.listed(w.alpha, field, tc.args...); !slices.Equal(got, tc.alpha) {
			t.Errorf("%s from alpha's coordinator lists the projects %q, want %q", name, got, tc.alpha)
		}
		if got := w.listed(w.coord, field, tc.args...); !slices.Equal(got, tc.old) {
			t.Errorf("%s from the old session lists the projects %q, want %q", name, got, tc.old)
		}
	}
}

// Spec work entry 4: the loop project's coordinator lists every project's
// registration, the instance's own rows, and a row of another project
// only when an entry that rings it names the row.
func TestTheLoopProjectsCoordinatorListsTheRowsItReaches(t *testing.T) {
	w := newScopeWorld(t)
	if got := w.listed(w.lp, "id", "project", "list"); !slices.Equal(got, []string{"alpha", "beta", "loop"}) {
		t.Fatalf("project list = %q, want every project", got)
	}
	if got := w.listed(w.lp, "project", "stage", "list"); !slices.Equal(got, []string{"", "loop"}) {
		t.Fatalf("stage list lists the projects %q, want the instance's and the loop project's", got)
	}
	// Each project's loop fault rings the loop project's coordinator, and
	// names the run that reported it.
	if got := w.listed(w.lp, "run", "run", "list"); !slices.Equal(got, []string{w.runOf["alpha"], w.runOf["beta"]}) {
		t.Fatalf("run list = %q, want the runs the faults name", got)
	}
	if env, code := w.as(w.lp, "fault", "resolve", w.faultOf["alpha"], "--fixed-in", "v9.9.9"); code != 0 {
		t.Fatalf("fault resolve = %d %v", code, env)
	}
	if got := w.listed(w.lp, "run", "run", "list"); !slices.Equal(got, []string{w.runOf["beta"]}) {
		t.Fatalf("run list after the resolve = %q, want beta's run only", got)
	}
}
