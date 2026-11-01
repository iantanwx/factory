package pipeline_test

import (
	"fmt"
	"maps"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
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
// source or with a request. A source that is neither refuses for every
// filer. No refusal writes a bug.
func TestC17OnlyTheCoordinatorAndAQARunFileABug(t *testing.T) {
	w := newWorld(t)
	ticket, bug := w.qaTicket()
	qa := w.run("run-qa", ticket, config.StageQA)
	implementer := w.run("run-impl", ticket, machine.StageImplementer)
	reviewer := w.run("run-rev", ticket, machine.StageCodeReviewer)
	with := func(base map[string]string, k, v string) map[string]string {
		out := maps.Clone(base)
		if v == "" {
			delete(out, k)
		} else {
			out[k] = v
		}
		return out
	}
	cases := []struct {
		name       string
		pr         rpc.Principal
		flags      map[string]string
		code, rule string
	}{
		{"implementer with source qa", implementer, bug, cli.CodeCapabilityDenied, "bug_filer"},
		{"implementer with no source", implementer, with(bug, "source", ""), cli.CodeCapabilityDenied, "bug_filer"},
		{"code-reviewer with source qa", reviewer, bug, cli.CodeCapabilityDenied, "bug_filer"},
		{"coordinator with source qa", w.coordinator(), with(operatorBug(), "source", "qa"), cli.CodeUsage, "source_mismatch"},
		{"QA run with no source", qa, with(bug, "source", ""), cli.CodeUsage, "source_mismatch"},
		{"QA run with source operator", qa, with(bug, "source", "operator"), cli.CodeUsage, "source_mismatch"},
		{"QA run with a request", qa, with(bug, "request", "Ian: please file it"), cli.CodeUsage, "qa_takes_no_request"},
		{"coordinator with source qa-stagehand", w.coordinator(), with(operatorBug(), "source", "qa-stagehand"), cli.CodeUsage, "source_unknown"},
		{"coordinator with source apps/web", w.coordinator(), with(operatorBug(), "source", "apps/web"), cli.CodeUsage, "source_unknown"},
		{"QA run with source qa-stagehand", qa, with(bug, "source", "qa-stagehand"), cli.CodeUsage, "source_unknown"},
		{"QA run with source apps/web", qa, with(bug, "source", "apps/web"), cli.CodeUsage, "source_unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantRule(t, w.call(c.pr, "", "work create", c.flags), c.code, c.rule)
		})
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
