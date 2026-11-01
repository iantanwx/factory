package github

import (
	"context"
	"strconv"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// epicInReview submits a PRD epic whose document PR is pr.
func (e *env) epicInReview(pr int) string {
	e.t.Helper()
	id, res := e.m.Create(context.Background(), machine.CreateRequest{Project: testProject, Kind: machine.KindEpic, Title: "e",
		Verb: "prd_submit", Params: map[string]string{"pr": strconv.Itoa(pr)}, Actor: machine.Server("test")})
	mustOK(e.t, res)
	return id
}

// U13: a PRD PR merged while the doc-reviewer holds it counts as the
// operator's sign-off. The merge lands on sign-off first, as the outran
// row says, and the sign-off row carries it on in the same read. The
// review lease is revoked.
func TestDocumentMergedDuringReviewIsTheSignoff(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	pr := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	id := e.epicInReview(pr.Number)
	w.Update(pr, func(p *gt.PR) { p.Head = BranchFor(id) })
	l, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageDocReviewer, RunID: "run-rev"})
	mustOK(t, res)
	r := e.reactor()
	w.Update(pr, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaM })
	e.tick(r)
	it := e.status(id)
	if it.Status != machine.SpecNeeded || it.Owner != "" || it.Epoch <= l.Epoch || it.Fact(machine.FactPRDMerged) != shaM {
		t.Fatalf("after the merge in review: %s owner %q epoch %d %v", it.Status, it.Owner, it.Epoch, it.Facts)
	}
}

// Only the epic's current document PR moves it. The merged PRD PR, read
// again while the spec waits for its author or its sign-off, moves
// nothing, and it is no fault that blocks the reconcile.
func TestOnlyTheCurrentDocumentPRMovesTheEpic(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	prd := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	id := e.epicInReview(prd.Number)
	w.Update(prd, func(p *gt.PR) { p.Head = BranchFor(id) })
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "review_pass", Params: map[string]string{"head": shaA}, Actor: machine.Server("test")}))
	r := e.reactor()
	w.Update(prd, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaM })
	e.tick(r)
	if got := e.status(id).Status; got != machine.SpecNeeded {
		t.Fatalf("the operator's merge left the epic in %s", got)
	}
	// The spec PR opens, passes review and waits for the operator.
	_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StagePlanner, RunID: "run-spec"})
	mustOK(t, res)
	spec := w.Add(&gt.PR{Head: SpecBranchFor(id), HeadSHA: shaB})
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "pr_open",
		Params: map[string]string{"pr": strconv.Itoa(spec.Number)}, Actor: machine.Server("test")}))
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "review_pass", Params: map[string]string{"head": shaB}, Actor: machine.Server("test")}))
	// The old PRD PR is read again: the spec is not signed off by it.
	w.Update(prd, func(p *gt.PR) {})
	e.tick(r)
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.status(id).Status; got != machine.SpecSignoff {
		t.Fatalf("the merged PRD PR moved the epic to %s", got)
	}
	w.Update(spec, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaM })
	e.tick(r)
	if got := e.status(id).Status; got != machine.SpecApproved {
		t.Fatalf("the spec merge left the epic in %s", got)
	}
}

// C2, C18, C19, C20: the reactor records the path of a spec that merges on
// GitHub. The path is the one file of the spec PR at
// docs/specs/<epic>-*-spec.md or docs/plans/<epic>-*-spec.md. A PRD merge
// records none, even with such a file. A spec PR with two such files, in
// one directory or one in each, records none, and the epic still moves to
// spec_approved.
func TestSpecMergeOnGitHubRecordsTheSpecPath(t *testing.T) {
	for _, c := range []struct {
		name  string
		files func(epic string) []string
		want  func(epic string) string
	}{
		{
			"one spec file",
			func(epic string) []string { return []string{"README.md", "docs/plans/" + epic + "-cache-spec.md"} },
			func(epic string) string { return "docs/plans/" + epic + "-cache-spec.md" },
		},
		{
			"one spec file under docs/specs",
			func(epic string) []string { return []string{"README.md", "docs/specs/" + epic + "-cache-spec.md"} },
			func(epic string) string { return "docs/specs/" + epic + "-cache-spec.md" },
		},
		{
			"one spec file in each directory",
			func(epic string) []string {
				return []string{"docs/specs/" + epic + "-cache-spec.md", "docs/plans/" + epic + "-cache-spec.md"}
			},
			func(string) string { return "" },
		},
		{
			"two spec files",
			func(epic string) []string {
				return []string{"docs/plans/" + epic + "-cache-spec.md", "docs/plans/" + epic + "-store-spec.md"}
			},
			func(string) string { return "" },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			w := e.withWorld()
			ctx := context.Background()
			prd := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
			id := e.epicInReview(prd.Number)
			w.Update(prd, func(p *gt.PR) { p.Head, p.Files = BranchFor(id), c.files(id) })
			mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "review_pass", Params: map[string]string{"head": shaA}, Actor: machine.Server("test")}))
			r := e.reactor()
			w.Update(prd, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaM })
			e.tick(r)
			if it := e.status(id); it.Status != machine.SpecNeeded || it.Fact(machine.FactSpecPath) != "" {
				t.Fatalf("after the PRD merge: %s %v", it.Status, it.Facts)
			}

			_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StagePlanner, RunID: "run-spec"})
			mustOK(t, res)
			spec := w.Add(&gt.PR{Head: SpecBranchFor(id), HeadSHA: shaB, Files: c.files(id)})
			mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "pr_open",
				Params: map[string]string{"pr": strconv.Itoa(spec.Number)}, Actor: machine.Server("test")}))
			mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "review_pass", Params: map[string]string{"head": shaB}, Actor: machine.Server("test")}))
			w.Update(spec, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaM })
			e.tick(r)
			if it := e.status(id); it.Status != machine.SpecApproved || it.Fact(machine.FactSpecPath) != c.want(id) {
				t.Fatalf("after the spec merge: %s %v, want spec_path %q", it.Status, it.Facts, c.want(id))
			}
		})
	}
}

