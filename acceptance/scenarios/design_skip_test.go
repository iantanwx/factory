//go:build acceptance

package scenarios

import (
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// factorio-a4yq C16 and C17: the coordinator's skip of a UI epic in design
// refuses while the design branch heads an open pull request. After the
// operator closes it, the skip moves the epic to spec_needed with the
// operator's words, and a planner takes it.
func TestDesignSkipEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	epic := uiPRDInSignoff(w, "Widgets", "")
	atMerge(w, gt.MergeSHA(docPR(w, epic).Number))
	w.Must(w.Coordinator("inbox", "answer", "signoff/"+acceptance.Project+"/"+epic, "--body", `The operator: "LGTM, ship the PRD."`))
	wantStatus(w, epic, machine.Design)

	const words = "Ian: skip the design, the page copies the settings screen."
	pr := w.API.World.Add(&gt.PR{Head: github.DesignBranchFor(epic)})
	r := w.Coordinator("epic", "skip-design", epic, "--project", acceptance.Project, "--request", words)
	if r.Code != 3 || r.RefusalCode() != cli.CodeShapeRefused || r.Env["rule"] != "design_pr_open" {
		t.Fatalf("the skip with an open design pull request: %s", r)
	}
	wantStatus(w, epic, machine.Design)

	w.API.World.Update(pr, func(p *gt.PR) { p.State = "closed" })
	w.Must(w.Coordinator("epic", "skip-design", epic, "--project", acceptance.Project, "--request", words))
	wantStatus(w, epic, machine.SpecNeeded)
	if got := w.Show(epic).Facts[machine.FactDesignSkipped]; got != words {
		t.Fatalf("design_skipped = %q, want %q", got, words)
	}
	if st, ref := w.TryDispatch(machine.StagePlanner); st == nil || st.Item != epic {
		t.Fatalf("no planner took %s: %+v", epic, ref)
	}
}
