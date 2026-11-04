package machine

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

// stopBody is a shaped ticket body with one contract, two Done means lines
// and one stop condition.
const stopBody = "## Source\n\nSpec entry 7.\n\n## Change\n\nRefuse it.\n\n## Contracts to prove\n\n- C1. Refusal: one.\n\n" +
	"## Files\n\n- verbs.go\n\n## Done means\n\n- The gates pass.\n- The proof is red at the base.\n\n" +
	"## Stop conditions\n\n- The change spends money.\n\n## Out of scope\n\n- Nothing.\n"

// verdict submits a code verdict on id as rev at shaA.
func (w *world) verdict(id string, rev rpc.Principal, verdict, body string) cli.Result {
	w.t.Helper()
	return w.call("review submit", rev, []string{id}, map[string]string{"verdict": verdict, "head": shaA, "body": body})
}

// refused submits a verdict that must refuse with SHAPE_REFUSED of rule,
// with a hint that names each of names, and leave the ticket in in_review
// with no new comment.
func (w *world) refused(id string, rev rpc.Principal, verdict, body, rule string, log *commentLog, names ...string) {
	w.t.Helper()
	posted := len(log.calls)
	r := wantCode(w.t, w.verdict(id, rev, verdict, body), cli.CodeShapeRefused)
	if r.Rule != rule || r.Item != id || r.CurrentStatus != InReview {
		w.t.Fatalf("refusal: %+v", *r)
	}
	for _, n := range names {
		if !regexp.MustCompile(`\b` + n + `\b`).MatchString(r.Hint) {
			w.t.Fatalf("the hint %q does not name %s", r.Hint, n)
		}
	}
	if it := w.show(id); it.Status != InReview || len(log.calls) != posted {
		w.t.Fatalf("a refused verdict wrote: %s %v", it.Status, log.calls)
	}
}

// backInReview takes a ticket in todo through an implementer's hand-off to
// in_review, and claims it for a new code reviewer.
func (w *world) backInReview(id, run string) rpc.Principal {
	w.t.Helper()
	w.claimAs(id, StageImplementer, "impl-"+run)
	w.must(id, "pr_open", map[string]string{"head": shaA})
	if w.show(id).Status == InSimplify {
		w.must(id, VerbSimplifyOff, nil)
	}
	return w.claimAs(id, StageCodeReviewer, run)
}

// standing reads the answers that review checklist names for stop1.
func (w *world) standing(id string, rev rpc.Principal) []string {
	w.t.Helper()
	res := w.call("review checklist", rev, []string{id}, nil)
	mustOK(w.t, res)
	raw, err := json.Marshal(res.Data)
	if err != nil {
		w.t.Fatal(err)
	}
	var owed []struct {
		ID      string   `json:"id"`
		Answers []string `json:"answers"`
	}
	if err := json.Unmarshal(raw, &owed); err != nil {
		w.t.Fatal(err)
	}
	for _, o := range owed {
		if o.ID == "stop1" {
			return o.Answers
		}
	}
	w.t.Fatalf("review checklist owes no stop1: %s", raw)
	return nil
}

// A code verdict on a shaped ticket carries a checklist line for each
// contract, each Done means line and each stop condition. A pass that omits
// D2 is refused and names D2, and so is a line with no evidence.
func TestCodeVerdictOwesEveryChecklistLine(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)
	id, rev := w.shapedInReview(stopBody, map[string]string{FactShape: "ticket"})

	w.refused(id, rev, "pass", "- C1 pass: red at the base.\n- D1 pass: green.\n- stop1 pass: no spend.", "checklist_incomplete", log, "D2")
	w.refused(id, rev, "reject", "- C1 fail: no test.\n- D1 pass: green.\n- D2 pass\n- stop1 pass: no spend.", "checklist_incomplete", log, "D2")

	mustOK(t, w.verdict(id, rev, "pass", "- C1 pass: red at the base.\n- D1 pass: green.\n- D2 pass: red.\n- stop1 pass: no spend."))
	if it := w.show(id); it.Status != ReadyToMerge || len(log.calls) != 1 {
		t.Fatalf("a full checklist: %s %v", it.Status, log.calls)
	}
}

// review checklist names each line a code verdict on the ticket owes, and
// a ticket with no shape fact owes none.
func TestReviewChecklistListsEachOwedLine(t *testing.T) {
	w := newWorld(t)
	id, rev := w.shapedInReview(stopBody, map[string]string{FactShape: "ticket"})
	res := w.call("review checklist", rev, []string{id}, nil)
	mustOK(t, res)
	raw, _ := json.Marshal(res.Data)
	if want := `[{"id":"C1"},{"id":"D1","text":"The gates pass."},{"id":"D2","text":"The proof is red at the base."},` +
		`{"id":"stop1","text":"The change spends money."}]`; string(raw) != want {
		t.Fatalf("review checklist = %s, want %s", raw, want)
	}

	plain, rev := w.shapedInReview(stopBody, nil)
	if res := w.call("review checklist", rev, []string{plain}, nil); res.Exit != cli.ExitNothing {
		t.Fatalf("review checklist on a ticket with no shape fact: %+v", res)
	}
}

