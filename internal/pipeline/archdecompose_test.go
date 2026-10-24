package pipeline_test

import (
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// F-15: an architecture epic has no PRD. Its ADR stands in for the PRD,
// so decompose accepts its batch, and its tickets cite no story label.
// A ticket that names a story label is refused: no PRD holds one.
func TestDecomposeAcceptsAnArchitectureEpic(t *testing.T) {
	w := newWorld(t)
	data := ok(t, w.call(w.coordinator(), "k-arch", "epic create", map[string]string{"title": "Quality ratchets",
		"project": testProject, "architecture": "true", "body": "An ADR records the decision."}))
	epic := data["epic"].(string)
	w.mustApply(machine.Request{Item: epic, Verb: machine.VerbADRLanded, ExpectedStatus: machine.Architecture,
		Params: map[string]string{"path": "docs/adr/0003-quality-ratchets.md"}, Actor: machine.Server("test")})
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageSpecAuthor, RunID: "run-spec"}); res.Refusal != nil {
		t.Fatalf("spec author claim: %+v", *res.Refusal)
	}
	w.mustApply(machine.Request{Item: epic, Verb: "pr_open", Params: map[string]string{"pr": "99"}, Actor: machine.Server("test")})
	w.mustApply(machine.Request{Item: epic, Verb: "review_pass", Command: "review submit", Actor: machine.Server("test")})
	w.mustApply(machine.Request{Item: epic, Verb: "doc_merged", Actor: machine.Server("test")})
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageDecomposer, RunID: "run-dec"}); res.Refusal != nil {
		t.Fatalf("decomposer claim: %+v", *res.Refusal)
	}

	dec := w.run("run-dec", epic, machine.StageDecomposer)
	storied := batch(
		ticket{Key: "ratchet", Title: "Add the coverage ratchet", Body: "Builds C-1."},
		ticket{Key: "gate", Title: "Wire the ratchet into the gates", Body: "Builds C-2.", Stories: []string{"US-01"}, BlockedBy: []string{"ratchet"}},
	)
	if ref := wantCode(t, w.call(dec, "", "epic decompose", fileFlag(storied)), cli.CodeStoryUnknown); ref.Rule != "story_without_prd" {
		t.Fatalf("a story label under an architecture epic: rule %s", ref.Rule)
	}
	if n := len(w.items(machine.KindTicket)); n != 0 {
		t.Fatalf("a refused batch created %d tickets", n)
	}
	if got := w.show(epic).Status; got != machine.Decomposing {
		t.Fatalf("a refused batch moved the epic to %s", got)
	}

	b := batch(
		ticket{Key: "ratchet", Title: "Add the coverage ratchet", Body: "Builds C-1."},
		ticket{Key: "gate", Title: "Wire the ratchet into the gates", Body: "Builds C-2.", BlockedBy: []string{"ratchet"}},
	)
	ok(t, w.call(dec, "k-dec", "epic decompose", fileFlag(b)))
	if n := len(w.items(machine.KindTicket)); n != 2 {
		t.Fatalf("the batch made %d tickets", n)
	}
	if got := w.show(epic).Status; got != machine.Execution {
		t.Fatalf("epic after the batch: %s", got)
	}
}
