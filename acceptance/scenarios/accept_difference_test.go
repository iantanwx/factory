//go:build acceptance

package scenarios

import (
	"encoding/json"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/design"
	"github.com/iantanwx/loop/internal/machine"
)

// stateRecord is a design record with the states S1.1 and S1.2.
const stateRecord = "# Widgets: design record\n\n## Screens\n\n### S1. Widget list\n\n**Stories:** US-01\n\n" +
	"- **S1.1 Empty.** Picture `s1-1.png`, target `/widgets?empty`.\n" +
	"- **S1.2 Full.** Picture `s1-2.png`, target `/widgets`, pixel.\n"

// factorio-11pc C15 and C16: a UI epic whose design record merged reaches
// execution. The coordinator's acceptance of a state the record lacks
// changes nothing, and its acceptance of S1.2 appends the operator's
// words. The epic stays in execution.
func TestC15CoordinatorAcceptsADifferenceEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	epic := uiEpicInDesign(w, "Widgets")
	pr := openDesignPull(w, epic, "the operator wants the widgets designed", stateRecord)
	designVerdict(w, epic, "pass", machine.DesignSignoff)
	sha := w.MergeByHand(pr)
	w.API.World.SetFile(sha, design.Dir(epic)+design.RecordFile, stateRecord)
	w.React()
	wantStatus(w, epic, machine.SpecNeeded)

	draftSpec(w, epic, "draft one")
	docReview(w, epic, "pass")
	wantStatus(w, epic, machine.SpecSignoff)
	w.MergeByHand(docPR(w, epic))
	w.React()
	wantStatus(w, epic, machine.SpecApproved)
	// The PRD merge served uiPRD only through the API, and the decomposer
	// reads it on the base.
	w.PushMain("PRD: Widgets", map[string]string{prdPath: uiPRD}, "")
	dec := w.Dispatch(machine.StageDecomposer)
	batch, _ := json.Marshal(map[string]any{"tickets": []map[string]any{
		{"key": "list", "title": "List widgets", "body": ticketBody("Create `widgets/list.txt` holding the line `list`."), "stories": []string{"US-01"}},
	}})
	w.Must(w.In(dec.Checkout, "epic", "decompose", "--file", w.WriteFile("decomposition.json", string(batch))))
	w.Must(w.In(dec.Checkout, "run", "finish", "--outcome", "done"))
	wantStatus(w, epic, machine.Execution)

	const words = `The operator: "the badge sits 4px lower on the phone, and that is fine."`
	before := w.Show(epic).Facts
	if r := w.Coordinator("epic", "accept-difference", epic, "--project", acceptance.Project, "--state", "S9.9", "--request", words); r.RefusalCode() != "SHAPE_REFUSED" {
		t.Fatalf("the acceptance of an unknown state = %s", r)
	}
	if got := w.Show(epic).Facts[machine.FactAcceptedDifferences]; got != before[machine.FactAcceptedDifferences] {
		t.Fatalf("a refused acceptance wrote accepted_differences %q", got)
	}
	w.Must(w.Coordinator("epic", "accept-difference", epic, "--project", acceptance.Project, "--state", "S1.2", "--request", words))
	wantStatus(w, epic, machine.Execution)
	var got []struct{ State, Words string }
	if err := json.Unmarshal([]byte(w.Show(epic).Facts[machine.FactAcceptedDifferences]), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].State != "S1.2" || got[0].Words != words {
		t.Fatalf("accepted_differences = %+v, want one entry of S1.2 with the operator's words", got)
	}
}
