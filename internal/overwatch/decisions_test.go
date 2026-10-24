package overwatch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
)

// jamTrigger is the trigger of the factorio-jam fixture.
const jamTrigger = "a loop release that holds the fix of factorio-88t ships"

// jamNote is the note the decisions part prints for factorio-jam.
const jamNote = "decision factorio-jam waits on its trigger \"" + jamTrigger +
	"\"; no by date, so only a wake reads the trigger. If the event happened, run `loop decision fire factorio-jam`"

// jamWorld is the factorio-jam fixture: a world whose only content is one
// decision that waits on an event, with no by date (spec, Decisions, "A
// decision that waits on its event is a note").
func jamWorld(t *testing.T) *fakeWorld {
	t.Helper()
	w := newFakeWorld(t)
	w.add("factorio", machine.Item{ID: "factorio-jam", Kind: machine.KindDecision, Status: machine.DecisionPending,
		Title: "No host upgrade until factorio-88t ships", Facts: map[string]string{inbox.FactTrigger: jamTrigger}},
		"Hold every host upgrade until the fix ships.")
	return w
}

// C13: a decision with no by date, swept at three ticks, gives code 0 and
// the same note each time.
func TestC13ADecisionThatWaitsOnItsEventIsANote(t *testing.T) {
	w := jamWorld(t)
	for range 3 {
		pr := sweepWorkItemPart(t, w, "decisions")
		if pr.Code != Quiet || !slices.Equal(pr.Lines, []string{jamNote}) {
			t.Fatalf("at %s: %+v", w.now, pr)
		}
		w.now = w.now.Add(10 * time.Minute)
	}
}

// C13: a decision with a by date is a note that states it, a decision in
// any other state prints no line, and a failed read is code 2.
func TestC13ADatedDecisionIsANoteAndOtherStatesPrintNothing(t *testing.T) {
	w := newFakeWorld(t)
	w.add("alpha", machine.Item{ID: "alpha-d1", Kind: machine.KindDecision, Status: machine.DecisionPending,
		Facts: map[string]string{inbox.FactTrigger: "the trial ends", inbox.FactBy: "2026-11-01"}}, "")
	want := []string{"decision alpha-d1 waits on its trigger \"the trial ends\"; by 2026-11-01, when decision-timers fires it. " +
		"If the event happened, run `loop decision fire alpha-d1`"}
	if pr := sweepWorkItemPart(t, w, "decisions"); pr.Code != Quiet || !slices.Equal(pr.Lines, want) {
		t.Fatalf("a dated decision: %+v", pr)
	}

	w = newFakeWorld(t)
	for _, status := range []string{machine.NeedsHuman, machine.Done, machine.Canceled} {
		w.add("factorio", machine.Item{ID: "factorio-d-" + status, Kind: machine.KindDecision, Status: status,
			Facts: map[string]string{inbox.FactTrigger: "the trial ends"}}, "")
	}
	if pr := sweepWorkItemPart(t, w, "decisions"); pr.Code != Quiet || len(pr.Lines) != 0 {
		t.Fatalf("decisions out of pending: %+v", pr)
	}

	w = jamWorld(t)
	w.itemsErr = errors.New("beads is down")
	if pr := sweepWorkItemPart(t, w, "decisions"); pr.Code != Unread || len(pr.Lines) != 1 || !strings.Contains(pr.Lines[0], "beads is down") {
		t.Fatalf("a failed read: %+v", pr)
	}
}

// C13: a world whose only content is the factorio-jam fixture ticks three
// times with the shipped parts and rings nobody, and each tick's report
// holds the note. The host around it is healthy: the coordinator session
// runs, and the doctor passes, so the inbox and server parts read quiet.
func TestC13TheJamFixtureTicksThreeTimesAndRingsNobody(t *testing.T) {
	w := newReachWorld(t)
	w.fakeWorld = jamWorld(t)
	w.lines = []github.CheckLine{{Check: "install", OK: true, Detail: "the install matches loop install"}}
	d := &doorbell{}
	j := &Job{World: w, Parts: Parts(), Ring: d.ring}
	for range 3 {
		if err := j.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		r, ok := j.Last()
		if !ok || r.Code != Quiet {
			t.Fatalf("the tick's report = %+v", r)
		}
		i := slices.IndexFunc(r.Parts, func(p PartReport) bool { return p.Name == "decisions" })
		if i < 0 || !slices.Equal(r.Parts[i].Lines, []string{jamNote}) {
			t.Fatalf("the tick's report holds no decisions note: %+v", r.Parts)
		}
		w.now = w.now.Add(10 * time.Minute)
	}
	if len(d.texts) != 0 || ringRows(t, w.fakeWorld) != 0 {
		t.Fatalf("the jam fixture rang %q", d.texts)
	}
}
