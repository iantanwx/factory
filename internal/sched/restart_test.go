package sched

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
)

var alphaAndBeta = map[string]bool{"alpha": true, "beta": true}

// withLeftOpen makes the left-open list of each project one question, so
// a prompt that names it holds the list.
func (hw *homeWorld) withLeftOpen() {
	hw.s.SetLeftOpen(func(_ context.Context, project string) ([]string, error) {
		return []string{"question/4 of " + project}, nil
	})
}

// lastLaunch is the spec of the newest start, and the text of its prompt.
func (hw *homeWorld) lastLaunch() (agenthost.StartSpec, string) {
	hw.t.Helper()
	ls := hw.host.Launches()
	spec := ls[len(ls)-1].Spec
	raw, err := os.ReadFile(spec.PromptFile)
	if err != nil {
		hw.t.Fatal(err)
	}
	return spec, string(raw)
}

// sameSession fails the test unless project's coordinator is still cs.
func (hw *homeWorld) sameSession(project string, cs CoordinatorState) {
	hw.t.Helper()
	now := hw.mustHome(project)
	if now.RunID != cs.RunID || now.Session.ID != cs.Session.ID || !hw.live(now.Session) {
		hw.t.Fatalf("%s's coordinator changed: %+v, was %+v", project, now, cs)
	}
}

// C27: with a host that resumes, A's next session asks to resume, in A's
// checkout, with a new token. Its prompt holds no left-open list.
func TestC27ANextSessionResumesWhenTheHostCan(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "beta"}, alphaAndBeta)
	hw.withLeftOpen()
	hw.tick()
	for _, l := range hw.host.Launches() {
		if l.Spec.Resume {
			t.Fatalf("a first start asked to resume: %+v", l.Spec)
		}
	}
	a := hw.mustHome("alpha")

	hw.host.Exit(a.Session.ID, 0)
	hw.tick()
	a2 := hw.mustHome("alpha")
	spec, prompt := hw.lastLaunch()
	if !spec.Resume || spec.Checkout != a.Checkout || !hw.live(a2.Session) || a2.Session.ID == a.Session.ID {
		t.Fatalf("alpha's next start = %+v, session %+v", spec, a2)
	}
	if strings.Contains(prompt, "question/4") {
		t.Fatalf("the resumed session's prompt lists the open work:\n%s", prompt)
	}
	if hw.liveToken(a.RunID) || !hw.liveToken(a2.RunID) {
		t.Fatalf("alpha tokens: old live %v, new live %v", hw.liveToken(a.RunID), hw.liveToken(a2.RunID))
	}
}

// C27: with a host that answers ErrResumeUnsupported, A's next session
// starts fresh, and its prompt lists the work A's last session left open.
func TestC27ANextSessionListsTheOpenWorkWhenTheHostCannotResume(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "beta"}, alphaAndBeta)
	hw.withLeftOpen()
	hw.tick()
	a := hw.mustHome("alpha")
	hw.host.SetResumeUnsupported(true)

	hw.host.Exit(a.Session.ID, 0)
	hw.tick()
	a2 := hw.mustHome("alpha")
	spec, prompt := hw.lastLaunch()
	if spec.Resume || spec.Checkout != a.Checkout || !hw.live(a2.Session) || a2.Session.ID == a.Session.ID {
		t.Fatalf("alpha's next start = %+v, session %+v", spec, a2)
	}
	if !strings.Contains(prompt, "question/4 of alpha") {
		t.Fatalf("the prompt does not list alpha's open work:\n%s", prompt)
	}
	if hw.liveToken(a.RunID) || !hw.liveToken(a2.RunID) {
		t.Fatalf("alpha tokens: old live %v, new live %v", hw.liveToken(a.RunID), hw.liveToken(a2.RunID))
	}
	if n := hw.alerts(AlertCoordinator); n != 0 {
		t.Fatalf("%d %s alerts, want none", n, AlertCoordinator)
	}
}

// missingCheckout starts the coordinators of alpha and beta. Then alpha's
// session ends, the operator deletes its checkout, and two ticks run. It
// returns alpha's and beta's coordinators before the deletion.
func missingCheckout(t *testing.T) (*homeWorld, CoordinatorState, CoordinatorState) {
	t.Helper()
	hw := newHomeWorld(t, []string{"alpha", "beta"}, alphaAndBeta)
	hw.withLeftOpen()
	hw.tick()
	a, b := hw.mustHome("alpha"), hw.mustHome("beta")
	hw.host.Exit(a.Session.ID, 0)
	if err := os.RemoveAll(a.Checkout); err != nil {
		t.Fatal(err)
	}
	hw.tick()
	hw.tick()
	return hw, a, b
}

