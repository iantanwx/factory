package pipeline_test

import (
	"strconv"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
)

const sendWords = "Ian: send this one to the ADR stage first."

func sendFlags(request string) map[string]string {
	return map[string]string{"project": testProject, "request": request}
}

// put moves the epic to status with facts, as a writer outside the server.
func (w *world) put(epic, status string, facts map[string]string) {
	w.t.Helper()
	set := map[string]string{}
	for k, v := range facts {
		set["loop."+k] = v
	}
	if _, err := w.bx.Update(w.ctx, epic, beadsx.UpdateSpec{IfStatus: w.show(epic).Status, Status: status, SetMetadata: set}); err != nil {
		w.t.Fatal(err)
	}
}

// C9: the coordinator's send on an epic in prd_signoff keeps the status
// and records the operator's words. The operator's merge of the PRD then
// lands the epic on architecture, not on spec_needed.
func TestC9EpicADRBeforeThePRDMergeRoutesTheMerge(t *testing.T) {
	w := newWorld(t)
	epic, n := w.submitted()
	w.review(epic, "pass")
	out := ok(t, w.call(w.coordinator(), "", "epic adr", sendFlags(sendWords), epic))
	if out["status"] != "prd_signoff" {
		t.Fatalf("epic adr answered %v", out)
	}
	if it := w.show(epic); it.Status != "prd_signoff" || it.Fact("adr_sent") != sendWords {
		t.Fatalf("after the send: status %s, adr_sent %q", it.Status, it.Fact("adr_sent"))
	}
	w.merge(n)
	w.tick()
	if it := w.show(epic); it.Status != "architecture" || it.Fact("prd_merged") == "" {
		t.Fatalf("after the operator's merge: status %s, prd_merged %q", it.Status, it.Fact("prd_merged"))
	}
}

// C16: the send on an unheld epic in spec_review returns it to
// architecture, and the spec pull request stays in doc_pr.
func TestC16EpicADRInTheSpecStageReturnsToArchitecture(t *testing.T) {
	w := newWorld(t)
	epic, _ := w.submitted()
	w.put(epic, "spec_review", map[string]string{"prd_merged": "m1", "doc_pr": "42"})
	out := ok(t, w.call(w.coordinator(), "", "epic adr", sendFlags(sendWords), epic))
	if out["status"] != "architecture" {
		t.Fatalf("epic adr answered %v", out)
	}
	if it := w.show(epic); it.Status != "architecture" || it.Fact("adr_sent") != sendWords || it.Fact("doc_pr") != "42" {
		t.Fatalf("after the send: status %s, adr_sent %q, doc_pr %q", it.Status, it.Fact("adr_sent"), it.Fact("doc_pr"))
	}
}

// C15: epic adr refuses outside its rows, on a held epic in the spec
// stage, for a caller other than the coordinator, and with no operator
// words. Each refusal changes nothing.
func TestC15EpicADRRefusedOutsideItsRows(t *testing.T) {
	w := newWorld(t)
	type refusal struct {
		name, status string
		held         bool
		caller       string
		request      string
		code, rule   string
	}
	cases := []refusal{
		{name: "a blank request", status: "prd_signoff", request: "  ", code: cli.CodeUsage, rule: "no_operator_request"},
		{name: "no request", status: "prd_signoff", code: cli.CodeUsage, rule: "no_operator_request"},
		{name: "a run", status: "prd_signoff", caller: "run", request: sendWords, code: cli.CodeCapabilityDenied, rule: "coordinator_only"},
		{name: "a held spec review", status: "spec_review", held: true, request: sendWords, code: cli.CodeLeaseHeld, rule: "lease_held"},
	}
	for _, s := range []string{"spec_drafting", "spec_approved", "decomposing", "execution", "done", "escalated", "architecture"} {
		cases = append(cases, refusal{name: s, status: s, request: sendWords, code: cli.CodeTransitionNotAllowed, rule: "no_edge"})
	}
	for _, c := range cases {
		epic, res := w.m.Create(w.ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindEpic, Title: c.name,
			Verb: machine.VerbCreatePRD, Actor: machine.Server("test")})
		ok(t, res)
		w.put(epic, c.status, map[string]string{"prd_merged": "m1"})
		if c.held {
			if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageDocReviewer, RunID: "run-rev"}); res.Refusal != nil {
				t.Fatalf("%s: claim: %+v", c.name, *res.Refusal)
			}
		}
		pr := w.coordinator()
		if c.caller == "run" {
			pr = w.run("run-x", epic, machine.StageImplementer)
		}
		flags := sendFlags(c.request)
		if c.request == "" {
			delete(flags, "request")
		}
		before := w.show(epic)
		if r := wantCode(t, w.call(pr, "", "epic adr", flags, epic), c.code); r.Rule != c.rule {
			t.Errorf("%s: rule %q, want %q", c.name, r.Rule, c.rule)
		}
		if after := w.show(epic); after.Status != c.status || after.Seq != before.Seq || after.Fact("adr_sent") != "" {
			t.Fatalf("%s: the refusal changed the epic: %+v", c.name, after)
		}
	}
}

