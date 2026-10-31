package pipeline_test

import (
	"maps"
	"strconv"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/pipeline"
	"github.com/iantanwx/loop/internal/secrets"
)

// submitted runs prd submit once and returns the epic and its PR number.
func (w *world) submitted() (string, int) {
	w.t.Helper()
	w.commitPRD(prdText)
	data := ok(w.t, w.submit("k-submit"))
	pr := int(data["pr"].(map[string]any)["number"].(float64))
	return data["epic"].(string), pr
}

// prd submit from an intake room pushes the PRD, opens one PR and creates
// one epic in prd_review. A replay, and a retry after a crash between the
// bead and the PR, change nothing more.
func TestPRDSubmitCreatesOneEpicInReview(t *testing.T) {
	w := newWorld(t)
	head := w.commitPRD(prdText)
	w.p.SetCrash(func(point string) bool { return point == "epic_created" })
	wantCode(t, w.submit("k-1"), cli.CodeInternal)
	w.p.SetCrash(nil)

	data := ok(t, w.submit("k-1"))
	epic := data["epic"].(string)
	it := w.show(epic)
	n := int(data["pr"].(map[string]any)["number"].(float64))
	if it.Kind != machine.KindEpic || it.Status != machine.PRDReview || it.Fact(machine.FactDocPR) != strconv.Itoa(n) ||
		it.Fact(machine.FactPRDPath) != prdPath {
		t.Fatalf("epic after submit: %+v", it)
	}
	if got := w.git.RemoteSHA(github.BranchFor(epic)); got != head {
		t.Fatalf("the PRD branch is at %q, want %s", got, head)
	}
	if w.pr(n).Head != github.BranchFor(epic) || w.pr(n).Title != "Widgets" {
		t.Fatalf("PR = %+v", w.pr(n))
	}
	if stories, _ := data["stories"].([]any); len(stories) != 2 {
		t.Fatalf("stories = %v", data["stories"])
	}

	again := ok(t, w.submit("k-2"))
	if again["epic"] != epic {
		t.Fatalf("a second submit made epic %v", again["epic"])
	}
	if got := len(w.items(machine.KindEpic)); got != 1 {
		t.Fatalf("%d epics", got)
	}
	if got := len(w.api.World.PRs()); got != 1 {
		t.Fatalf("%d PRs", got)
	}
}

// Only an intake room submits a PRD, a PRD needs story labels (R45), and
// a refused submit opens no PR.
func TestPRDSubmitRefusals(t *testing.T) {
	w := newWorld(t)
	head := w.commitPRD("# Widgets\n\nNo stories here.\n")
	flags := func() map[string]string {
		return headFlags(head, map[string]string{"path": prdPath, "title": "Widgets"})
	}
	wantCode(t, w.call(w.run("run-x", "", machine.StageImplementer), "", "prd submit", flags()), cli.CodeCapabilityDenied)
	wantCode(t, w.call(w.intakeRoom(), "", "prd submit", flags()), cli.CodeUsage)
	other := headFlags(head, map[string]string{"path": "docs/prd/missing.md", "title": "Widgets"})
	wantCode(t, w.call(w.intakeRoom(), "", "prd submit", other), cli.CodeUsage)
	if n := len(w.api.World.PRs()); n != 0 {
		t.Fatalf("a refused submit opened %d PRs", n)
	}
}

