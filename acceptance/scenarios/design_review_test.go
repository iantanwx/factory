//go:build acceptance

package scenarios

import (
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/design"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// uiEpicInDesign signs off uiPRD, and the epic lands on design.
func uiEpicInDesign(w *acceptance.World, title string) string {
	w.T.Helper()
	epic := uiPRDInSignoff(w, title, "")
	atMerge(w, gt.MergeSHA(docPR(w, epic).Number))
	w.Must(w.Coordinator("inbox", "answer", "signoff/"+acceptance.Project+"/"+epic, "--body", `The operator: "LGTM, ship the PRD."`))
	wantStatus(w, epic, machine.Design)
	return epic
}

// openDesignPull commits the design record of an epic in design in a room
// on the epic, pushes it, and opens its pull request with `loop pr open`,
// as the design room does. With no record the room only opens the pull
// request again. No design room kind exists yet, so a review
// room on the epic stands in for it. Each open of one epic needs its own
// reason, or the room open replays the first room. It returns the pull
// request.
func openDesignPull(w *acceptance.World, epic, reason, record string) *gt.PR {
	w.T.Helper()
	room, co := openRoom(w, "review", epic, reason)
	if record != "" {
		w.Commit(co, "docs(design): record of "+epic, map[string]string{design.Dir(epic) + design.RecordFile: record})
		w.Must(w.In(co, "branch", "push"))
	}
	w.Must(w.In(co, "pr", "open", "--title", "docs(design): record of "+epic, "--body", "The design record of "+epic+"."))
	w.Must(w.Coordinator("room", "close", room))
	pr := docPR(w, epic)
	if pr.Head != github.DesignBranchFor(epic) {
		w.T.Fatalf("the design pull request #%d has head %s, want %s", pr.Number, pr.Head, github.DesignBranchFor(epic))
	}
	wantStatus(w, epic, machine.DesignReview)
	return pr
}

// designVerdict dispatches a doc-reviewer on epic, which submits verdict
// at the head that pr show names. The epic then holds want.
func designVerdict(w *acceptance.World, epic, verdict, want string) {
	w.T.Helper()
	run := w.Dispatch(machine.StageDocReviewer)
	if run.Item != epic {
		w.T.Fatalf("the doc-reviewer took %s, want %s", run.Item, epic)
	}
	head := w.Must(w.In(run.Checkout, "pr", "show")).Str("head_sha")
	w.Must(w.In(run.Checkout, "review", "submit", epic, "--verdict", verdict, "--head", head, "--body", "Checked each visible criterion.",
		"--readers", allReaders))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	wantStatus(w, epic, want)
}

// factorio-a4yq C22 and C24: the design pull request of an epic in design
// goes to the doc-reviewer at each open. A reject returns the epic to
// design, and a pass to design_signoff. Only the operator's approval
// merges the record, and the epic moves on to spec_needed with the
// record's path. A merge during the review moves the epic on through
// design_signoff, and a closed design pull request escalates the epic
// with its resume at design.
func TestDesignRecordReviewAndSignoffEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	const record = "# Design record\n\nUS-01: a visitor sees every widget.\n"

	e1 := uiEpicInDesign(w, "Widgets")
	pr := openDesignPull(w, e1, "the operator wants the widgets designed", record)
	failTwice(w, machine.StageDocReviewer, e1, machine.DesignReview)
	designVerdict(w, e1, "reject", machine.Design)
	if got := w.API.World.PR(pr.Number).State; got != "open" {
		t.Fatalf("the rejected design pull request is %s, want open", got)
	}
	w.PushBranch(pr.Head, "docs(design): answer the review", map[string]string{design.Dir(e1) + design.RecordFile: record + "\nUS-01 also shows an empty list.\n"})
	if again := openDesignPull(w, e1, "the operator answers the design review", ""); again.Number != pr.Number {
		t.Fatalf("the second open made #%d, want #%d again", again.Number, pr.Number)
	}
	designVerdict(w, e1, "pass", machine.DesignSignoff)
	entry := signoffEntry(w, e1)
	path := design.Dir(e1) + design.RecordFile
	w.API.World.SetFile(gt.MergeSHA(pr.Number), path, record)
	w.Must(w.Coordinator("inbox", "answer", asString(entry["entry"]), "--body", `The operator: "LGTM, ship the design."`))
	if calls := mergesOf(w, pr.Number); len(calls) != 1 || calls[0].SHA != asString(entry["head"]) {
		t.Fatalf("the merges of design #%d = %+v, want one at %v", pr.Number, calls, entry["head"])
	}
	wantStatus(w, e1, machine.SpecNeeded)
	if f := w.Show(e1).Facts; f[machine.FactDesignPath] != path || f[machine.FactDesignMerged] != gt.MergeSHA(pr.Number) {
		t.Fatalf("%s holds design_path %q and design_merged %q, want %s at %s", e1, f[machine.FactDesignPath],
			f[machine.FactDesignMerged], path, gt.MergeSHA(pr.Number))
	}

	// outran: the operator merges the record while a review runs. The
	// merge moves the epic to design_signoff, and the reactor's read of
	// the same merge moves it on to spec_needed, as for a PRD.
	e2 := uiEpicInDesign(w, "Gadgets")
	pr2 := openDesignPull(w, e2, "the operator wants the gadgets designed", record)
	run := w.Dispatch(machine.StageDocReviewer)
	sha := w.MergeByHand(pr2)
	w.API.World.SetFile(sha, design.Dir(e2)+design.RecordFile, record)
	w.React()
	wantStatus(w, e2, machine.SpecNeeded)
	if f := w.Show(e2).Facts; f[machine.FactDesignMerged] != sha || f[machine.FactDesignPath] != design.Dir(e2)+design.RecordFile {
		t.Fatalf("%s holds design_merged %q and design_path %q, want the hand merge %s", e2, f[machine.FactDesignMerged],
			f[machine.FactDesignPath], sha)
	}
	if r := w.In(run.Checkout, "review", "submit", e2, "--verdict", "pass", "--head", pr2.HeadSHA, "--readers", allReaders); r.Code != 3 {
		t.Fatalf("the outran reviewer's verdict = %s", r)
	}

	e3 := uiEpicInDesign(w, "Gizmos")
	w.ClosePR(openDesignPull(w, e3, "the operator wants the gizmos designed", record))
	w.React()
	wantStatus(w, e3, machine.Escalated)
	if got := w.Show(e3).Facts[machine.FactResume]; got != machine.Design {
		t.Fatalf("%s resumes at %q, want %s", e3, got, machine.Design)
	}
}
