//go:build acceptance

package scenarios

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// uiPRD is a PRD with one UI story, US-01.
const uiPRD = `# Widgets

## US-01 List widgets

**UI:** yes

A visitor sees every widget.

## US-02 Store widgets

**UI:** no

The server keeps every widget.
`

// uiConfig is a project config that declares a UI.
const uiConfig = "merge:\n  mechanism: direct\nui:\n  kind: web\n  paths: [web/**]\n"

// uiPRDInSignoff submits uiPRD from a brainstorm room and passes its
// review. A send names the operator's words that send the epic to the ADR
// stage, or is empty.
func uiPRDInSignoff(w *acceptance.World, title, send string) string {
	w.T.Helper()
	room, co := openRoom(w, "brainstorm", "", "the operator wants "+title)
	w.Commit(co, "PRD: "+title, map[string]string{prdPath: uiPRD})
	epic := w.Must(w.In(co, "prd", "submit", "--path", prdPath, "--title", title)).Str("epic")
	w.Must(w.Coordinator("room", "close", room))
	docPass(w, epic)
	if send != "" {
		w.Must(w.Coordinator("epic", "adr", epic, "--project", acceptance.Project, "--request", send))
	}
	wantStatus(w, epic, machine.PRDSignoff)
	return epic
}

// atMerge serves uiConfig and uiPRD at a merge commit, as the merge of a
// UI project's PRD holds them.
func atMerge(w *acceptance.World, sha string) {
	w.API.World.SetFile(sha, ".loop/config.yaml", uiConfig)
	w.API.World.SetFile(sha, prdPath, uiPRD)
}

func wantUIStories(w *acceptance.World, epic, want string) {
	w.T.Helper()
	if got := w.Show(epic).Facts[machine.FactUIStories]; got != want {
		w.T.Fatalf("%s holds ui_stories %q, want %q", epic, got, want)
	}
}

// factorio-a4yq C12, C13 and C15: in a project that declares a UI, the
// operator's sign-off of a PRD with a UI story lands the epic on design,
// and no planner takes it. A UI epic that waits for an ADR lands on
// design when its ADR merges, from architecture and from adr_review. An
// epic in design that an ADR names only in prose returns to architecture.
func TestUIEpicRoutesToTheDesignStageEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})

	e1 := uiPRDInSignoff(w, "Widgets", "")
	atMerge(w, gt.MergeSHA(docPR(w, e1).Number))
	w.Must(w.Coordinator("inbox", "answer", "signoff/"+acceptance.Project+"/"+e1, "--body", `The operator: "LGTM, ship the PRD."`))
	wantStatus(w, e1, machine.Design)
	wantUIStories(w, e1, "US-01")
	if st, _ := w.TryDispatch(machine.StagePlanner); st != nil {
		t.Fatalf("a planner took %s", st.Item)
	}

	e2 := uiPRDInSignoff(w, "Gadgets", "Ian: gadgets need an ADR first.")
	e3 := uiPRDInSignoff(w, "Gizmos", "Ian: gizmos need an ADR first.")
	for _, ep := range []string{e2, e3} {
		atMerge(w, w.MergeByHand(docPR(w, ep)))
	}
	w.React()
	for _, ep := range []string{e2, e3} {
		wantStatus(w, ep, machine.Architecture)
		wantUIStories(w, ep, "US-01")
	}

	// Replay the write the reactor before F-10's fix made for an epic that
	// ADR 7 names only in prose.
	const adr7 = "docs/adr/0007-gadget-store.md"
	res := w.Srv.Machine.Apply(context.Background(), machine.Request{Project: acceptance.Project, Item: e3,
		Verb: machine.VerbADRLanded, ExpectedStatus: machine.Architecture, Params: map[string]string{"path": adr7, "added": "a1"},
		Actor: machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("adr_landed on %s: %+v", e3, res.Refusal)
	}
	wantStatus(w, e3, machine.Design)
	w.PushMain("ADR 7", map[string]string{adr7: "# ADR 0007: Gadget store\n\n**Ticket:** " + e2 +
		"\n\nEpic " + e3 + " reads the same store later.\n"}, "")
	w.React()
	wantStatus(w, e2, machine.Design)
	wantStatus(w, e3, machine.Architecture)

	// e3's own ADR pull request opens a review, and its merge outruns the
	// review.
	const adr8 = "docs/adr/0008-gizmo-store.md"
	text := "# ADR 0008: Gizmo store\n\n**Ticket:** " + e3 + "\n\nUse Postgres.\n"
	dir := t.TempDir()
	gt.Git(t, dir, "clone", "-q", "-b", "main", w.Remote, ".")
	gt.Git(t, dir, "switch", "-q", "-c", github.ADRBranchFor(e3))
	head := gt.Commit(t, dir, "docs(adr): 0008 gizmo store", map[string]string{adr8: text})
	gt.Git(t, dir, "push", "-q", "origin", github.ADRBranchFor(e3))
	pr := w.API.World.Add(&gt.PR{Head: github.ADRBranchFor(e3), HeadSHA: head})
	w.API.World.Update(pr, func(p *gt.PR) { p.Added = []string{adr8} })
	w.API.World.SetFile(pr.HeadSHA, adr8, text)
	w.React()
	wantStatus(w, e3, machine.ADRReview)
	w.MergeByHand(pr)
	w.React()
	wantStatus(w, e3, machine.Design)
	wantUIStories(w, e3, "US-01")
}
