package machine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// TestEpicCancelsBeforeExecution proves factorio-bphx: an epic in any state
// before execution cancels with a reason into a terminal canceled state.
func TestEpicCancelsBeforeExecution(t *testing.T) {
	for _, from := range []string{Backlog, PRDNeeded, PRDReview, PRDAmending, PRDSignoff, Architecture, SpecNeeded,
		SpecReview, SpecAmending, SpecSignoff, SpecApproved, Escalated} {
		t.Run(from, func(t *testing.T) {
			w := newWorld(t)
			ep := w.create(KindEpic, VerbCreateArchitecture, nil)
			w.force(ep, from, nil)
			w.must(ep, "cancel", map[string]string{"reason": "ADR 0013 gave the work to factorio-yzrt."})
			got := w.show(ep)
			if got.Status != Canceled {
				t.Fatalf("status %q, want %q", got.Status, Canceled)
			}
			tbl := epicTable()
			if !tbl.Terminal(Canceled) {
				t.Fatalf("canceled is not terminal for an epic")
			}
			hist := w.history(ep)
			if last := hist[len(hist)-1]; last.Set[factPrefix+FactCancelReason] != "ADR 0013 gave the work to factorio-yzrt." {
				t.Fatalf("the cancel keeps no reason: %+v", last)
			}
		})
	}
}

// TestC1EpicCancelsUntilDecomposing proves C-bug-1 for the drafting
// states and the edge: the coordinator cancels an epic whose PRD a run
// drafts, which ends the run's lease, and the run that drafts a spec
// cancels its own epic. An epic in decomposing, execution, done or
// canceled refuses work cancel, and stays.
func TestC1EpicCancelsUntilDecomposing(t *testing.T) {
	w := newWorld(t)
	reason := map[string]string{"reason": "ADR 0013 gave the work to factorio-yzrt."}
	prd := w.create(KindEpic, VerbCreatePRD, nil)
	w.claimAs(prd, StagePRDAuthor, "prd")
	mustOK(t, w.call("work cancel", coord(), []string{prd}, reason))
	if it := w.show(prd); it.Status != Canceled || it.Owner != "" {
		t.Fatalf("after the coordinator's cancel: %s, owner %q", it.Status, it.Owner)
	}
	spec := w.create(KindEpic, VerbCreateArchitecture, nil)
	w.force(spec, SpecNeeded, map[string]string{FactADR: "docs/adr/0013-x.md"})
	run := w.claimAs(spec, StagePlanner, "spec")
	mustOK(t, w.call("work cancel", run, []string{spec}, reason))
	if it := w.show(spec); it.Status != Canceled || it.Owner != "" || it.Fact(FactCancelReason) != reason["reason"] {
		t.Fatalf("after the run's cancel: %s, owner %q, facts %v", it.Status, it.Owner, it.Facts)
	}
	for _, from := range []string{Decomposing, Execution, Done, Canceled} {
		ep := w.create(KindEpic, VerbCreateArchitecture, nil)
		w.force(ep, from, nil)
		wantCode(t, w.call("work cancel", coord(), []string{ep}, reason), cli.CodeTransitionNotAllowed)
		if got := w.show(ep).Status; got != from {
			t.Fatalf("after the refused cancel from %s: %s", from, got)
		}
	}
}

type prCloseLog struct {
	calls []string
	fail  error
}

func (l *prCloseLog) close(_ context.Context, project string, pr int) error {
	l.calls = append(l.calls, fmt.Sprintf("%s#%d", project, pr))
	return l.fail
}

// TestC2EpicCancelClosesItsDocumentPR proves C-bug-2: the cancel of an
// epic that records a document pull request closes that pull request. An
// epic with none closes nothing, and a close that fails leaves the epic
// canceled.
func TestC2EpicCancelClosesItsDocumentPR(t *testing.T) {
	w := newWorld(t)
	log := &prCloseLog{}
	w.m.SetDocPRCloser(log.close)
	reason := map[string]string{"reason": "ADR 0013 gave the work to factorio-yzrt."}
	ep := w.create(KindEpic, VerbCreateArchitecture, nil)
	w.force(ep, SpecReview, map[string]string{FactDocPR: "7"})
	mustOK(t, w.call("work cancel", coord(), []string{ep}, reason))
	if !slices.Equal(log.calls, []string{"alpha#7"}) {
		t.Fatalf("closed %v, want [alpha#7]", log.calls)
	}
	bare := w.create(KindEpic, VerbCreateArchitecture, nil)
	mustOK(t, w.call("work cancel", coord(), []string{bare}, reason))
	if len(log.calls) != 1 {
		t.Fatalf("an epic with no document PR closed %v", log.calls[1:])
	}
	log.fail = errors.New("github: 502")
	down := w.create(KindEpic, VerbCreateArchitecture, nil)
	w.force(down, PRDReview, map[string]string{FactDocPR: "9"})
	mustOK(t, w.call("work cancel", coord(), []string{down}, reason))
	if got := w.show(down).Status; got != Canceled || !slices.Contains(log.calls, "alpha#9") {
		t.Fatalf("after a failed close: %s, closed %v", got, log.calls)
	}
}