// A pass holds no fail and no extra finding. A reject may hold both.
func TestCodePassWithAFailEntryIsRefused(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)
	id, rev := w.shapedInReview(stopBody, map[string]string{FactShape: "ticket"})

	w.refused(id, rev, "pass", "- C1 pass: red.\n- D1 fail: lint is red.\n- D2 pass: red.\n- stop1 pass: no spend.", "checklist_fail", log, "D1")
	w.refused(id, rev, "pass", "- C1 pass: red.\n- D1 pass: green.\n- D2 pass: red.\n- stop1 pass: no spend.\n- X1 pass: a nit.", "checklist_fail", log, "X1")

	mustOK(t, w.verdict(id, rev, "reject", "- C1 pass: red.\n- D1 fail: lint is red.\n- D2 pass: red.\n- stop1 pass: no spend.\n- X1 fail: a stray log."))
	if it := w.show(id); it.Status != Todo || len(log.calls) != 1 {
		t.Fatalf("a reject with fails: %s %v", it.Status, log.calls)
	}
}

// A stop condition that reads fail sends the ticket to a person, not back
// to the implementer. A pass with it is refused.
func TestFiredStopConditionEscalates(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)
	id, rev := w.shapedInReview(stopBody, map[string]string{FactShape: "ticket"})
	fired := "- C1 pass: red.\n- D1 pass: green.\n- D2 pass: red.\n- stop1 fail: the change calls the paid API."

	w.refused(id, rev, "pass", fired, "checklist_fail", log, "stop1")

	mustOK(t, w.verdict(id, rev, "reject", fired))
	it := w.show(id)
	if it.Status != Escalated || it.Fact(FactResume) != Todo || it.Fact(FactRejects) != "" || len(log.calls) != 1 {
		t.Fatalf("a fired stop condition: %s resume %q rejects %q %v", it.Status, it.Fact(FactResume), it.Fact(FactRejects), log.calls)
	}
	if rounds := Rounds(w.history(id)); len(rounds) != 1 || rounds[0].Verdict != "reject" || rounds[0].Body != fired {
		t.Fatalf("the escalated verdict's round: %+v", rounds)
	}
}

// waived reads only on a stop condition, and only when its evidence names
// an operator answer recorded after the last escalation of that condition.
func TestStopWaiverNeedsAStandingOperatorAnswer(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)
	id, rev := w.shapedInReview(stopBody, map[string]string{FactShape: "ticket"})
	rest := "- C1 pass: red.\n- D1 pass: green.\n- D2 pass: red.\n"

	w.refused(id, rev, "pass", rest+"- stop1 waived: the operator agreed.", "checklist_waiver", log, "stop1")
	w.refused(id, rev, "pass", "- C1 waived: no test needed.\n- D1 pass: green.\n- D2 pass: red.\n- stop1 pass: no spend.", "checklist_waiver", log, "C1")
	if got := w.standing(id, rev); len(got) != 0 {
		t.Fatalf("standing answers before any escalation: %q", got)
	}

	// The first escalation of stop1, and the operator's answer to it.
	mustOK(t, w.verdict(id, rev, "reject", rest+"- stop1 fail: it calls the paid API."))
	mustOK(t, w.apply(id, VerbAnswer, map[string]string{"answer": "Waive stop1: the paid API is in budget."}))
	rev = w.backInReview(id, "rev2")
	first := w.standing(id, rev)
	if len(first) != 1 {
		t.Fatalf("standing answers after the first answer: %q", first)
	}
	w.refused(id, rev, "pass", rest+"- stop1 waived: the operator agreed.", "checklist_waiver", log, "stop1")
	mustOK(t, w.verdict(id, rev, "reject", "- C1 pass: red.\n- D1 fail: lint is red.\n- D2 pass: red.\n- stop1 waived: "+first[0]+" waives it."))
	if got := w.show(id).Status; got != Todo {
		t.Fatalf("a reject with a standing waiver: %s", got)
	}

	// A newer escalation of stop1 supersedes the first answer.
	rev = w.backInReview(id, "rev3")
	mustOK(t, w.verdict(id, rev, "reject", rest+"- stop1 fail: it now calls a second paid API."))
	mustOK(t, w.apply(id, VerbAnswer, map[string]string{"answer": "Waive stop1 again."}))
	rev = w.backInReview(id, "rev4")
	second := w.standing(id, rev)
	if len(second) != 1 || second[0] == first[0] {
		t.Fatalf("standing answers after the second answer: %q, the first was %q", second, first)
	}
	w.refused(id, rev, "pass", rest+"- stop1 waived: "+first[0]+" waives it.", "checklist_waiver", log, "stop1")

	mustOK(t, w.verdict(id, rev, "pass", rest+"- stop1 waived: "+second[0]+" waives it."))
	if got := w.show(id).Status; got != ReadyToMerge {
		t.Fatalf("a pass with a standing waiver: %s", got)
	}
}