// C32: the operator deletes the checkout the server made for alpha. The
// next ticks start no session for alpha, and raise one open alert that
// names alpha and the restart verb.
func TestC32AMissingCheckoutStartsNothing(t *testing.T) {
	hw, _, b := missingCheckout(t)

	if n := len(hw.host.Launches()); n != 2 {
		t.Fatalf("%d sessions started, want alpha's and beta's first ones only", n)
	}
	if names := hw.fixtures["alpha"].co.names(); len(names) != 1 {
		t.Fatalf("alpha checkouts asked = %q, want the first one only", names)
	}
	if n := hw.count(`SELECT count(*) FROM alerts WHERE kind = ?`, AlertCoordinatorCheckoutMissing); n != 1 {
		t.Fatalf("%d %s alerts, want 1", n, AlertCoordinatorCheckoutMissing)
	}
	got := hw.str(`SELECT project || ' ' || dedupe_key || ' ' || state FROM alerts WHERE kind = ?`, AlertCoordinatorCheckoutMissing)
	if got != "alpha coordinator_checkout_missing:alpha open" {
		t.Fatalf("alert = %q", got)
	}
	if body := hw.str(`SELECT body FROM alerts WHERE kind = ?`, AlertCoordinatorCheckoutMissing); !strings.Contains(body, "loop coordinator restart --project alpha") {
		t.Fatalf("the alert does not name the restart: %q", body)
	}
	hw.sameSession("beta", b)
}

// C34: after C32, the restart makes alpha's checkout again and starts a
// fresh session in it, with the left-open list. Beta's session stays.
func TestC34RestartMakesAMissingCheckoutAgain(t *testing.T) {
	hw, _, b := missingCheckout(t)

	cs, err := hw.s.RestartCoordinator(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !exists(cs.Checkout) || !hw.live(cs.Session) {
		t.Fatalf("alpha's coordinator after the restart = %+v", cs)
	}
	if names := hw.fixtures["alpha"].co.names(); !slices.Equal(names, []string{"loop-coordinator", "loop-coordinator"}) {
		t.Fatalf("alpha checkouts asked = %q, want a new loop-coordinator checkout", names)
	}
	spec, prompt := hw.lastLaunch()
	if spec.Resume || spec.Checkout != cs.Checkout || !strings.Contains(prompt, "question/4 of alpha") {
		t.Fatalf("alpha's start = %+v, prompt:\n%s", spec, prompt)
	}
	if got := hw.mustHome("alpha"); got.RunID != cs.RunID || got.Fresh {
		t.Fatalf("alpha's stored coordinator = %+v, restart answered %+v", got, cs)
	}
	hw.sameSession("beta", b)

	n := len(hw.host.Launches())
	hw.tick()
	if len(hw.host.Launches()) != n {
		t.Fatal("the tick after the restart started another session")
	}
}

// C34: with alpha's session running, the restart closes it and revokes
// its token. The new session starts fresh in the same checkout.
func TestC34RestartReplacesARunningSession(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "beta"}, alphaAndBeta)
	hw.withLeftOpen()
	hw.tick()
	a, b := hw.mustHome("alpha"), hw.mustHome("beta")

	cs, err := hw.s.RestartCoordinator(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if hw.live(a.Session) || hw.liveToken(a.RunID) {
		t.Fatalf("the old session: live %v, token live %v", hw.live(a.Session), hw.liveToken(a.RunID))
	}
	if !hw.live(cs.Session) || !hw.liveToken(cs.RunID) || cs.Checkout != a.Checkout {
		t.Fatalf("alpha's coordinator after the restart = %+v, was %+v", cs, a)
	}
	spec, prompt := hw.lastLaunch()
	if spec.Resume || !strings.Contains(prompt, "question/4 of alpha") {
		t.Fatalf("alpha's start = %+v, prompt:\n%s", spec, prompt)
	}
	hw.sameSession("beta", b)
}

// C34: with alpha's old session live, the restart closes it and revokes
// its token. The store holds no coordinator row, and alpha's new session
// starts fresh in the old checkout.
func TestC34RestartReplacesTheOldSession(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "beta"}, alphaAndBeta)
	hw.withLeftOpen()
	old := hw.oldSession("alpha")
	hw.tick()
	b := hw.mustHome("beta")

	cs, err := hw.s.RestartCoordinator(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if hw.live(old.Session) || hw.liveToken(old.RunID) {
		t.Fatalf("the old session: live %v, token live %v", hw.live(old.Session), hw.liveToken(old.RunID))
	}
	if hw.oldRows() != 0 {
		t.Fatal("the store holds the coordinator row")
	}
	if !hw.live(cs.Session) || cs.Checkout != old.Checkout || hw.tokenProject(cs.RunID) != "alpha" {
		t.Fatalf("alpha's coordinator after the restart = %+v, old checkout %s", cs, old.Checkout)
	}
	spec, prompt := hw.lastLaunch()
	if spec.Resume || !strings.Contains(prompt, "question/4 of alpha") {
		t.Fatalf("alpha's start = %+v, prompt:\n%s", spec, prompt)
	}
	hw.sameSession("beta", b)
}

// C36: the restart refuses a project that is not registered, and one with
// no config at its default branch. No session starts.
func TestC36RestartNeedsARegisteredProjectWithAConfig(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "gamma"}, map[string]bool{"alpha": true})
	hw.tick()
	n := len(hw.host.Launches())

	if _, err := hw.s.RestartCoordinator(context.Background(), "nope"); !errors.Is(err, config.ErrProjectNotFound) {
		t.Fatalf("restart of an unknown project = %v", err)
	}
	if _, err := hw.s.RestartCoordinator(context.Background(), "gamma"); !errors.Is(err, ErrNoProjectConfig) {
		t.Fatalf("restart of a project with no config = %v", err)
	}
	if len(hw.host.Launches()) != n || hw.homeRows() != 1 {
		t.Fatalf("a refused restart started a session: %d launches, %d rows", len(hw.host.Launches()), hw.homeRows())
	}
}
