package pipeline_test

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
)

// adrBound sends a PRD epic to the ADR stage, and merges its PRD. The
// epic then waits in architecture with prd_merged.
func (w *world) adrBound() string {
	w.t.Helper()
	epic, n := w.submitted()
	w.review(epic, "pass")
	ok(w.t, w.call(w.coordinator(), "", "epic adr", sendFlags(sendWords), epic))
	w.merge(n)
	w.tick()
	if it := w.show(epic); it.Status != machine.Architecture || it.Fact(machine.FactPRDMerged) == "" {
		w.t.Fatalf("the sent epic after its PRD merge: status %s, prd_merged %q", it.Status, it.Fact(machine.FactPRDMerged))
	}
	return epic
}

// C18: the inbox shows the architecture entry of a PRD epic, and names a
// ticket create that cites a story. Only the coordinator creates that
// ticket, with the operator's words and a story the PRD holds. The ticket
// holds the epic and its stories, and no ticket shape, and it ends the
// entry. Each refusal creates nothing.
func TestC18CoordinatorFilesTheADRTicketOfAPRDEpic(t *testing.T) {
	w := newWorld(t)
	epic := w.adrBound()
	id := "architecture/" + testProject + "/" + epic
	e := entry(w.inbox(), id)
	if e == nil || e.Awaits != inbox.AwaitsCoordinator {
		t.Fatalf("the PRD epic in architecture: entry %+v", e)
	}
	if want := "loop work create --project " + testProject + " --epic " + epic + " --story <a story label of its PRD> --request"; !strings.Contains(e.Next, want) {
		t.Fatalf("next = %q, want it to name %q", e.Next, want)
	}

	body := "Write the ADR that decides where widgets live."
	flags := func(story, request string) map[string]string {
		f := map[string]string{"project": testProject, "epic": epic, "request": request, "title": "Decide the widget store", "body": body}
		if story != "" {
			f["story"] = story
		}
		return f
	}
	for _, c := range []struct {
		name       string
		caller     string
		flags      map[string]string
		code, rule string
	}{
		{"a run", "run", flags("US-01", sendWords), cli.CodeCapabilityDenied, "coordinator_only"},
		{"no story", "coordinator", flags("", sendWords), cli.CodeStoryUnknown, "story_not_in_prd"},
		{"a story the PRD lacks", "coordinator", flags("US-09", sendWords), cli.CodeStoryUnknown, "story_not_in_prd"},
		{"no operator words", "coordinator", flags("US-01", " "), cli.CodeUsage, "no_operator_request"},
	} {
		pr := w.coordinator()
		if c.caller == "run" {
			pr = w.run("run-x", "", machine.StageImplementer)
		}
		res := w.call(pr, "", "work create", c.flags)
		if res.Refusal == nil || res.Refusal.Code != c.code || res.Refusal.Rule != c.rule {
			t.Errorf("%s: want %s %s, got exit %d %+v", c.name, c.code, c.rule, res.Exit, res.Refusal)
		}
	}
	if n := len(w.items(machine.KindTicket)); n != 0 {
		t.Fatalf("the refused creates made %d tickets", n)
	}

	data := ok(t, w.call(w.coordinator(), "", "work create", flags("US-01,US-02", sendWords)))
	it := w.show(data["item"].(string))
	if it.Status != machine.Todo || it.Fact(machine.FactEpic) != epic || it.Fact(machine.FactStories) != "US-01,US-02" ||
		it.Fact(machine.FactRequest) != sendWords || it.Fact(machine.FactShape) != "" {
		t.Fatalf("the ADR ticket: status %s, epic %q, stories %q, request %q, shape %q", it.Status,
			it.Fact(machine.FactEpic), it.Fact(machine.FactStories), it.Fact(machine.FactRequest), it.Fact(machine.FactShape))
	}
	if e := entry(w.inbox(), id); e != nil {
		t.Fatalf("the ADR ticket left the entry %+v", e)
	}
}

// C19: a PRD epic that went through the ADR stage holds prd_merged and
// adr, and its decomposition still refuses a ticket that cites no story
// of its PRD. A batch whose tickets each cite one passes.
func TestC19APRDEpicsDecompositionStillNeedsStoryLabels(t *testing.T) {
	w := newWorld(t)
	epic := w.adrBound()
	w.mustApply(machine.Request{Item: epic, Verb: machine.VerbADRLanded, ExpectedStatus: machine.Architecture,
		Params: map[string]string{"path": "docs/adr/0013-widget-store.md", "added": "a1"}, Actor: machine.Server("test")})
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageSpecAuthor, RunID: "run-spec"}); res.Refusal != nil {
		t.Fatalf("spec author claim: %+v", *res.Refusal)
	}
	w.mustApply(machine.Request{Item: epic, Verb: "pr_open", Params: map[string]string{"pr": "99"}, Actor: machine.Server("test")})
	w.mustApply(machine.Request{Item: epic, Verb: "review_pass", Command: "review submit", Params: map[string]string{"head": "h1"}, Actor: machine.Server("test")})
	w.onBase(map[string]string{specPath(epic, "widgets"): specWith("None.\n")})
	w.mustApply(machine.Request{Item: epic, Verb: "doc_merged", Actor: machine.Server("test")})
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageDecomposer, RunID: "run-dec"}); res.Refusal != nil {
		t.Fatalf("decomposer claim: %+v", *res.Refusal)
	}
	if it := w.show(epic); it.Fact(machine.FactPRDMerged) == "" || it.Fact(machine.FactADR) == "" {
		t.Fatalf("the epic holds prd_merged %q and adr %q", it.Fact(machine.FactPRDMerged), it.Fact(machine.FactADR))
	}

	dec := w.run("run-dec", epic, machine.StageDecomposer)
	unstoried := batch(
		ticket{Key: "list", Title: "List widgets", Stories: []string{"US-01"}},
		ticket{Key: "store", Title: "Store widgets", BlockedBy: []string{"list"}},
	)
	r := wantCode(t, w.call(dec, "", "epic decompose", fileFlag(unstoried)), cli.CodeStoryUnknown)
	if r.Rule != "story_not_in_prd" || !strings.Contains(r.Hint, "ticket store cites no story label") {
		t.Fatalf("a ticket with no story: rule %s, hint %q", r.Rule, r.Hint)
	}
	if n := len(w.items(machine.KindTicket)); n != 0 {
		t.Fatalf("a refused batch created %d tickets", n)
	}
	ok(t, w.call(dec, "k-dec", "epic decompose", fileFlag(threeTickets)))
	if got := w.show(epic).Status; got != machine.Execution {
		t.Fatalf("epic after the batch: %s", got)
	}
}
