package github

import (
	"strconv"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// C-bug-1: `pr open` on the item's open PR with a new title and body
// writes them to the PR, and the envelope says it updated.
func TestPROpenOnAnOpenPRUpdatesTitleAndBody(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InProgress, shaA)
	pr := w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA, Title: "old title", Body: "old body"})

	res := e.runCall("pr open", id, machine.StageImplementer,
		map[string]string{"title": "new title", "body": "new body names what the check stops"}, nil)
	mustOK(t, res)
	out, _ := res.Data.(map[string]any)
	if out["created"] != false || out["updated"] != true {
		t.Fatalf("envelope created=%v updated=%v, want created=false updated=true", out["created"], out["updated"])
	}
	got := w.PR(pr.Number)
	if got.Title != "new title" || got.Body != "new body names what the check stops" {
		t.Fatalf("PR #%d holds title %q body %q after pr open", pr.Number, got.Title, got.Body)
	}
	if !patchedPull(e, 1) {
		t.Fatalf("no PATCH of /pulls/1: %v", e.api.Calls())
	}
}

// C-bug-1: `pr open` with the title and body the open PR already holds
// writes nothing, and the envelope says it did not update.
func TestPROpenWithTheSameTitleAndBodyWritesNothing(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InProgress, shaA)
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA, Title: "same title", Body: "same body"})

	res := e.runCall("pr open", id, machine.StageImplementer,
		map[string]string{"title": "same title", "body": "same body"}, nil)
	mustOK(t, res)
	out, _ := res.Data.(map[string]any)
	if out["created"] != false || out["updated"] != false {
		t.Fatalf("envelope created=%v updated=%v, want created=false updated=false", out["created"], out["updated"])
	}
	if patchedPull(e, 1) {
		t.Fatalf("PATCH of /pulls/1 with an unchanged title and body: %v", e.api.Calls())
	}
}

// C-bug-1: `pr open` with no --body writes the new title and leaves the
// body the open PR holds.
func TestPROpenWithNoBodyLeavesTheBody(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InProgress, shaA)
	pr := w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA, Title: "old title", Body: "the body a reviewer read"})

	res := e.runCall("pr open", id, machine.StageImplementer, map[string]string{"title": "new title"}, nil)
	mustOK(t, res)
	out, _ := res.Data.(map[string]any)
	if out["updated"] != true {
		t.Fatalf("envelope updated=%v, want true", out["updated"])
	}
	got := w.PR(pr.Number)
	if got.Title != "new title" || got.Body != "the body a reviewer read" {
		t.Fatalf("PR #%d holds title %q body %q after pr open with no body", pr.Number, got.Title, got.Body)
	}
}

// patchedPull reports whether the test sent a PATCH of PR n.
func patchedPull(e *env, n int) bool {
	for _, c := range e.api.Calls() {
		if strings.HasPrefix(c, "PATCH ") && strings.HasSuffix(c, "/pulls/"+strconv.Itoa(n)) {
			return true
		}
	}
	return false
}

// parity 3.21: `pr open` runs the body gate before GitHub. A body over
// the 700-word budget is refused with BODY_REFUSED, no pull request is
// opened, and the item stays with its run.
func TestPROpenRefusesABodyOverTheBudget(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	id := e.ticket(machine.InProgress, shaA)

	body := strings.TrimSpace(strings.Repeat("word ", 701))
	res := e.runCall("pr open", id, machine.StageImplementer, map[string]string{"title": "t", "body": body}, nil)
	if res.Refusal == nil || res.Refusal.Code != "BODY_REFUSED" {
		t.Fatalf("pr open with 701 words: refusal %+v, want BODY_REFUSED", res.Refusal)
	}
	if !strings.Contains(res.Refusal.Hint, "budget 700 words, actual 701") {
		t.Fatalf("hint %q does not name the budget and the count", res.Refusal.Hint)
	}
	for _, c := range e.api.Calls() {
		if strings.HasPrefix(c, "POST ") || strings.HasPrefix(c, "PATCH ") {
			t.Fatalf("pr open wrote to GitHub after the gate refused: %s", c)
		}
	}
	if st := e.status(id).Status; st != machine.InProgress {
		t.Fatalf("item %s is %s after the refusal, want %s", id, st, machine.InProgress)
	}
}

// parity 3.21: the gate refuses the markup and the prose rules on an open
// pull request too, and the PR keeps the body it holds.
func TestPROpenRefusesABodyMarkupAndProseFaultOnAnOpenPR(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InProgress, shaA)
	pr := w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA, Title: "t", Body: "the body a reviewer read"})

	body := "**One** and **two** in a block.\n\nA dash — here.\n"
	res := e.runCall("pr open", id, machine.StageImplementer, map[string]string{"title": "t", "body": body}, nil)
	if res.Refusal == nil || res.Refusal.Code != "BODY_REFUSED" {
		t.Fatalf("pr open with a markup and a prose fault: refusal %+v, want BODY_REFUSED", res.Refusal)
	}
	for _, want := range []string{"bold-texture", "em-dash"} {
		if !strings.Contains(res.Refusal.Hint, want) {
			t.Errorf("hint does not name %s:\n%s", want, res.Refusal.Hint)
		}
	}
	if got := w.PR(pr.Number).Body; got != "the body a reviewer read" {
		t.Fatalf("PR #%d holds body %q after the refusal", pr.Number, got)
	}
}

// A new ticket passes through in_simplify: the implementer's pr open
// hands it to the simplify stage, and the simplify run's pr open, with
// no title and no body, hands it to review at the head its round started
// from. The pull request keeps its title and body.
func TestSimplifyRunHandsTheTicketToReview(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InProgress, shaA)
	pr := w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA, Title: "t", Body: "the implementer's red and green runs"})
	mustOK(t, e.runCall("pr open", id, machine.StageImplementer, map[string]string{"title": "t"}, nil))
	if it := e.status(id); it.Status != "in_simplify" || it.Fact("simplify_from") != shaA {
		t.Fatalf("after the implementer's pr open: %s %v", it.Status, it.Facts)
	}
	if _, res := e.m.Claim(t.Context(), machine.ClaimRequest{Project: testProject, Item: id, Stage: config.StageSimplify,
		RunID: "run-" + config.StageSimplify}); res.Refusal != nil {
		t.Fatalf("simplify claim: %+v", *res.Refusal)
	}

	mustOK(t, e.runCall("pr open", id, config.StageSimplify, nil, nil))
	if it := e.status(id); it.Status != machine.InReview || it.Fact(machine.FactHead) != shaA || it.Owner != "" {
		t.Fatalf("after the simplify run's pr open: %s owner %q %v", it.Status, it.Owner, it.Facts)
	}
	if got := w.PR(pr.Number); got.Title != "t" || got.Body != "the implementer's red and green runs" {
		t.Fatalf("PR #%d holds title %q body %q", pr.Number, got.Title, got.Body)
	}
}
