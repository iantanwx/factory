package sched

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// C2: with the simplify stage registered and StageEnabled unchanged, a
// project config that does not list simplify gets its firing, disabled, on
// the stage's default schedule (docs/plans/factorio-1r94-simplify-stage-spec.md).
func TestC2AConfigWithNoSimplifyEntryFiresNoSimplifyRun(t *testing.T) {
	sn := snapshot(t, "stages:\n  implementer: {}\n  code-reviewer: {}\nmerge:\n  mechanism: direct\n", "")
	firings := Firings(config.DefaultHost(t.TempDir()), []config.Project{{ID: prj}},
		func(string) *config.ProjectSnapshot { return sn }, Jobs{})
	var got []Firing
	for _, f := range firings {
		if f.Stage == "simplify" {
			got = append(got, f)
		}
	}
	want := Firing{Project: prj, Stage: "simplify", Kind: FiringRun, Enabled: false, Schedule: "1,6,11,16,21,26,31,36,41,46,51,56 * * * *"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("simplify firings = %+v, want [%+v]", got, want)
	}
}

// The simplify run edits code that ships, so it runs at high effort, as
// every code author does.
func TestSimplifyRunsAtHighEffortByDefault(t *testing.T) {
	pc, err := config.ParseProjectConfig([]byte("merge:\n  mechanism: direct\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := profiles("simplify", pc, config.DefaultHost(t.TempDir()))[0].Effort; got != "high" {
		t.Fatalf("simplify effort = %q, want high", got)
	}
}

const simplifyFrom = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// withSimplify makes the project list the simplify stage, enabled or not.
func (w *world) withSimplify(enabled bool) {
	w.t.Helper()
	stage := "  simplify: {}\n"
	if !enabled {
		stage = "  simplify: {enabled: false}\n"
	}
	w.project.set(snapshot(w.t, "stages:\n  implementer: {}\n"+stage+"  code-reviewer: {}\nmerge:\n  mechanism: direct\n", ""))
}

// waitingForSimplify makes a ticket that waits in in_simplify, with its
// pull request at simplifyFrom.
func (w *world) waitingForSimplify() string {
	w.t.Helper()
	id := w.ticket("in_simplify")
	is, err := w.bx.Get(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.bx.Update(context.Background(), id, beadsx.UpdateSpec{IfStatus: is.Status,
		SetMetadata: map[string]string{"loop.head": simplifyFrom, "loop.simplify_from": simplifyFrom}}); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// passedThrough fails the test unless the ticket waits in in_review at
// simplifyFrom, and the run left no failure row and no hold.
func (w *world) passedThrough(id, run string) {
	w.t.Helper()
	it, ref := w.m.Show(context.Background(), prj, id)
	if ref != nil {
		w.t.Fatal(ref)
	}
	if it.Status != machine.InReview || it.Owner != "" || it.Fact(machine.FactHead) != simplifyFrom {
		w.t.Fatalf("after run %s: %s owner %q facts %v", run, it.Status, it.Owner, it.Facts)
	}
	if n := w.count(`SELECT count(*) FROM failures WHERE run_id = ?`, run); n != 0 {
		w.t.Fatalf("run %s left %d failure rows", run, n)
	}
	if n := w.alerts(AlertRunBlocked) + w.alerts(AlertRetryLimit); n != 0 {
		w.t.Fatalf("run %s raised %d hold alerts", run, n)
	}
}

// runMove starts a run of stage, applies verb at simplifyFrom as that run,
// and finishes it done.
func (w *world) runMove(stage, verb string) *Started {
	w.t.Helper()
	ctx := context.Background()
	st := w.dispatch(stage)
	res := w.m.Apply(ctx, machine.Request{Project: prj, Item: st.Item, Verb: verb, Params: map[string]string{"head": simplifyFrom},
		Actor: machine.Actor{Name: "run:" + st.RunID, RunID: st.RunID, Fenced: true, Epoch: w.lease(st.Item).Epoch}})
	if res.Refusal != nil {
		w.t.Fatalf("%s run %s: %s: %+v", stage, st.RunID, verb, res.Refusal)
	}
	if _, ref := w.s.Finish(ctx, st.RunID, "done"); ref != nil {
		w.t.Fatal(ref)
	}
	return st
}

// C7: every end of a simplify run that pushed nothing passes the ticket
// to review at simplify_from, with no count and no hold.
func TestC7EveryOtherEndPassesTheTicketThrough(t *testing.T) {
	ctx := context.Background()
	for _, end := range []string{"failed", "blocked", "done", "crash"} {
		t.Run(end, func(t *testing.T) {
			w := newWorld(t)
			w.withSimplify(true)
			id := w.waitingForSimplify()
			st := w.dispatch(config.StageSimplify)
			if end == "crash" {
				w.host.Exit(st.Session.ID, 1)
				w.tick()
			} else if _, ref := w.s.Finish(ctx, st.RunID, end); ref != nil {
				t.Fatal(ref)
			}
			w.passedThrough(id, st.RunID)
		})
	}
}

// C7: a reject with unchanged inputs admits a simplify run again, round
// after round, because no round counts.
func TestC7EachRejectAdmitsAnotherSimplifyRound(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.withSimplify(true)
	id := w.waitingForSimplify()
	for round := range 3 {
		st := w.dispatch(config.StageSimplify)
		if st.Item != id {
			t.Fatalf("round %d started on %s", round, st.Item)
		}
		if _, ref := w.s.Finish(ctx, st.RunID, "failed"); ref != nil {
			t.Fatal(ref)
		}
		w.passedThrough(id, st.RunID)
		w.runMove(machine.StageCodeReviewer, "review_reject")
		w.runMove(machine.StageImplementer, "pr_open")
		if got := w.status(id); got != "in_simplify" {
			t.Fatalf("round %d: after the reject and the rework: %s", round, got)
		}
	}
}

// C10: in a project that disables the simplify stage, one tick of
// dependency-advance passes each waiting ticket that no run holds to
// review. A held ticket waits for its run. In a project that enables the
// stage, the tick moves nothing.
func TestC10ADisabledStagePassesEachWaitingTicketOn(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.withSimplify(true)
	held := w.waitingForSimplify()
	run := w.dispatch(config.StageSimplify)
	free := w.waitingForSimplify()
	w.withSimplify(false)
	if err := w.s.advanceDependencies(ctx, prj); err != nil {
		t.Fatal(err)
	}
	if got := w.status(free); got != machine.InReview {
		t.Fatalf("the free ticket of a disabled stage: %s, want in_review", got)
	}
	if got, owner := w.status(held), w.lease(held).Owner; got != "in_simplify" || owner != run.RunID {
		t.Fatalf("the held ticket: %s owner %q, want in_simplify owner %s", got, owner, run.RunID)
	}

	w.withSimplify(true)
	waiting := w.waitingForSimplify()
	if err := w.s.advanceDependencies(ctx, prj); err != nil {
		t.Fatal(err)
	}
	if got := w.status(waiting); got != "in_simplify" {
		t.Fatalf("a ticket of an enabled stage: %s, want in_simplify", got)
	}
}

// C11: a simplify run starts clean after the implementer's run on the
// same ticket: a new session in a new checkout, and a brief that names
// the stage and its runbook and holds nothing of the implementer's run.
func TestC11ASimplifyRunStartsClean(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.withSimplify(true)
	id := w.ticket(machine.Todo)
	if _, err := w.bx.Update(ctx, id, beadsx.UpdateSpec{IfStatus: machine.Todo,
		SetMetadata: map[string]string{"loop.simplify_due": "true"}}); err != nil {
		t.Fatal(err)
	}
	impl := w.runMove(machine.StageImplementer, "pr_open")

	st := w.dispatch(config.StageSimplify)
	if st.Item != id || st.Checkout == impl.Checkout || st.Session.ID == impl.Session.ID {
		t.Fatalf("simplify run %+v reuses the implementer's %+v", st, impl)
	}
	b, md := briefFiles(t, st)
	if b["stage"] != "simplify" || b["runbook"] != ".claude/skills/loop-simplify/SKILL.md" {
		t.Fatalf("brief stage %v runbook %v", b["stage"], b["runbook"])
	}
	if strings.Contains(md, impl.RunID) || strings.Contains(md, impl.Checkout) {
		t.Fatalf("the brief names the implementer's run:\n%s", md)
	}
}

// C18: every end of a simplify run drops the commit the server holds for
// it, so a commit that no hand-off pushed never reaches GitHub. The
// settlement of an implementer run drops nothing.
func TestC18SettlementDropsTheHeldCommit(t *testing.T) {
	ctx := context.Background()
	for _, end := range []string{"failed", "blocked", "done", "crash"} {
		t.Run(end, func(t *testing.T) {
			w := newWorld(t)
			var dropped []string
			w.s.SetHeldDropper(func(_ context.Context, project, run string) error {
				dropped = append(dropped, project+"/"+run)
				return nil
			})
			w.withSimplify(true)
			id := w.waitingForSimplify()
			st := w.dispatch(config.StageSimplify)
			if end == "crash" {
				w.host.Exit(st.Session.ID, 1)
				w.tick()
			} else if _, ref := w.s.Finish(ctx, st.RunID, end); ref != nil {
				t.Fatal(ref)
			}
			if want := []string{prj + "/" + st.RunID}; !slices.Equal(dropped, want) {
				t.Fatalf("dropped %v, want %v", dropped, want)
			}
			w.passedThrough(id, st.RunID)

			dropped = nil
			w.runMove(machine.StageCodeReviewer, "review_reject")
			w.runMove(machine.StageImplementer, "pr_open")
			if len(dropped) != 0 {
				t.Fatalf("the settlement of other stages dropped %v", dropped)
			}
		})
	}
}
