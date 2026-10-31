//go:build acceptance

package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// comment is a person's comment on the epic's document PR, read by one
// reactor tick. The epic then waits for the reviser in want.
func comment(w *acceptance.World, epic, body, want string) {
	w.T.Helper()
	w.API.World.Say(docPR(w, epic).Number, gt.Said{Login: "operator", Association: "OWNER", Body: body})
	w.React()
	wantStatus(w, epic, want)
}

// amend starts the reviser on epic, and returns its run.
func amend(w *acceptance.World, epic string) acceptance.Run {
	w.T.Helper()
	run := w.Dispatch(machine.StageReviser)
	if run.Item != epic {
		w.T.Fatalf("the reviser took %s, want %s", run.Item, epic)
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
// wakes the reviser. A reply alone returns a signed-off document to
// sign-off, a revision returns it to review, and the operator's merge
// while the reviser works is the sign-off.
func TestPersonCommentRowsEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	room, co := openRoom(w, "intake", "", "the operator wants sprockets")
	epic := submitPRD(w, co, "Sprockets")
	w.Must(w.Coordinator("room", "close", room))

	// The PRD in review: the reviser replies, and the PRD returns to review.
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

	// The operator merges while the reviser answers: the merge is the
	// sign-off.
	comment(w, epic, "One more question.", machine.PRDAmending)
	run := amend(w, epic)
	w.MergeByHand(docPR(w, epic))
	w.React()
	wantStatus(w, epic, machine.SpecNeeded)
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))

	// The spec in review: two failed reviser runs keep the epic, and a
	// revision returns the spec to review.
	draftSpec(w, epic, "draft one")
	comment(w, epic, "ADR-0011 is superseded by ADR-0013.", machine.SpecAmending)
	failTwice(w, machine.StageReviser, epic, machine.SpecAmending)
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

// C1 and C10, work entry 3 of the vocabulary spec: a project config that
// sets stages.amender.cap to 2 loads with the reviser's cap at 2, and the
// project's old_stage_names alert names amender and reviser. An epic in
// prd_amending starts a reviser run whose brief names the reviser's
// runbook.
func TestC1C10AnAmenderKeyLoadsAsTheReviser(t *testing.T) {
	cfg := strings.Replace(acceptance.DefaultConfig, "  reviser: {}\n", "  amender:\n    cap: 2\n", 1)
	w := newWorld(t, acceptance.Options{Config: cfg})
	co := openIntake(w, "the operator wants widgets")
	epic := submitPRD(w, co, "Widgets")
	docReview(w, epic, "reject")
	wantStatus(w, epic, machine.PRDAmending)

	run := w.Dispatch("reviser")
	if run.Item != epic {
		t.Fatalf("the reviser took %s, want %s", run.Item, epic)
	}
	raw, err := os.ReadFile(filepath.Join(run.Checkout, ".loop", "brief.json"))
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Runbook string `json:"runbook"`
	}
	if err := json.Unmarshal(raw, &b); err != nil || b.Runbook != ".claude/skills/loop-reviser/SKILL.md" {
		t.Fatalf("the reviser's brief names runbook %q, %v", b.Runbook, err)
	}

	caps := map[string]any{}
	for _, r := range w.Must(w.Coordinator("settings", "show", acceptance.Project)).Data()["settings"].([]any) {
		row := r.(map[string]any)
		caps[row["key"].(string)] = row["value"]
	}
	if caps["stages.reviser.cap"] != "2" {
		t.Fatalf("stages.reviser.cap = %v, want 2", caps["stages.reviser.cap"])
	}
	if _, ok := caps["stages.amender.cap"]; ok {
		t.Fatal("settings show names stages.amender.cap")
	}
	if n := w.Count(`SELECT count(*) FROM alerts WHERE kind = 'old_stage_names' AND state = 'open'
		AND body LIKE '%amender%' AND body LIKE '%reviser%'`); n != 1 {
		t.Fatalf("%d open old_stage_names alerts name amender and reviser, want 1", n)
	}
}