// A closed document PR escalates the epic, and never cancels it.
func TestClosedDocumentPREscalatesTheEpic(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	pr := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	id := e.epicInReview(pr.Number)
	w.Update(pr, func(p *gt.PR) { p.Head = BranchFor(id) })
	r := e.reactor()
	w.Update(pr, func(p *gt.PR) { p.State = "closed" })
	e.tick(r)
	if it := e.status(id); it.Status != machine.Escalated || it.Fact(machine.FactResume) != machine.PRDReview {
		t.Fatalf("after the close: %s %v", it.Status, it.Facts)
	}
}

// runCall is one verb from a run of stage that holds item at its lease
// epoch, inside the env's checkout.
func (e *env) runCall(path, item, stage string, flags, extra map[string]string) cli.Result {
	e.t.Helper()
	v, ok := cli.Surface().Verb(path)
	if !ok {
		e.t.Fatalf("no verb %q", path)
	}
	if flags == nil {
		flags = map[string]string{}
	}
	if extra == nil {
		extra = map[string]string{}
	}
	it := e.status(item)
	call := &rpc.Call{Inv: cli.Invocation{Verb: v, Flags: flags, Extra: extra, RequestID: "req-" + path},
		Principal: rpc.Principal{Kind: cli.PrincipalRun, RunID: "run-" + stage, Project: testProject, Item: item,
			Stage: stage, Checkout: e.checkout, Epoch: it.Epoch}}
	h, ok := e.svc.Handlers()[path]
	if !ok {
		e.t.Fatalf("no GitHub handler for %q", path)
	}
	return h(context.Background(), call)
}

