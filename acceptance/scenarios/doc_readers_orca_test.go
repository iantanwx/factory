//go:build acceptance && orca

package scenarios

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/agenthosttest"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// C14 on real orca: a real doc-reviewer reviews a PRD with no fault. Its
// pack holds a stub reader that returns one false finding for each lens.
// The run passes the PRD, its round names the three readers, and its
// comment carries the Readers line. One session, the doc-reviewer: the
// readers are its subagents.
func TestC14FalseReaderFindingsRejectNothing(t *testing.T) {
	rh, err := acceptance.OrcaHost(agenthosttest.Declared(t, "TestC14FalseReaderFindingsRejectNothing"))
	if err != nil {
		t.Fatal(err)
	}
	q, ok := rh.Agents.(agenthost.QuotaReader)
	if !ok {
		t.Fatal("the orca host reads no quota")
	}
	starts := &kinds{Host: rh.Agents, QuotaReader: q}
	rh.Agents = starts
	w := newWorld(t, acceptance.Options{Config: docReadersConfig, Real: rh,
		Pack: map[string]string{"agents/loop-doc-reader.md": docReadersStub}})
	epic, pr := prdInReview(w, "Greeting file", docReadersPRDPath, docReadersPRD)

	if run := w.Dispatch(machine.StageDocReviewer); run.Item != epic {
		t.Fatalf("the doc-reviewer took %s, want %s", run.Item, epic)
	}
	waitFor(w, epic, machine.PRDSignoff, machine.PRDReview)

	hist, err := w.Srv.Machine.History(context.Background(), acceptance.Project, epic)
	if err != nil {
		t.Fatal(err)
	}
	rounds := machine.Rounds(hist)
	const line = "Readers: unslop, asd-ste100, content-lens."
	if len(rounds) != 1 || rounds[0].Verdict != "pass" || !slices.Equal(rounds[0].Readers, []string{"unslop", "asd-ste100", "content-lens"}) ||
		!strings.Contains(rounds[0].Body, line) {
		t.Fatalf("rounds = %+v, want one pass that names the three readers and holds %q", rounds, line)
	}
	if !slices.ContainsFunc(w.API.World.Comments(pr), func(c map[string]any) bool {
		body, _ := c["body"].(string)
		return strings.Contains(body, line)
	}) {
		t.Fatalf("no comment on #%d holds %q: %v", pr, line, w.API.World.Comments(pr))
	}
	if got := starts.started(); !slices.Equal(got, []agenthost.Kind{agenthost.KindStage}) {
		t.Fatalf("the world started sessions of kinds %v, want 1 stage session", got)
	}
}

// prdInReview puts an epic in prd_review in process, as a brainstorm
// room's submit does, and returns it and its document PR. A real world has
// no room session to submit from.
func prdInReview(w *acceptance.World, title, path, text string) (string, int) {
	w.T.Helper()
	ctx := context.Background()
	epic, ref := w.Srv.Machine.CreateBead(ctx, machine.CreateRequest{Project: acceptance.Project, Kind: machine.KindEpic,
		Title: title, Facts: map[string]string{machine.FactPRDPath: path}})
	if ref != nil {
		w.T.Fatalf("create the epic: %+v", ref)
	}
	dir := w.T.TempDir()
	gt.Git(w.T, dir, "clone", "-q", "-b", "main", w.Remote, ".")
	gt.Git(w.T, dir, "switch", "-q", "-c", github.BranchFor(epic))
	head := gt.Commit(w.T, dir, "docs(prd): add the PRD of "+title, map[string]string{path: text})
	gt.Git(w.T, dir, "push", "-q", "origin", github.BranchFor(epic))
	pr := w.API.World.Add(&gt.PR{Head: github.BranchFor(epic), HeadSHA: head, Title: title})
	w.API.World.Update(pr, func(p *gt.PR) { p.Added = []string{path} })
	res := w.Srv.Machine.Apply(ctx, machine.Request{Project: acceptance.Project, Item: epic, Verb: machine.VerbPRDSubmit,
		ExpectedStatus: machine.Initial, Params: map[string]string{"pr": strconv.Itoa(pr.Number)}, Actor: machine.Server("test")})
	if res.Refusal != nil {
		w.T.Fatalf("prd_submit on %s: %+v", epic, res.Refusal)
	}
	wantStatus(w, epic, machine.PRDReview)
	return epic, pr.Number
}