// A listed sign-off links to its PR from the item the inbox listed. The
// inbox reads no item a second time for its link.
func TestSignoffLinkReusesTheListedItem(t *testing.T) {
	w := newWorld(t)
	epic, n := w.submitted()
	w.review(epic, "pass")
	in := inbox.New(inbox.Config{Store: w.st, Machine: w.m, Projects: w.projects.List, ItemLink: pipeline.ItemLink(w.projects),
		Link: func(project, item string) string {
			t.Errorf("the inbox read %s/%s again for its link", project, item)
			return ""
		}})
	es, err := in.Entries(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e := entry(es, "signoff/"+testProject+"/"+epic); e == nil || e.Link != "https://github.com/"+gt.Repo+"/pull/"+strconv.Itoa(n) {
		t.Fatalf("sign-off entry = %+v", e)
	}
}

// A review pass waits for the operator, whose merge of the PRD PR is the
// sign-off. The sign-off entry links to the PR.
func TestOperatorMergeIsTheSignoff(t *testing.T) {
	w := newWorld(t)
	epic, n := w.submitted()
	w.review(epic, "pass")
	if got := w.show(epic).Status; got != machine.PRDSignoff {
		t.Fatalf("after the pass: %s", got)
	}
	e := entry(w.inbox(), "signoff/"+testProject+"/"+epic)
	if e == nil || e.Link != "https://github.com/"+gt.Repo+"/pull/"+strconv.Itoa(n) {
		t.Fatalf("sign-off entry = %+v", e)
	}
	w.tick()
	if got := w.show(epic).Status; got != machine.PRDSignoff {
		t.Fatalf("an open PR moved the epic to %s", got)
	}
	w.merge(n)
	w.tick()
	if it := w.show(epic); it.Status != machine.SpecNeeded || it.Fact(machine.FactPRDMerged) == "" {
		t.Fatalf("after the operator's merge: %+v", it)
	}
}

// AE7: the first reject starts one amender run, and the amender's PR
// update returns the epic to review. The second reject waits in the
// operator's inbox for an intake room, and no amender run starts.
func TestFirstRejectAmendsSecondWaitsForAnIntakeRoom(t *testing.T) {
	w := newWorld(t)
	epic, _ := w.submitted()
	w.review(epic, "reject")
	it := w.show(epic)
	if it.Status != machine.PRDAmending || it.Fact(machine.FactPRDRejects) != "1" {
		t.Fatalf("after the first reject: %+v", it)
	}
	c, ref := w.adm.Peek(w.ctx, testProject, machine.StageAmender)
	if ref != nil || c == nil || c.Item != epic {
		t.Fatalf("the amender's work query = %+v %+v", c, ref)
	}
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageAmender, RunID: "run-amd"}); res.Refusal != nil {
		t.Fatalf("amender claim: %+v", *res.Refusal)
	}
	gt.Commit(t, w.checkout, "amend", map[string]string{prdPath: prdText + "\nRevised.\n"})
	amd := w.run("run-amd", epic, machine.StageAmender)
	head := gt.Git(t, w.checkout, "rev-parse", "HEAD")
	ok(t, w.call(amd, "", "branch push", headFlags(head, map[string]string{})))
	ok(t, w.call(amd, "", "pr open", map[string]string{"title": "Widgets"}))
	if it := w.show(epic); it.Status != machine.PRDReview || it.Owner != "" {
		t.Fatalf("after the amender: %+v", it)
	}
	if n := len(w.api.World.PRs()); n != 1 {
		t.Fatalf("the amender opened a second PR: %d", n)
	}

	w.review(epic, "reject")
	if it := w.show(epic); it.Status != machine.Escalated || it.Fact(machine.FactPRDRejects) != "2" {
		t.Fatalf("after the second reject: %+v", it)
	}
	if c, ref := w.adm.Peek(w.ctx, testProject, machine.StageAmender); c != nil || ref != nil {
		t.Fatalf("an amender run would start again: %+v %+v", c, ref)
	}
	e := entry(w.inbox(), "escalation/"+testProject+"/"+epic)
	if e == nil || e.Awaits != inbox.AwaitsPerson || !strings.Contains(e.Text, "intake room") {
		t.Fatalf("the inbox entry = %+v", e)
	}
}

