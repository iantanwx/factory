package admission

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// fire admits each item the stage's work query offers, as one firing of
// the stage does, and returns what it admitted.
func (w *world) fire(stage string) []Admitted {
	w.t.Helper()
	var out []Admitted
	for {
		c, ref := w.a.Peek(context.Background(), prj, stage)
		if ref != nil {
			w.t.Fatalf("peek %s: %+v", stage, ref)
		}
		if c == nil {
			return out
		}
		out = append(out, w.mustAdmit(stage, c.Item))
	}
}

// C14: with stages.qa.cap: 2 and five tickets in in_qa, one firing admits
// two QA runs. The third admits after one of them settles.
func TestC14NoMoreQARunsThanTheCap(t *testing.T) {
	w := newWorld(t)
	w.configs.set(prj, stageCaps(map[string]int{machine.StageQA: 2}))
	for range 5 {
		w.ticket(machine.InQA)
	}
	first := w.fire(machine.StageQA)
	if len(first) != 2 {
		t.Fatalf("the first firing admitted %d QA runs, want 2", len(first))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE ended_at IS NULL AND stage = ?`, machine.StageQA); n != 2 {
		t.Fatalf("live QA runs = %d, want 2", n)
	}
	w.settle(first[0], Settlement{Finish: "done"})
	if next := w.fire(machine.StageQA); len(next) != 1 {
		t.Fatalf("the firing after a settle admitted %d QA runs, want 1", len(next))
	}
}

// A push to the pull request of a ticket in in_qa is new input to the QA
// stage, as it is to the code-reviewer. So two failed QA runs hold the
// ticket only until the next head.
func TestAPushReadmitsAHeldQARun(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.InQA)
	w.heads.push(id, "h1")
	for range 2 {
		w.settle(w.mustAdmit(machine.StageQA, id), Settlement{Finish: "failed"})
	}
	_, ref := w.admit(machine.StageQA, id)
	wantRefusal(t, ref, cli.CodeRetryLimit)
	w.heads.push(id, "h2")
	if ad := w.mustAdmit(machine.StageQA, id); ad.Attempt != 1 {
		t.Fatalf("attempt at the new head = %d, want 1", ad.Attempt)
	}
}
