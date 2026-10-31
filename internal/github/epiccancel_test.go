package github

import (
	"context"
	"strings"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// C-bug-2: the cancel of an epic closes its open document PR with no
// merge. A PR that is already closed is no fault, and gets no write.
func TestC2EpicCancelClosesItsOpenDocumentPR(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	r := e.reactor()
	e.m.SetDocPRCloser(r.CloseDocPR)
	open := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	ep := e.epicInReview(open.Number)
	e.apply(ep, "cancel", map[string]string{"reason": "ADR 0013 gave the work to factorio-yzrt."})
	if got := w.PR(open.Number); got.State != "closed" || got.Merged {
		t.Fatalf("after the cancel the PR is %s, merged %v", got.State, got.Merged)
	}
	shut := w.Add(&gt.PR{Head: "loop/placeholder-2", HeadSHA: shaA, State: "closed"})
	other := e.epicInReview(shut.Number)
	before := len(e.api.Calls())
	e.apply(other, "cancel", map[string]string{"reason": "dropped"})
	for _, c := range e.api.Calls()[before:] {
		if strings.HasPrefix(c, "PATCH ") {
			t.Fatalf("the cancel wrote to a closed PR: %s", c)
		}
	}
	if got := e.status(other).Status; got != machine.Canceled {
		t.Fatalf("the epic is %s", got)
	}
}

// C-bug-2: a cancel whose close never ran, as after a crash, closes the
// PR at the next start-up.
func TestC2StartupClosesTheDocumentPROfACanceledEpic(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	pr := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	ep := e.epicInReview(pr.Number)
	e.apply(ep, "cancel", map[string]string{"reason": "dropped"})
	if got := w.PR(pr.Number).State; got != "open" {
		t.Fatalf("with no closer the PR is %s", got)
	}
	if err := NewReactor(e.svc).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.PR(pr.Number); got.State != "closed" || got.Merged {
		t.Fatalf("after the start-up the PR is %s, merged %v", got.State, got.Merged)
	}
	if got := e.status(ep).Status; got != machine.Canceled {
		t.Fatalf("the start-up moved the epic to %s", got)
	}
}