func offFlags(request string) map[string]string {
	flags := sendFlags(request)
	flags["off"] = "true"
	return flags
}

// The answer to PRD open question 4: epic adr --off on an epic in
// prd_signoff clears adr_needed and adr_sent and keeps the status. The
// operator's merge of the PRD then lands the epic on spec_needed.
func TestEpicADROffClearsBothFactsFromPRDSignoff(t *testing.T) {
	w := newWorld(t)
	epic, n := w.submitted()
	w.review(epic, "pass")
	w.put(epic, "prd_signoff", map[string]string{"adr_needed": "R3 binds every epic"})
	ok(t, w.call(w.coordinator(), "", "epic adr", sendFlags(sendWords), epic))
	out := ok(t, w.call(w.coordinator(), "", "epic adr", offFlags("Ian: no ADR for this one."), epic))
	if out["status"] != "prd_signoff" {
		t.Fatalf("epic adr --off answered %v", out)
	}
	if it := w.show(epic); it.Status != "prd_signoff" || it.Fact("adr_needed") != "" || it.Fact("adr_sent") != "" {
		t.Fatalf("after --off: status %s, adr_needed %q, adr_sent %q", it.Status, it.Fact("adr_needed"), it.Fact("adr_sent"))
	}
	w.merge(n)
	w.tick()
	if it := w.show(epic); it.Status != "spec_needed" || it.Fact("prd_merged") == "" {
		t.Fatalf("after the operator's merge: status %s, prd_merged %q", it.Status, it.Fact("prd_merged"))
	}
}

// epic adr --off starts from prd_signoff only, and needs the operator's
// words. Each refusal changes nothing.
func TestEpicADROffRefusedOutsidePRDSignoff(t *testing.T) {
	w := newWorld(t)
	cases := []struct {
		name, status, request, code, rule string
	}{
		{"no request", "prd_signoff", "", cli.CodeUsage, "no_operator_request"},
	}
	for _, s := range []string{"prd_needed", "prd_review", "prd_amending", "spec_needed", "spec_review", "architecture"} {
		cases = append(cases, struct{ name, status, request, code, rule string }{s, s, sendWords, cli.CodeTransitionNotAllowed, "no_edge"})
	}
	for _, c := range cases {
		epic, res := w.m.Create(w.ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindEpic, Title: c.name,
			Verb: machine.VerbCreatePRD, Actor: machine.Server("test")})
		ok(t, res)
		w.put(epic, c.status, map[string]string{"adr_needed": "R3", "adr_sent": sendWords})
		flags := offFlags(c.request)
		if c.request == "" {
			delete(flags, "request")
		}
		before := w.show(epic)
		if r := wantCode(t, w.call(w.coordinator(), "", "epic adr", flags, epic), c.code); r.Rule != c.rule {
			t.Errorf("%s: rule %q, want %q", c.name, r.Rule, c.rule)
		}
		if after := w.show(epic); after.Status != c.status || after.Seq != before.Seq || after.Fact("adr_needed") != "R3" || after.Fact("adr_sent") != sendWords {
			t.Fatalf("%s: the refusal changed the epic: %+v", c.name, after)
		}
	}
}

// C16, across adr_landed: an epic that the send returned to architecture
// from spec_review writes on its ADR branch, and keeps doc_pr. The merge
// of an ADR that names it moves it to spec_needed, and the planner's
// pr show then finds the same open spec pull request on the spec branch.
func TestC16SpecPullRequestOutlivesTheADRStage(t *testing.T) {
	w := newWorld(t)
	epic, n := w.submitted()
	w.review(epic, "pass")
	w.merge(n)
	w.tick()
	w.specOpen(epic, "run-spec-1")
	spec := w.prOf(epic)
	ok(t, w.call(w.coordinator(), "", "epic adr", sendFlags(sendWords), epic))
	if it := w.show(epic); it.Status != "architecture" || github.BranchOf(it) != "loop/"+epic+"-adr" {
		t.Fatalf("after the send: status %s, branch %s", it.Status, github.BranchOf(it))
	}
	w.onBase(map[string]string{"docs/adr/0001-widgets.md": "# ADR 0001: Widgets\n\n**Ticket:** " + epic + "\n"})
	w.tick()
	it := w.show(epic)
	if it.Status != "spec_needed" || it.Fact("doc_pr") != strconv.Itoa(spec) || github.BranchOf(it) != "loop/"+epic+"-spec" {
		t.Fatalf("after the ADR landed: status %s, doc_pr %q, branch %s, want spec_needed, %d, loop/%s-spec", it.Status,
			it.Fact("doc_pr"), github.BranchOf(it), spec, epic)
	}
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StagePlanner, RunID: "run-spec-2"}); res.Refusal != nil {
		t.Fatalf("planner claim: %+v", *res.Refusal)
	}
	out := ok(t, w.call(w.run("run-spec-2", epic, machine.StagePlanner), "", "pr show", map[string]string{}))
	if got := out["number"]; got != float64(spec) {
		t.Fatalf("pr show after the ADR stage = %v, want the spec pull request #%d", out, spec)
	}
}
