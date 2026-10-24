package github

import (
	"strconv"
	"strings"
	"testing"

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