// The spec follows the same review and sign-off: a reject returns it to
// its author, a pass waits for the operator, and the operator's merge of
// the spec PR approves it. Only the current document PR counts.
func TestSpecFollowsTheSameReviewAndSignoff(t *testing.T) {
	w := newWorld(t)
	epic, prd := w.submitted()
	w.review(epic, "pass")
	w.merge(prd)
	w.tick()
	if got := w.show(epic).Status; got != machine.SpecNeeded {
		t.Fatalf("epic is %s", got)
	}

	author := func(run string) {
		t.Helper()
		if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StagePlanner, RunID: run}); res.Refusal != nil {
			t.Fatalf("planner claim: %+v", *res.Refusal)
		}
		gt.Commit(t, w.checkout, "spec "+run, map[string]string{"docs/specs/widgets.md": "spec by " + run + "\n"})
		pr := w.run(run, epic, machine.StagePlanner)
		head := gt.Git(t, w.checkout, "rev-parse", "HEAD")
		ok(t, w.call(pr, "", "branch push", headFlags(head, map[string]string{})))
		ok(t, w.call(pr, "", "pr open", map[string]string{"title": "Widgets spec"}))
	}
	author("run-spec-1")
	it := w.show(epic)
	spec, _ := strconv.Atoi(it.Fact(machine.FactDocPR))
	if it.Status != machine.SpecReview || spec == prd || spec == 0 {
		t.Fatalf("after the spec's PR: %+v", it)
	}
	w.review(epic, "reject")
	if got := w.show(epic).Status; got != machine.SpecNeeded {
		t.Fatalf("a spec reject went to %s", got)
	}
	author("run-spec-2")
	if got := w.show(epic).Fact(machine.FactDocPR); got != strconv.Itoa(spec) {
		t.Fatalf("the second draft moved the document PR to %s", got)
	}
	w.review(epic, "pass")
	w.tick()
	if got := w.show(epic).Status; got != machine.SpecSignoff {
		t.Fatalf("the merged PRD PR moved the spec to %s", got)
	}
	if e := entry(w.inbox(), "signoff/"+testProject+"/"+epic); e == nil || !strings.HasSuffix(e.Link, "/pull/"+strconv.Itoa(spec)) {
		t.Fatalf("spec sign-off entry = %+v", e)
	}
	w.merge(spec)
	w.tick()
	if got := w.show(epic).Status; got != machine.SpecApproved {
		t.Fatalf("after the spec merge: %s", got)
	}
}

// decomposing walks a submitted epic to decomposing, held by run-dec, with
// its PRD and a spec with no open question on main. Its spec merged before
// spec_path existed, so the epic holds no spec_path fact.
func (w *world) decomposing() string {
	w.t.Helper()
	return w.decomposingWith(false)
}

// decomposingWith is decomposing. With recorded, the spec's merge records
// its path in spec_path.
func (w *world) decomposingWith(recorded bool) string {
	w.t.Helper()
	epic, prd := w.submitted()
	w.review(epic, "pass")
	w.merge(prd)
	w.tick()
	for _, step := range []struct{ stage, run string }{{machine.StagePlanner, "run-spec"}} {
		if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: step.stage, RunID: step.run}); res.Refusal != nil {
			w.t.Fatalf("claim: %+v", *res.Refusal)
		}
	}
	w.mustApply(machine.Request{Item: epic, Verb: "pr_open", Params: map[string]string{"pr": "99"}, Actor: machine.Server("test")})
	w.mustApply(machine.Request{Item: epic, Verb: "review_pass", Command: "review submit", Params: map[string]string{"head": "h1"}, Actor: machine.Server("test")})
	w.onBase(map[string]string{specPath(epic, "widgets"): specWith("None.\n")})
	merged := map[string]string{}
	if recorded {
		merged["spec_path"] = specPath(epic, "widgets")
	}
	w.mustApply(machine.Request{Item: epic, Verb: "doc_merged", Params: merged, Actor: machine.Server("test")})
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageDecomposer, RunID: "run-dec"}); res.Refusal != nil {
		w.t.Fatalf("decomposer claim: %+v", *res.Refusal)
	}
	return epic
}

func specPath(epic, name string) string { return "docs/plans/" + epic + "-" + name + "-spec.md" }

// specWith is a spec whose Open questions section holds questions. Its
// first entry starts on line 9.
func specWith(questions string) string {
	return "# Widgets spec\n\n## Contracts\n\n- C1. A trader sees widgets.\n\n## Open questions\n\n" + questions
}

// onBase commits files to main, and brings the mirror up to date. An empty
// text removes its file.
func (w *world) onBase(files map[string]string) {
	w.t.Helper()
	gt.Commit(w.t, w.checkout, "base", files)
	gt.Git(w.t, w.checkout, "push", "-q", w.git.RemoteDir(), "HEAD:main")
	if _, _, err := w.svc.SyncBase(w.ctx, w.projects[0]); err != nil {
		w.t.Fatal(err)
	}
}

type ticket struct {
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Stories   []string `json:"stories"`
	BlockedBy []string `json:"blocked_by,omitempty"`
}

