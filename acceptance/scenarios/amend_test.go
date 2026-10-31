//go:build acceptance

package scenarios

import (
	"testing"

	"github.com/iantanwx/loop/acceptance"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// comment is a person's comment on the epic's document PR, read by one
// reactor tick. The epic then waits for the amender in want.
func comment(w *acceptance.World, epic, body, want string) {
	w.T.Helper()
	w.API.World.Say(docPR(w, epic).Number, gt.Said{Login: "operator", Association: "OWNER", Body: body})
	w.React()
	wantStatus(w, epic, want)
}

// amend starts the amender on epic, and returns its run.
func amend(w *acceptance.World, epic string) acceptance.Run {
	w.T.Helper()
	run := w.Dispatch(machine.StageAmender)
	if run.Item != epic {
		w.T.Fatalf("the amender took %s, want %s", run.Item, epic)
	}
	return run
}

// answer replies on the document PR, pushes a revision when one is given,
// opens the PR again and finishes the run.
func answer(w *acceptance.World, run acceptance.Run, title, path, revision string) {
	w.T.Helper()
	if revision != "" {
		syncItem(w, run.Checkout)
		w.Commit(run.Checkout, "answer the comment", map[string]string{path: revision})
		w.Must(w.In(run.Checkout, "branch", "push"))
	}
	w.Must(w.In(run.Checkout, "pr", "reply", "--body", "Answered in the document, or here."))
	w.Must(w.In(run.Checkout, "pr", "open", "--title", title))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
}

// 1.12: a person's comment on a PRD or a spec, in review or in sign-off,
// wakes the amender. A reply alone returns a signed-off document to
// sign-off, a revision returns it to review, and the operator's merge
// while the amender works is the sign-off.
func TestPersonCommentRowsEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	room, co := openRoom(w, "brainstorm", "", "the operator wants sprockets")
	epic := submitPRD(w, co, "Sprockets")
	w.Must(w.Coordinator("room", "close", room))

	// The PRD in review: the amender replies, and the PRD returns to review.
	comment(w, epic, "Does US-02 cover a rename?", machine.PRDAmending)
	answer(w, amend(w, epic), "PRD: Sprockets", prdPath, "")
	wantStatus(w, epic, machine.PRDReview)
	w.React()
	wantStatus(w, epic, machine.PRDReview)

	// The PRD in sign-off: a reply alone returns it to sign-off.
	docReview(w, epic, "pass")
	comment(w, epic, "Why no US-03?", machine.PRDAmending)
	answer(w, amend(w, epic), "PRD: Sprockets", prdPath, "")
	wantStatus(w, epic, machine.PRDSignoff)

	// The operator merges while the amender answers: the merge is the
	// sign-off.
	comment(w, epic, "One more question.", machine.PRDAmending)
	run := amend(w, epic)
	w.MergeByHand(docPR(w, epic))
	w.React()
	wantStatus(w, epic, machine.SpecNeeded)
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))

	// The spec in review: two failed amender runs keep the epic, and a
	// revision returns the spec to review.
	draftSpec(w, epic, "draft one")
	comment(w, epic, "ADR-0011 is superseded by ADR-0013.", machine.SpecAmending)
	failTwice(w, machine.StageAmender, epic, machine.SpecAmending)
	answer(w, amend(w, epic), "Spec: widgets", "docs/specs/widgets.md", "# Widgets spec\n\ncites ADR-0013\n")
	wantStatus(w, epic, machine.SpecReview)

	// The spec in sign-off: a reply alone returns it to sign-off.
	docReview(w, epic, "pass")
	comment(w, epic, "Is the cap per host?", machine.SpecAmending)
	answer(w, amend(w, epic), "Spec: widgets", "docs/specs/widgets.md", "")
	wantStatus(w, epic, machine.SpecSignoff)

	comment(w, epic, "Ship it after one fix.", machine.SpecAmending)
	run = amend(w, epic)
	w.MergeByHand(docPR(w, epic))
	w.React()
	wantStatus(w, epic, machine.SpecApproved)
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
}
