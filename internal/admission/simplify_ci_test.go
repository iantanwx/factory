package admission

import (
	"context"
	"sync"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// fakeCI is the CI state of each head. A head it does not list has no
// check yet, so it is pending.
type fakeCI struct {
	mu    sync.Mutex
	state map[string]CIState
	calls int
}

func (f *fakeCI) set(head string, s CIState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[head] = s
}

func (f *fakeCI) read(_ context.Context, _, head string) (CIState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.state[head], nil
}

// peekReviewer returns the item the code-reviewer's work query offers, or
// "" when it offers none.
func (w *world) peekReviewer() string {
	w.t.Helper()
	c, ref := w.a.Peek(context.Background(), prj, machine.StageCodeReviewer)
	if ref != nil {
		w.t.Fatalf("peek: %+v", ref)
	}
	if c == nil {
		return ""
	}
	return c.Item
}

// C13: a ticket in in_review whose live head differs from its
// simplify_from waits outside the code-reviewer while CI there is pending
// or red, and admission admits it once CI is green.
func TestC13TheCodeReviewerWaitsForGreenCIAtASimplifyHead(t *testing.T) {
	w := newWorld(t)
	ci := &fakeCI{state: map[string]CIState{}}
	w.a.SetCIReader(ci.read)
	id := w.ticket(machine.InReview)
	w.fact(id, machine.FactSimplifyFrom, "impl")
	w.heads.push(id, "simplified")

	for _, s := range []CIState{CIPending, CIRed} {
		ci.set("simplified", s)
		if got := w.peekReviewer(); got != "" {
			t.Fatalf("CI %v at the simplify head: the work query offers %s, want nothing", s, got)
		}
		_, ref := w.admit(machine.StageCodeReviewer, id)
		wantRefusal(t, ref, cli.CodeCINotGreen)
	}
	if n := w.count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("runs = %d, want 0: a wait reserved a run", n)
	}

	ci.set("simplified", CIGreen)
	if got := w.peekReviewer(); got != id {
		t.Fatalf("CI green at the simplify head: the work query offers %q, want %s", got, id)
	}
	w.mustAdmit(machine.StageCodeReviewer, id)
}

// C13: a ticket whose live head equals its simplify_from, or that has no
// simplify_from, enters review as today, with no CI wait.
func TestC13NoCIWaitAtTheImplementersHead(t *testing.T) {
	w := newWorld(t)
	ci := &fakeCI{state: map[string]CIState{}}
	w.a.SetCIReader(ci.read)
	unchanged := w.ticket(machine.InReview)
	w.fact(unchanged, machine.FactSimplifyFrom, "impl1")
	w.heads.push(unchanged, "impl1")
	unset := w.ticket(machine.InReview)
	w.heads.push(unset, "impl2")

	w.mustAdmit(machine.StageCodeReviewer, unchanged)
	w.mustAdmit(machine.StageCodeReviewer, unset)
	if ci.calls != 0 {
		t.Fatalf("CI reads = %d, want 0", ci.calls)
	}
}