// shapedBody holds every section of the Ticket shape.
const shapedBody = "## Source\n\nWork entry 1.\n\n## Change\n\nA trader sees widgets.\n\n## Contracts to prove\n\n" +
	"- C1. A trader sees widgets.\n\n## Files\n\n- May change: `widgets.go`.\n\n## Done means\n\nThe gates pass.\n\n" +
	"## Out of scope\n\n- Sorting: ticket sort.\n"

// batch is the decomposition of ts. A ticket with no body gets shapedBody.
func batch(ts ...ticket) map[string]any {
	for i := range ts {
		if ts[i].Body == "" {
			ts[i].Body = shapedBody
		}
	}
	return map[string]any{"tickets": ts}
}

var threeTickets = batch(
	ticket{Key: "list", Title: "List widgets", Stories: []string{"US-01"}},
	ticket{Key: "sort", Title: "Sort widgets", Stories: []string{"US-02"}, BlockedBy: []string{"list"}},
	ticket{Key: "both", Title: "Polish widgets", Stories: []string{"US-01", "US-02"}, BlockedBy: []string{"sort"}},
)

// KTD24: a decompose batch replayed after a crash creates no duplicate
// ticket, and the epic moves to execution once.
func TestDecomposeReplayAfterACrashCreatesNoDuplicate(t *testing.T) {
	w := newWorld(t)
	epic := w.decomposing()
	dec := w.run("run-dec", epic, machine.StageDecomposer)
	created := 0
	w.p.SetCrash(func(point string) bool {
		if point == "ticket_created" {
			created++
			return created == 2
		}
		return false
	})
	wantCode(t, w.call(dec, "k-dec", "epic decompose", fileFlag(threeTickets)), cli.CodeInternal)
	w.p.SetCrash(nil)
	if got := len(w.items(machine.KindTicket)); got != 2 {
		t.Fatalf("the crash left %d tickets", got)
	}
	if got := w.show(epic).Status; got != machine.Decomposing {
		t.Fatalf("the crashed batch moved the epic to %s", got)
	}

	data := ok(t, w.call(dec, "k-dec", "epic decompose", fileFlag(threeTickets)))
	tickets := w.items(machine.KindTicket)
	if len(tickets) != 3 {
		t.Fatalf("%d tickets after the replay", len(tickets))
	}
	if got := w.show(epic); got.Status != machine.Execution || got.Owner != "" {
		t.Fatalf("epic after the batch: %+v", got)
	}
	byKey := map[string]machine.Item{}
	for _, tk := range tickets {
		key := strings.TrimPrefix(tk.Fact(machine.FactCreateKey), epic+"/")
		byKey[key] = tk
		if tk.Fact(machine.FactEpic) != epic {
			t.Fatalf("%s names epic %q", tk.ID, tk.Fact(machine.FactEpic))
		}
	}
	if byKey["list"].Status != machine.Todo || byKey["sort"].Status != machine.Backlog || byKey["both"].Status != machine.Backlog {
		t.Fatalf("ticket states: %+v", byKey)
	}
	if byKey["both"].Fact(machine.FactStories) != "US-01,US-02" {
		t.Fatalf("stories = %q", byKey["both"].Fact(machine.FactStories))
	}
	if got := data["tickets"].([]any); len(got) != 3 {
		t.Fatalf("answer = %v", data)
	}

	// A replay after the batch landed answers the same tickets and writes
	// nothing.
	again := ok(t, w.call(dec, "k-dec-2", "epic decompose", fileFlag(threeTickets)))
	if len(w.items(machine.KindTicket)) != 3 || len(again["tickets"].([]any)) != 3 {
		t.Fatalf("the late replay wrote: %v", again)
	}
}

