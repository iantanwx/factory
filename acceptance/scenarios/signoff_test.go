//go:build acceptance

package scenarios

import (
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// docPass starts a doc-reviewer run on epic and passes the document at the
// head it reads with `loop pr show`, as the doc-reviewer runbook says. It
// returns that head.
func docPass(w *acceptance.World, epic string) string {
	w.T.Helper()
	run := w.Dispatch(machine.StageDocReviewer)
	if run.Item != epic {
		w.T.Fatalf("the doc-reviewer took %s, want %s", run.Item, epic)
	}
	head := w.Must(w.In(run.Checkout, "pr", "show")).Str("head_sha")
	if head == "" {
		w.T.Fatalf("pr show named no head for %s", epic)
	}
	w.Must(w.In(run.Checkout, "review", "submit", epic, "--verdict", "pass", "--head", head, "--body", "Read the document at "+head+"."))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	return head
}

// signoffEntry is the epic's sign-off entry, or a failure.
func signoffEntry(w *acceptance.World, epic string) map[string]any {
	w.T.Helper()
	e := w.Entry("signoff/" + acceptance.Project + "/" + epic)
	if e == nil {
		w.T.Fatalf("no sign-off entry for %s", epic)
	}
	return e
}

// mergesOf lists the merge calls GitHub took for PR n.
func mergesOf(w *acceptance.World, n int) []gt.MergeCall {
	var out []gt.MergeCall
	for _, m := range w.API.World.Merges() {
		if m.PR == n {
			out = append(out, m)
		}
	}
	return out
}

// factorio-8qm: the operator's approval is the sign-off. The coordinator
// records it with the operator's own words, and the server merges the
// document PR at the head the entry named. No agent merges. The epic
// leaves prd_signoff, then spec_signoff, at once.
func TestSignoffAnswerMergesTheDocumentAtItsPinnedHead(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	epic := submitPRD(w, openIntake(w, "the operator wants widgets"), "Widgets")

	head := docPass(w, epic)
	wantStatus(w, epic, machine.PRDSignoff)
	entry := signoffEntry(w, epic)
	if entry["head"] != head || entry["awaits"] != "person" {
		t.Fatalf("the PRD's sign-off entry = %v, want head %s", entry, head)
	}
	if next := asString(entry["next"]); !contains(next, "loop inbox answer signoff/") {
		t.Fatalf("the entry's next step = %q, want the inbox answer", next)
	}
	prd := docPR(w, epic)
	w.Must(w.Coordinator("inbox", "answer", asString(entry["entry"]), "--body", `The operator: "LGTM, ship the PRD."`))
	if calls := mergesOf(w, prd.Number); len(calls) != 1 || calls[0].SHA != head || calls[0].Status != 200 {
		t.Fatalf("the merges of PRD #%d = %+v, want one at %s", prd.Number, calls, head)
	}
	wantStatus(w, epic, machine.SpecNeeded)
	if got := w.Show(epic).Facts[machine.FactPRDMerged]; got != gt.MergeSHA(prd.Number) {
		t.Fatalf("prd_merged = %q, want the merge commit %s", got, gt.MergeSHA(prd.Number))
	}
	if n := w.Count(`SELECT count(*) FROM audit WHERE action = 'signoff_answered' AND detail LIKE '%LGTM, ship the PRD.%'`); n != 1 {
		t.Fatalf("%d audit rows hold the operator's words", n)
	}

	draftSpec(w, epic, "draft one")
	head = docPass(w, epic)
	wantStatus(w, epic, machine.SpecSignoff)
	spec := docPR(w, epic)
	w.Must(w.Coordinator("inbox", "answer", "signoff/"+acceptance.Project+"/"+epic, "--body", `The operator: "both PRs LGTM"`))
	if calls := mergesOf(w, spec.Number); len(calls) != 1 || calls[0].SHA != head {
		t.Fatalf("the merges of spec #%d = %+v, want one at %s", spec.Number, calls, head)
	}
	wantStatus(w, epic, machine.SpecApproved)
	if e := w.Entry("signoff/" + acceptance.Project + "/" + epic); e != nil {
		t.Fatalf("the sign-off entry stays after the merge: %v", e)
	}
	// The reactor reads the merge again, and the epic stays where it is.
	w.React()
	wantStatus(w, epic, machine.SpecApproved)
}

// A push after the entry was raised: the answer merges nothing, and the
// entry is raised again at the new head. The operator's next approval
// merges that head.
func TestSignoffAnswerRefusesAMovedHeadAndRaisesTheEntryAgain(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	epic := submitPRD(w, openIntake(w, "the operator wants widgets"), "Widgets")
	reviewed := docPass(w, epic)
	before := signoffEntry(w, epic)
	pr := docPR(w, epic)

	moved := w.PushBranch(pr.Head, "a late edit", map[string]string{prdPath: prdText + "\nA late edit.\n"})
	if moved == reviewed {
		t.Fatal("the push did not move the head")
	}
	r := w.Coordinator("inbox", "answer", asString(before["entry"]), "--body", `The operator: "LGTM"`)
	if r.RefusalCode() != cli.CodeStatusChanged || r.Code != 3 {
		t.Fatalf("the answer at a moved head = %s, want %s", r, cli.CodeStatusChanged)
	}
	if rule, _ := r.Env["rule"].(string); rule != "head_moved" {
		t.Fatalf("the refusal's rule = %q", rule)
	}
	if calls := mergesOf(w, pr.Number); len(calls) != 0 {
		t.Fatalf("a refused answer sent merges %+v", calls)
	}
	wantStatus(w, epic, machine.PRDSignoff)
	after := signoffEntry(w, epic)
	if after["head"] != moved {
		t.Fatalf("the entry names head %v, want the new head %s", after["head"], moved)
	}
	if at, was := after["at"].(float64), before["at"].(float64); at <= was {
		t.Fatalf("the entry was not raised again: at %v, before %v", at, was)
	}

	w.Must(w.Coordinator("inbox", "answer", asString(after["entry"]), "--body", `The operator, after reading the late edit: "LGTM"`))
	if calls := mergesOf(w, pr.Number); len(calls) != 1 || calls[0].SHA != moved {
		t.Fatalf("the merges of #%d = %+v, want one at %s", pr.Number, calls, moved)
	}
	wantStatus(w, epic, machine.SpecNeeded)
}
