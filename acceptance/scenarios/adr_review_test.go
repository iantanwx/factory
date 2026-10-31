//go:build acceptance

package scenarios

import (
	"encoding/json"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

const adrReviewed = "docs/adr/0001-widget-store.md"

// adrReviewText is an ADR whose Ticket line names epic.
func adrReviewText(epic, decision string) string {
	return "# ADR 0001: Widget store\n\n**Ticket:** " + epic + "\n\n" + decision + "\n"
}

// openADRPull creates an architecture epic, and an architect room opens
// the pull request that adds its ADR, and closes. GitHub lists the ADR as
// added.
func openADRPull(w *acceptance.World, title string) (string, *gt.PR) {
	w.T.Helper()
	r := w.Must(w.Coordinator("epic", "create", "--architecture", "--project", acceptance.Project,
		"--title", title, "--body", "The operator asked for an ADR on the widget store."))
	epic := r.Str("epic")
	room, co := openRoom(w, "architect", epic, "the operator wants to decide the widget store")
	text := adrReviewText(epic, "Use Postgres.")
	w.Commit(co, "docs(adr): 0001 widget store", map[string]string{adrReviewed: text})
	w.Must(w.In(co, "branch", "push"))
	w.Must(w.In(co, "pr", "open", "--title", "docs(adr): 0001 widget store", "--body", "ADR 0001 records the operator's answer."))
	pr := w.PRFor(epic)
	if pr == nil {
		w.T.Fatal("the architect room opened no pull request on the epic's branch")
	}
	dec, _ := json.Marshal(map[string]any{"decision": "ADR 0001 in the open pull request.", "statement": "Postgres."})
	w.Must(w.In(co, "room", "apply", room, "--file", w.WriteFile("decision-"+epic+".json", string(dec))))
	w.Must(w.Coordinator("room", "close", room))
	w.API.World.Update(pr, func(p *gt.PR) { p.Added = []string{adrReviewed} })
	w.API.World.SetFile(pr.HeadSHA, adrReviewed, text)
	return epic, pr
}

// adrVerdict dispatches a doc-reviewer on epic, which submits verdict at
// the head that pr show names.
func adrVerdict(w *acceptance.World, epic, verdict string) {
	w.T.Helper()
	run := w.Dispatch(machine.StageDocReviewer)
	if run.Item != epic {
		w.T.Fatalf("the doc-reviewer took %s, want %s", run.Item, epic)
	}
	head := w.Must(w.In(run.Checkout, "pr", "show")).Str("head_sha")
	w.Must(w.In(run.Checkout, "review", "submit", epic, "--verdict", verdict, "--head", head, "--body", "The ADR binds two epics."))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	wantStatus(w, epic, machine.Architecture)
}

// factorio-ku99 C26, C27 and C28: the doc-reviewer reviews each ADR pull
// request of an epic in architecture once at each head. A verdict returns
// the epic to architecture, and a new head opens the review again. A
// close with no merge ends the review. A merge during the review moves
// the epic to spec_needed and fences the reviewer.
func TestADRReviewEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})

	e1, pr := openADRPull(w, "Move the widget store to Postgres")
	w.React()
	wantStatus(w, e1, machine.ADRReview)
	failTwice(w, machine.StageDocReviewer, e1, machine.ADRReview)
	adrVerdict(w, e1, "reject")
	w.React()
	wantStatus(w, e1, machine.Architecture)

	text := adrReviewText(e1, "Use Postgres, and keep SQLite for tests.")
	head := w.PushBranch(pr.Head, "docs(adr): answer the review", map[string]string{adrReviewed: text})
	w.API.World.SetFile(head, adrReviewed, text)
	w.React()
	wantStatus(w, e1, machine.ADRReview)
	adrVerdict(w, e1, "pass")

	head = w.PushBranch(pr.Head, "docs(adr): name the cost", map[string]string{adrReviewed: text + "\nThe move costs a migration.\n"})
	w.API.World.SetFile(head, adrReviewed, text+"\nThe move costs a migration.\n")
	w.React()
	wantStatus(w, e1, machine.ADRReview)
	w.ClosePR(pr)
	w.React()
	wantStatus(w, e1, machine.Architecture)

	e2, pr2 := openADRPull(w, "Move the gadget store to Postgres")
	w.React()
	wantStatus(w, e2, machine.ADRReview)
	run := w.Dispatch(machine.StageDocReviewer)
	w.MergeByHand(pr2)
	w.React()
	wantStatus(w, e2, machine.SpecNeeded)
	if r := w.In(run.Checkout, "review", "submit", e2, "--verdict", "pass", "--head", pr2.HeadSHA); r.Code != 3 {
		t.Fatalf("the outran reviewer's verdict = %s", r)
	}
}