// The PRD and the spec each have their own branch. The operator squash
// merges a PRD of two commits, so the PRD branch keeps commits whose patch
// the base lacks. The planner's checkout starts from the base, and its
// push lands on the spec branch, not on the PRD branch, where it would find
// the remote ahead. Its PR then moves the epic to spec review, and the
// reactor maps the spec PR back to the epic.
func TestSpecAfterASquashMergedPRDHasItsOwnBranch(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()

	// The brainstorm room's PRD: two commits on loop/<epic>.
	gitT(t, e.checkout, "checkout", "-q", "-b", "prd", e.base)
	commit(t, e.checkout, "prd draft", map[string]string{"docs/prd.md": "# PRD\n\nUS-01 draft\n"})
	prdHead := commit(t, e.checkout, "prd amend", map[string]string{"docs/prd.md": "# PRD\n\nUS-01 final\n"})
	prd := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: prdHead})
	id := e.epicInReview(prd.Number)
	w.Update(prd, func(p *gt.PR) { p.Head = BranchFor(id) })
	if _, err := e.push(id, prdHead); err != nil {
		t.Fatalf("PRD push: %v", err)
	}

	// The operator squash merges it: one new commit on main.
	gitT(t, e.checkout, "checkout", "-q", "-b", "squash", e.base)
	squash := commit(t, e.checkout, "PRD (#1)", map[string]string{"docs/prd.md": "# PRD\n\nUS-01 final\n"})
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), squash+":refs/heads/main")
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "review_pass", Params: map[string]string{"head": prdHead}, Actor: machine.Server("test")}))
	r := e.reactor()
	w.Update(prd, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, squash })
	e.tick(r)
	if it := e.status(id); it.Status != machine.SpecNeeded || it.Fact(machine.FactPRDMerged) == "" {
		t.Fatalf("after the squash merge: %s %v", it.Status, it.Facts)
	}

	// The planner starts from the base and pushes its spec.
	_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StagePlanner, RunID: "run-spec"})
	mustOK(t, res)
	gitT(t, e.checkout, "checkout", "-q", "-b", "spec", squash)
	specHead := commit(t, e.checkout, "spec", map[string]string{"docs/spec.md": "# Spec\n"})
	res = e.runCall("branch push", id, machine.StagePlanner, nil, map[string]string{cli.ExtraHead: specHead})
	mustOK(t, res)
	if got := e.remoteSHA(SpecBranchFor(id)); got != specHead {
		t.Fatalf("the spec branch holds %q, want %s", got, specHead)
	}
	if got := e.remoteSHA(BranchFor(id)); got != prdHead {
		t.Fatalf("the PRD branch moved to %q", got)
	}

	// branch sync names the spec branch as the item's branch.
	sync := decodeSync(t, e.runCall("branch sync", id, machine.StagePlanner, nil, nil))
	if sync.Item.Branch != SpecBranchFor(id) || sync.Item.SHA != specHead {
		t.Fatalf("branch sync item = %+v", sync.Item)
	}

	// pr open opens the spec PR on the spec branch and hands the epic to
	// review.
	res = e.runCall("pr open", id, machine.StagePlanner, map[string]string{"title": "Spec"}, nil)
	mustOK(t, res)
	it := e.status(id)
	if it.Status != machine.SpecReview {
		t.Fatalf("after pr open: %s", it.Status)
	}
	var spec *gt.PR
	for _, p := range w.PRs() {
		if p.Head == SpecBranchFor(id) {
			spec = w.PR(p.Number)
		}
	}
	if spec == nil || it.Fact(machine.FactDocPR) != strconv.Itoa(spec.Number) {
		t.Fatalf("no spec PR on %s, or doc_pr %q names another: %v", SpecBranchFor(id), it.Fact(machine.FactDocPR), w.PRs())
	}
	if res := e.runCall("pr show", id, machine.StagePlanner, nil, nil); res.Refusal != nil || res.Exit != cli.ExitDone {
		t.Fatalf("pr show of the spec = %+v", res)
	}

	// The operator signs the spec off by merge; the reactor maps the spec
	// branch to the epic.
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "review_pass", Params: map[string]string{"head": specHead}, Actor: machine.Server("test")}))
	w.Update(spec, func(p *gt.PR) { p.State, p.Merged, p.MergeSHA = "closed", true, shaM })
	e.tick(r)
	if got := e.status(id).Status; got != machine.SpecApproved {
		t.Fatalf("the spec merge left the epic in %s", got)
	}
}

// A spec branch names its epic, and no other branch does.
func TestSpecEpicOf(t *testing.T) {
	for branch, want := range map[string]string{
		"loop/tk-1-spec": "tk-1", "loop/tk-1": "", "loop/-spec": "", "main": "", "loop/a/b-spec": "",
	} {
		if got := SpecEpicOf(branch); got != want {
			t.Errorf("SpecEpicOf(%q) = %q, want %q", branch, got, want)
		}
	}
}

// An architecture epic waits until an ADR whose Ticket line names it lands
// on the base branch. The reactor reads the ADR's text in the mirror: a
// file outside the ADR directory, a file that is not Markdown, and an ADR
// whose Ticket line names another id that starts with this one move
// nothing. A full stop after the id on the Ticket line still names it.
func TestADRNamingTheEpicLandsIt(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	id, res := e.m.Create(context.Background(), machine.CreateRequest{Project: testProject, Kind: machine.KindEpic,
		Title: "Move the store to Postgres", Verb: "create_architecture", Actor: machine.Server("test")})
	mustOK(t, res)
	r := e.reactor()
	land := func(files map[string]string) {
		t.Helper()
		gt.Commit(t, e.checkout, "land", files)
		gt.Git(t, e.checkout, "push", "-q", e.git.RemoteDir(), "HEAD:main")
		e.tick(r)
	}
	land(map[string]string{
		"docs/notes.md":       "**Ticket:** " + id + "\n",
		"docs/adr/README.txt": "**Ticket:** " + id + "\n",
		"docs/adr/0001-x.md":  "# ADR 0001: Other\n\n**Ticket:** " + id + "0\n",
	})
	if it := e.status(id); it.Status != machine.Architecture {
		t.Fatalf("no ADR names %s, and it moved to %s", id, it.Status)
	}
	land(map[string]string{"docs/adr/0002-postgres.md": "# ADR 0002: Postgres\n\n**Ticket:** " + id + ".\n"})
	it := e.status(id)
	if it.Status != machine.SpecNeeded || it.Fact(machine.FactADR) != "docs/adr/0002-postgres.md" {
		t.Fatalf("after the ADR landed: %s %v", it.Status, it.Facts)
	}
	e.tick(r)
	if it := e.status(id); it.Status != machine.SpecNeeded {
		t.Fatalf("a second tick moved the epic to %s", it.Status)
	}
}
