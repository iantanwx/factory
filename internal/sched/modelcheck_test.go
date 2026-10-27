package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/machine"
)

// C4: each stage start asks the host to check the model receipt, on a
// plain profile as on an escalated one, and for an agent job with no item.
func TestC4EveryStageStartRequestsTheCheck(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, nightlyQA)
	w.project.set(snapshot(t, "stages:\n  implementer: {}\n  code-reviewer: {}\n  nightly-qa: {}\nmerge:\n  mechanism: direct\n", ""))
	w.rebuild()
	w.ticket(machine.Todo)
	w.ticket(machine.InReview)
	for _, stage := range []string{machine.StageImplementer, machine.StageCodeReviewer, "nightly-qa"} {
		w.dispatch(stage)
	}
	l := w.host.Launches()
	if len(l) != 3 {
		t.Fatalf("%d launches, want 3", len(l))
	}
	for i, ln := range l {
		if !ln.Spec.RequireVerifiedModel {
			t.Errorf("start %d (%s) does not request the model check", i, ln.Spec.Title)
		}
	}
}

// C5: a start whose harness records another model settles refused and
// counts nothing. The item is back in the state it was claimed from, with
// its lease free. Two mismatches of one stage leave one open alert with
// count 2, and a mismatch at another stage opens a second one.
func TestC5MismatchStartCountsNothingAndRaisesOneAlertForEachStage(t *testing.T) {
	w := newWorld(t)
	w.host.SetDropModel(true)
	id := w.ticket(machine.Todo)
	for n := range 2 {
		st, ref := w.s.Dispatch(context.Background(), prj, machine.StageImplementer)
		if ref == nil || st != nil {
			t.Fatalf("mismatch start %d = %+v, %+v; want a refusal", n, st, ref)
		}
		if w.status(id) != machine.Todo || w.lease(id).Owner != "" || w.failures(id) != 0 {
			t.Fatalf("after mismatch %d: status %s, lease %+v, failures %d", n, w.status(id), w.lease(id), w.failures(id))
		}
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE outcome_class = 'refused' AND ended_at IS NOT NULL`); n != 2 {
		t.Fatalf("%d refused runs, want 2", n)
	}
	if n := w.count(`SELECT count(*) FROM alerts WHERE kind = 'model_mismatch' AND state = 'open' AND dedupe_key = 'model_mismatch:alpha/implementer' AND count = 2`); n != 1 {
		t.Fatalf("%d open implementer model_mismatch alerts with count 2, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM alerts WHERE kind = 'run_start_failed'`); n != 0 {
		t.Fatalf("a mismatch raised %d run_start_failed alerts", n)
	}
	for _, ln := range w.host.Launches() {
		if st, _ := w.host.Settle(context.Background(), ln.ID); st.Liveness == agenthost.Live {
			t.Fatal("a mismatched session still runs")
		}
	}

	w.ticket(machine.InReview)
	if st, ref := w.s.Dispatch(context.Background(), prj, machine.StageCodeReviewer); ref == nil || st != nil {
		t.Fatalf("code-reviewer mismatch start = %+v, %+v; want a refusal", st, ref)
	}
	if n := w.count(`SELECT count(*) FROM alerts WHERE kind = 'model_mismatch' AND state = 'open'`); n != 2 {
		t.Fatalf("%d open model_mismatch alerts after a second stage, want 2", n)
	}
}

// C6: a start whose harness names no model in the receipt window runs the
// session, records it, and raises one model_receipt_missing alert for the
// project and stage.
func TestC6MissingReceiptRunsTheSession(t *testing.T) {
	w := newWorld(t)
	w.host.SetSilentModel(true)
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	if state, _ := w.runState(st.RunID); state != RunRunning {
		t.Fatalf("run state = %s, want running", state)
	}
	if rec := decodeRecord(w.str(`SELECT host_identity FROM runs WHERE run_id = ?`, st.RunID)); rec.Session == nil || rec.Session.ID != st.Session.ID {
		t.Fatalf("session record = %+v, want %+v", rec.Session, st.Session.ID)
	}
	if s, _ := w.host.Settle(context.Background(), st.Session.ID); s.Liveness != agenthost.Live {
		t.Fatalf("session liveness = %s, want live", s.Liveness)
	}
	if n := w.count(`SELECT count(*) FROM alerts WHERE kind = 'model_receipt_missing' AND state = 'open' AND dedupe_key = 'model_receipt_missing:alpha/implementer'`); n != 1 {
		t.Fatalf("%d open model_receipt_missing alerts, want 1", n)
	}
}

// finishingHost finishes the run inside Start, as a run that finishes
// while its host waits for the model receipt does.
type finishingHost struct {
	*fake.Host
	finish func()
}

func (h finishingHost) Start(ctx context.Context, spec agenthost.StartSpec) (agenthost.Session, error) {
	sess, err := h.Host.Start(ctx, spec)
	h.finish()
	return sess, err
}

// C6: a run that finishes during the receipt wait keeps its settlement
// and its session record, so the next tick closes the session.
func TestC6RunThatFinishesDuringTheWaitKeepsItsRecord(t *testing.T) {
	w := newWorld(t)
	w.host.SetSilentModel(true)
	w.cfg.Agents = finishingHost{Host: w.host, finish: func() {
		run := w.str(`SELECT run_id FROM runs WHERE ended_at IS NULL`)
		if _, ref := w.s.Finish(context.Background(), run, "done"); ref != nil {
			t.Errorf("finish %s: %+v", run, ref)
		}
	}}
	w.rebuild()
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	state, class := w.runState(st.RunID)
	if state == RunRunning || class == "" {
		t.Fatalf("the finished run reads state %s, class %q", state, class)
	}
	rec := decodeRecord(w.str(`SELECT host_identity FROM runs WHERE run_id = ?`, st.RunID))
	if rec.Session == nil || rec.Session.ID != st.Session.ID {
		t.Fatalf("session record = %+v, want %+v", rec.Session, st.Session.ID)
	}
	w.tick()
	if s, _ := w.host.Settle(context.Background(), st.Session.ID); s.Liveness == agenthost.Live {
		t.Fatal("the next tick left the finished run's session running")
	}
}
