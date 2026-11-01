package pipeline_test

import (
	"fmt"
	"maps"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

const qaHead = "4f1c2a9e8b7d6c5f4e3d2c1b0a9f8e7d6c5b4a39"

// qaTicket is a ticket whose review passed at qaHead, and the token of
// the QA run that holds it.
func (w *world) qaTicket() (string, map[string]string) {
	w.t.Helper()
	ticket := w.newItem(machine.KindTicket, machine.VerbCreate)
	if _, err := w.bx.Update(w.ctx, ticket, beadsx.UpdateSpec{IfStatus: machine.Todo, SetMetadata: map[string]string{"loop." + machine.FactReviewedHead: qaHead}}); err != nil {
		w.t.Fatal(err)
	}
	return ticket, map[string]string{"kind": "bug", "source": "qa", "project": testProject,
		"title": "The widgets page is blank at the merge base", "body": "Open /widgets at the merge base: the page is blank."}
}

func operatorBug() map[string]string {
	return map[string]string{"kind": "bug", "project": testProject, "title": "The list page shows no widgets",
		"body": "Open /widgets: the list is empty.", "request": "Ian: the widgets list is empty again, please look"}
}

// C16: a QA run's bug holds source qa, the run and the ticket head it
// read. The coordinator's bug holds source operator, and a bug filed
// before the source fact existed reads as operator.
func TestC16EachBugCarriesItsSource(t *testing.T) {
	w := newWorld(t)
	ticket, bug := w.qaTicket()
	data := ok(t, w.call(w.run("run-qa", ticket, config.StageQA), "k-qa", "work create", bug))
	it := w.show(data["item"].(string))
	if it.Kind != machine.KindBug || it.Status != machine.Reported || it.Fact("source") != "qa" ||
		it.Fact("qa_run") != "run-qa" || it.Fact("qa_head") != qaHead || it.Fact(machine.FactRequest) != "" {
		t.Fatalf("the QA run's bug: %+v", it)
	}

	data = ok(t, w.call(w.coordinator(), "", "work create", operatorBug()))
	if it := w.show(data["item"].(string)); it.Fact("source") != "operator" || it.Fact("qa_run") != "" {
		t.Fatalf("the coordinator's bug: %+v", it)
	}
	explicit := operatorBug()
	explicit["source"] = "operator"
	data = ok(t, w.call(w.coordinator(), "", "work create", explicit))
	if it := w.show(data["item"].(string)); it.Fact("source") != "operator" {
		t.Fatalf("the coordinator's bug with --source operator: %+v", it)
	}

	old := w.newItem(machine.KindBug, machine.VerbFile)
	if it := w.show(old); it.Fact("source") != "operator" {
		t.Fatalf("a bug with no source fact reads as %q, want operator", it.Fact("source"))
	}
}

func wantRule(t *testing.T, res cli.Result, code, rule string) {
	t.Helper()
	if r := wantCode(t, res, code); r.Rule != rule {
		t.Fatalf("want rule %s, got %+v", rule, *r)
	}
}

// C17: an implementer or a code-reviewer run files no bug. The
// coordinator files none with source qa, and a QA run none with another
// source. A source that is neither refuses for every filer, and writes
// no bug.
func TestC17OnlyTheCoordinatorAndAQARunFileABug(t *testing.T) {
	w := newWorld(t)
	ticket, bug := w.qaTicket()
	for _, stage := range []string{machine.StageImplementer, machine.StageCodeReviewer} {
		run := w.run("run-"+stage, ticket, stage)
		wantCode(t, w.call(run, "", "work create", bug), cli.CodeCapabilityDenied)
		plain := maps.Clone(bug)
		delete(plain, "source")
		wantCode(t, w.call(run, "", "work create", plain), cli.CodeCapabilityDenied)
	}

	qa := w.run("run-qa", ticket, config.StageQA)
	coordQA := operatorBug()
	coordQA["source"] = "qa"
	wantRule(t, w.call(w.coordinator(), "", "work create", coordQA), cli.CodeUsage, "source_mismatch")
	none := maps.Clone(bug)
	delete(none, "source")
	wantRule(t, w.call(qa, "", "work create", none), cli.CodeUsage, "source_mismatch")
	asOperator := maps.Clone(bug)
	asOperator["source"] = "operator"
	wantRule(t, w.call(qa, "", "work create", asOperator), cli.CodeUsage, "source_mismatch")
	withRequest := maps.Clone(bug)
	withRequest["request"] = "Ian: please file it"
	wantRule(t, w.call(qa, "", "work create", withRequest), cli.CodeUsage, "qa_takes_no_request")

	for _, v := range []string{"qa-stagehand", "apps/web"} {
		fromCoord := operatorBug()
		fromCoord["source"] = v
		wantRule(t, w.call(w.coordinator(), "", "work create", fromCoord), cli.CodeUsage, "source_unknown")
		fromQA := maps.Clone(bug)
		fromQA["source"] = v
		wantRule(t, w.call(qa, "", "work create", fromQA), cli.CodeUsage, "source_unknown")
	}
	if n := len(w.items(machine.KindBug)); n != 0 {
		t.Fatalf("a refused filing made %d bugs", n)
	}
}

// C17: a QA run files at most 3 bugs. The fourth refuses, a replay of a
// bug it filed still answers that bug, and another QA run has its own 3.
func TestC17AQARunFilesAtMostThreeBugs(t *testing.T) {
	w := newWorld(t)
	ticket, bug := w.qaTicket()
	qa := w.run("run-qa", ticket, config.StageQA)
	var first string
	for n := 1; n <= 3; n++ {
		data := ok(t, w.call(qa, fmt.Sprintf("k-%d", n), "work create", bug))
		if n == 1 {
			first = data["item"].(string)
		}
	}
	wantRule(t, w.call(qa, "k-4", "work create", bug), cli.CodeCapabilityDenied, "qa_bug_cap")
	if again := ok(t, w.call(qa, "k-1", "work create", bug)); again["item"] != first {
		t.Fatalf("the replay of the first bug answered %v, want %s", again["item"], first)
	}
	if n := len(w.items(machine.KindBug)); n != 3 {
		t.Fatalf("the QA run filed %d bugs, want 3", n)
	}
	ok(t, w.call(w.run("run-qa-2", ticket, config.StageQA), "k-1", "work create", bug))
}

// A QA run whose lease on the ticket ended files no bug.
func TestAQARunThatLostItsTicketFilesNoBug(t *testing.T) {
	w := newWorld(t)
	ticket, bug := w.qaTicket()
	qa := w.run("run-qa", ticket, config.StageQA)
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: ticket, Stage: machine.StageImplementer, RunID: "run-impl"}); res.Refusal != nil {
		t.Fatalf("claim: %+v", *res.Refusal)
	}
	wantRule(t, w.call(qa, "", "work create", bug), cli.CodeLeaseNotHeld, "stale_epoch")
	if n := len(w.items(machine.KindBug)); n != 0 {
		t.Fatalf("a refused filing made %d bugs", n)
	}
}
