package merge_test

import (
	"database/sql"
	"slices"
	"strconv"
	"testing"

	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// U13 and C24: the merge interface never hands a document PR to a
// mechanism. Only the operator merges a PRD, a spec or a design record,
// and that merge is the sign-off. An epic that waits for the sign-off,
// with its PR open and its review passed, stays where it is under every
// mechanism.
func TestDocumentPRIsNeverHandedOff(t *testing.T) {
	for _, d := range drivers() {
		t.Run(d.name, func(t *testing.T) {
			e := newEnv(t, d.mech)
			var prs []*gt.PR
			var epics []string
			for _, want := range []string{machine.PRDSignoff, machine.SpecSignoff, machine.DesignSignoff} {
				pr := e.world.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: sha('d'), Title: "doc"})
				id, res := e.m.Create(e.ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindEpic, Title: "e",
					Verb: "prd_submit", Params: map[string]string{"pr": strconv.Itoa(pr.Number)}, Actor: machine.Server("test")})
				e.mustOK(res)
				e.world.Update(pr, func(p *gt.PR) { p.Head = github.BranchFor(id) })
				e.apply(id, "review_pass", map[string]string{"head": sha('d')})
				switch want {
				case machine.SpecSignoff:
					e.apply(id, "doc_merged", map[string]string{"sha": sha('m')})
					_, res := e.m.Claim(e.ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StagePlanner, RunID: "run-" + id})
					e.mustOK(res)
					pr = e.world.Add(&gt.PR{Head: github.BranchFor(id), HeadSHA: sha('s'), Title: "spec"})
					e.apply(id, "pr_open", map[string]string{"head": sha('s'), "pr": strconv.Itoa(pr.Number)})
					e.apply(id, "review_pass", map[string]string{"head": sha('s')})
				case machine.DesignSignoff:
					e.apply(id, "doc_merged", map[string]string{"sha": sha('m'), "ui_stories": "US-01"})
					pr = e.world.Add(&gt.PR{Head: github.DesignBranchFor(id), HeadSHA: sha('g'), Title: "design"})
					e.apply(id, "pr_open", map[string]string{"head": sha('g'), "pr": strconv.Itoa(pr.Number)})
					e.apply(id, "review_pass", map[string]string{"head": sha('g')})
				}
				prs, epics = append(prs, pr), append(epics, id)
				e.wantStatus(id, want)
			}
			e.postEligibility()
			e.tick()
			e.reactorTick()
			e.tick()
			for _, pr := range prs {
				// direct reports every PR as held, so its merges decide.
				if d.name != "direct" && d.handedOff(e, pr) {
					t.Fatalf("document PR #%d was handed off", pr.Number)
				}
				if slices.ContainsFunc(e.world.Merges(), func(c gt.MergeCall) bool { return c.PR == pr.Number }) {
					t.Fatalf("document PR #%d was merged", pr.Number)
				}
			}
			if calls := e.mergeCalls(); len(calls) != 0 {
				t.Fatalf("the hand-off acted on a document PR: %v", calls)
			}
			var tracked int
			if err := e.st.Read(e.ctx, func(tx *sql.Tx) error {
				return tx.QueryRowContext(e.ctx, `SELECT count(*) FROM merge_tracking`).Scan(&tracked)
			}); err != nil || tracked != 0 {
				t.Fatalf("merge_tracking holds %d rows (%v)", tracked, err)
			}
			e.wantStatus(epics[0], machine.PRDSignoff)
			e.wantStatus(epics[1], machine.SpecSignoff)
			e.wantStatus(epics[2], machine.DesignSignoff)
		})
	}
}
