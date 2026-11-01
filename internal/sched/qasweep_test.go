package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/machine"
)

// TestC15TheSweepsFollowTheSetting proves C15 over four tickets. With the
// QA stage off, one dependency-advance tick moves the in_qa ticket that no
// run holds to ready_to_merge, and leaves the held one in in_qa. With the
// stage on, the next tick moves each ready_to_merge and queued ticket with
// no QA pass at its reviewed head to in_qa, and leaves the one whose
// qa_head equals its reviewed_head.
func TestC15TheSweepsFollowTheSetting(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	unheld := w.item("task", machine.InQA, map[string]string{machine.FactReviewedHead: "aaa111"})
	held := w.item("task", machine.InQA, map[string]string{machine.FactReviewedHead: "bbb222"})
	queued := w.item("task", machine.Queued, map[string]string{machine.FactReviewedHead: "ccc333", machine.FactQAHead: "ccc000"})
	passed := w.item("task", machine.ReadyToMerge, map[string]string{machine.FactReviewedHead: "ddd444", machine.FactQAHead: "ddd444"})
	if _, ref := w.a.Admit(ctx, admission.Request{Project: prj, Stage: machine.StageQA, Item: held, RunID: "r-qa", Harness: "claude", Model: "opus"}); ref != nil {
		t.Fatalf("admit a QA run for %s: %+v", held, ref)
	}

	w.project.set(snapshot(t, "stages:\n  implementer: {}\n  code-reviewer: {}\nmerge:\n  mechanism: direct\n", ""))
	w.advance()
	want := map[string]string{unheld: machine.ReadyToMerge, held: machine.InQA, queued: machine.Queued, passed: machine.ReadyToMerge}
	for id, st := range want {
		if got := w.status(id); got != st {
			t.Errorf("with the stage off: %s is %s, want %s", id, got, st)
		}
	}

	w.project.set(snapshot(t, "stages:\n  implementer: {}\n  code-reviewer: {}\n  qa: {}\nmerge:\n  mechanism: direct\n", ""))
	w.advance()
	want = map[string]string{unheld: machine.InQA, held: machine.InQA, queued: machine.InQA, passed: machine.ReadyToMerge}
	for id, st := range want {
		if got := w.status(id); got != st {
			t.Errorf("with the stage on: %s is %s, want %s", id, got, st)
		}
	}
}

// A config that cannot be read moves no ticket either way: a gap is no
// setting, and the tick reports the read.
func TestTheSweepsMoveNothingWithNoConfig(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	inQA := w.item("task", machine.InQA, map[string]string{machine.FactReviewedHead: "aaa111"})
	ready := w.item("task", machine.ReadyToMerge, map[string]string{machine.FactReviewedHead: "bbb222"})
	w.project.failWith(context.DeadlineExceeded)
	if err := w.s.advanceDependencies(ctx, prj); err == nil {
		t.Fatal("a tick with no config reported no error")
	}
	if w.status(inQA) != machine.InQA || w.status(ready) != machine.ReadyToMerge {
		t.Fatalf("with no config: %s, %s; want in_qa, ready_to_merge", w.status(inQA), w.status(ready))
	}
}