// KTD24: a ticket that cites no story label of the merged PRD is refused,
// and so is the whole batch.
func TestDecomposeRefusesTheWholeBatch(t *testing.T) {
	w := newWorld(t)
	epic := w.decomposing()
	dec := w.run("run-dec", epic, machine.StageDecomposer)
	for name, b := range map[string]map[string]any{
		"unknown story": batch(ticket{Key: "a", Title: "A", Stories: []string{"US-01"}}, ticket{Key: "b", Title: "B", Stories: []string{"US-09"}}),
		"no story":      batch(ticket{Key: "a", Title: "A", Stories: []string{"US-01"}}, ticket{Key: "b", Title: "B"}),
	} {
		res := w.call(dec, "", "epic decompose", fileFlag(b))
		if res.Refusal == nil || res.Refusal.Code != cli.CodeStoryUnknown {
			t.Fatalf("%s: %d %+v", name, res.Exit, res.Refusal)
		}
	}
	cycle := batch(ticket{Key: "a", Title: "A", Stories: []string{"US-01"}, BlockedBy: []string{"b"}},
		ticket{Key: "b", Title: "B", Stories: []string{"US-01"}, BlockedBy: []string{"a"}})
	wantCode(t, w.call(dec, "", "epic decompose", fileFlag(cycle)), cli.CodeUsage)
	if n := len(w.items(machine.KindTicket)); n != 0 {
		t.Fatalf("a refused batch created %d tickets", n)
	}
	if got := w.show(epic).Status; got != machine.Decomposing {
		t.Fatalf("a refused batch moved the epic to %s", got)
	}
	wantCode(t, w.call(w.run("run-x", epic, machine.StageImplementer), "", "epic decompose", fileFlag(threeTickets)), cli.CodeCapabilityDenied)
}

// R48: a PRD-less ticket from a run is PRD_REQUIRED. The coordinator
// creates one only when it cites the operator's request. A ticket under
// an epic needs the epic's merged PRD and its story labels.
func TestWorkCreateIsPRDFirst(t *testing.T) {
	w := newWorld(t)
	run := w.run("run-x", "", machine.StageImplementer)
	wantCode(t, w.call(run, "", "work create", map[string]string{"title": "Fix the typo"}), cli.CodePRDRequired)
	coord := map[string]string{"title": "Fix the typo", "project": testProject}
	wantCode(t, w.call(w.coordinator(), "", "work create", coord), cli.CodePRDRequired)
	if n := len(w.items(machine.KindTicket)); n != 0 {
		t.Fatalf("a refused create made %d tickets", n)
	}
	coord["request"] = "Ian: please just fix the typo on the start page"
	data := ok(t, w.call(w.coordinator(), "k-create", "work create", coord))
	it := w.show(data["item"].(string))
	if it.Kind != machine.KindTicket || it.Status != machine.Todo || it.Fact(machine.FactRequest) != coord["request"] {
		t.Fatalf("PRD-less ticket: %+v", it)
	}
	// A retry of the same call makes no second ticket.
	again := ok(t, w.call(w.coordinator(), "k-create", "work create", coord))
	if again["item"] != data["item"] || len(w.items(machine.KindTicket)) != 1 {
		t.Fatalf("the retry made %v", again)
	}

	epic, _ := w.submitted()
	underEpic := map[string]string{"title": "List widgets", "epic": epic, "story": "US-01", "body": ticketBody}
	wantCode(t, w.call(run, "", "work create", underEpic), cli.CodePRDRequired)
	w.review(epic, "pass")
	w.merge(w.prOf(epic))
	w.tick()
	underEpic["story"] = "US-07"
	wantCode(t, w.call(run, "", "work create", underEpic), cli.CodeStoryUnknown)
	underEpic["story"] = "US-01"
	data = ok(t, w.call(run, "", "work create", underEpic))
	if it := w.show(data["item"].(string)); it.Fact(machine.FactEpic) != epic || it.Fact(machine.FactStories) != "US-01" {
		t.Fatalf("ticket under the epic: %+v", it)
	}
}

func (w *world) prOf(epic string) int {
	w.t.Helper()
	n, err := strconv.Atoi(w.show(epic).Fact(machine.FactDocPR))
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// R46: a planner run on an epic with no merged PRD or ADR is refused.
func TestPlannerNeedsTheSignedOffPRD(t *testing.T) {
	w := newWorld(t)
	epic, _ := w.submitted()
	w.review(epic, "pass")
	// The epic is pushed to spec_needed by a writer outside the loop, with
	// no merge behind it.
	if _, err := w.bx.Update(w.ctx, epic, beadsxUpdate(machine.PRDSignoff, machine.SpecNeeded)); err != nil {
		t.Fatal(err)
	}
	_, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StagePlanner, RunID: "run-spec"})
	wantCode(t, res, cli.CodePRDRequired)
}

