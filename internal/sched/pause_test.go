package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/machine"
)

// C6: with the implementer paused in alpha, a fire of the stage checks no
// secret, takes no profile, peeks at no work and starts no run. A secret
// with no stored value raises no alert. The stage's cron row records the
// fire. The live implementer run keeps its lease, and its finish settles
// as before.
func TestC6PausedStageStartsNoRunAndALiveRunContinues(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.writeHost(nil, map[string]string{machine.StageImplementer: "0 * * * *"})
	w.rebuild()
	live := w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	if _, err := admission.Pause(ctx, w.st, admission.StagePause{Project: prj, Stage: machine.StageImplementer,
		Reason: "hold for the upgrade", By: "coordinator:c-1"}); err != nil {
		t.Fatal(err)
	}
	w.project.set(snapshot(t, "stages:\n  implementer: {}\nmerge:\n  mechanism: direct\n",
		"secrets:\n  - name: STRIPE_KEY\n    source: op://dev/stripe/key\n"))
	waiting := w.ticket(machine.Todo)

	w.clock.Set(at(t, "2026-10-21T10:00:30Z"))
	w.tick() // the first sight sets the deadline after now
	fired := at(t, "2026-10-21T11:00:30Z")
	w.clock.Set(fired)
	w.tick()

	if n := len(w.host.Launches()); n != 1 {
		t.Fatalf("%d launches, want the live run's one", n)
	}
	if n := w.count(`SELECT count(*) FROM runs`); n != 1 {
		t.Fatalf("%d run rows, want the live run's one", n)
	}
	if n := w.alerts(AlertMissingSecret); n != 0 {
		t.Fatalf("a paused stage raised %d missing_secret alerts", n)
	}
	if w.status(waiting) != machine.Todo {
		t.Fatalf("the waiting ticket is %s", w.status(waiting))
	}
	if got := w.count(`SELECT last_fired_at FROM schedule WHERE kind = 'cron' AND project = ? AND stage = ?`,
		prj, machine.StageImplementer); int64(got) != fired.UnixMilli() {
		t.Fatalf("the cron row's last fire = %d, want %d", got, fired.UnixMilli())
	}
	if w.lease(live).Owner != st.RunID {
		t.Fatalf("the live run lost its lease: %+v", w.lease(live))
	}
	res, ref := w.s.Finish(ctx, st.RunID, "done")
	if ref != nil || !res.Released {
		t.Fatalf("the live run's finish = %+v, %+v", res, ref)
	}
	if w.lease(live).Owner != "" {
		t.Fatalf("the finished run holds its lease: %+v", w.lease(live))
	}
}