// R48 and U5: the coordinator files a bug from the operator's report, in
// the bug's first state, so the reproducer stage admits it. A run files
// no bug, a bug needs the operator's request, and a report that holds a
// stored secret is refused before any write.
func TestCoordinatorFilesABugForTheReproducer(t *testing.T) {
	w := newWorld(t)
	w.red.SetStored([]secrets.NamedValue{{Name: testProject + "/API_KEY", Value: "s3cr3t-value-for-bug"}})
	bug := map[string]string{"kind": "bug", "title": "The list page shows no widgets", "project": testProject,
		"body": "Open /widgets: the list is empty, and the API returns three widgets."}
	run := w.run("run-x", "", machine.StageImplementer)
	wantCode(t, w.call(run, "", "work create", bug), cli.CodeCapabilityDenied)
	wantCode(t, w.call(w.coordinator(), "", "work create", bug), cli.CodePRDRequired)
	bug["request"] = "Ian: the widgets list is empty again, please look"
	bug["epic"] = "tk-1"
	wantCode(t, w.call(w.coordinator(), "", "work create", bug), cli.CodeUsage)
	delete(bug, "epic")
	leaky := maps.Clone(bug)
	leaky["body"] += " The key is s3cr3t-value-for-bug."
	wantCode(t, w.call(w.coordinator(), "", "work create", leaky), cli.CodeSecretInOutput)
	if n := len(w.items(machine.KindBug)); n != 0 {
		t.Fatalf("a refused filing made %d bugs", n)
	}

	data := ok(t, w.call(w.coordinator(), "k-bug", "work create", bug))
	it := w.show(data["item"].(string))
	if it.Kind != machine.KindBug || it.Status != machine.Reported || it.Fact(machine.FactRequest) != bug["request"] ||
		data["status"] != machine.Reported {
		t.Fatalf("filed bug: %+v, answer %v", it, data)
	}
	again := ok(t, w.call(w.coordinator(), "k-bug", "work create", bug))
	if again["item"] != data["item"] || len(w.items(machine.KindBug)) != 1 {
		t.Fatalf("the retry made %v", again)
	}
	c, ref := w.adm.Peek(w.ctx, testProject, machine.StageReproducer)
	if ref != nil || c == nil || c.Item != it.ID {
		t.Fatalf("the reproducer's work query = %+v %+v, want %s", c, ref, it.ID)
	}
}

// The coordinator creates an architecture epic. It waits in architecture
// until an ADR whose Ticket line names it lands on the base. A run
// creates none, an epic with no --architecture needs the operator's
// request, and a stored secret is refused.
func TestCoordinatorCreatesAnArchitectureEpic(t *testing.T) {
	w := newWorld(t)
	w.red.SetStored([]secrets.NamedValue{{Name: testProject + "/API_KEY", Value: "s3cr3t-value-for-epic"}})
	flags := map[string]string{"title": "Move the store to Postgres", "project": testProject,
		"body": "The operator wants the store on Postgres; an ADR records the decision."}
	wantCode(t, w.call(w.run("run-x", "", machine.StageImplementer), "", "epic create", flags), cli.CodeCapabilityDenied)
	wantCode(t, w.call(w.coordinator(), "", "epic create", flags), cli.CodePRDRequired)
	flags["architecture"] = "true"
	leaky := maps.Clone(flags)
	leaky["body"] += " s3cr3t-value-for-epic"
	wantCode(t, w.call(w.coordinator(), "", "epic create", leaky), cli.CodeSecretInOutput)
	if n := len(w.items(machine.KindEpic)); n != 0 {
		t.Fatalf("a refused create made %d epics", n)
	}
	data := ok(t, w.call(w.coordinator(), "k-arch", "epic create", flags))
	it := w.show(data["epic"].(string))
	if it.Kind != machine.KindEpic || it.Status != machine.Architecture || data["status"] != machine.Architecture {
		t.Fatalf("architecture epic: %+v, answer %v", it, data)
	}
	again := ok(t, w.call(w.coordinator(), "k-arch", "epic create", flags))
	if again["epic"] != data["epic"] || len(w.items(machine.KindEpic)) != 1 {
		t.Fatalf("the retry made %v", again)
	}
}
