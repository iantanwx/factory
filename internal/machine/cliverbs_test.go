package machine

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

const liveSecret = "sk-live-0123456789abcdef"

// call runs one CLI verb's handler as principal p.
func (w *world) call(path string, p rpc.Principal, args []string, flags map[string]string) cli.Result {
	w.t.Helper()
	v, ok := cli.Surface().Verb(path)
	if !ok {
		w.t.Fatalf("no verb %q", path)
	}
	h, ok := w.m.Handlers()[path]
	if !ok {
		w.t.Fatalf("the machine has no handler for %q", path)
	}
	if flags == nil {
		flags = map[string]string{}
	}
	inv := cli.Invocation{Verb: v, Args: args, Flags: flags, Extra: map[string]string{}, RequestID: "req-" + path}
	return h(context.Background(), &rpc.Call{Inv: inv, Principal: p})
}

func coord() rpc.Principal {
	return rpc.Principal{Kind: cli.PrincipalCoordinator, RunID: "coord", Project: prj}
}

// runOn is the principal of a run that holds item at its current epoch.
func (w *world) runOn(item, stage, run string) rpc.Principal {
	w.t.Helper()
	return rpc.Principal{Kind: cli.PrincipalRun, RunID: run, Project: prj, Item: item, Stage: stage, Epoch: w.show(item).Epoch}
}

// claimAs grants item to run for stage and returns the run's principal.
func (w *world) claimAs(item, stage, run string) rpc.Principal {
	w.t.Helper()
	w.claim(item, stage, run)
	return w.runOn(item, stage, run)
}

// inReview makes a ticket in in_review and claims it for a code reviewer.
func (w *world) inReview() (string, rpc.Principal) {
	w.t.Helper()
	id := w.create(KindTicket, "create", nil)
	w.claimAs(id, StageImplementer, "impl")
	w.must(id, "pr_open", map[string]string{"head": shaA})
	return id, w.claimAs(id, StageCodeReviewer, "rev")
}

const shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type commentLog struct {
	calls []string
	fail  *cli.Refusal
}

func (c *commentLog) post(_ context.Context, project, item, body, key string) *cli.Refusal {
	if c.fail != nil {
		return c.fail
	}
	c.calls = append(c.calls, project+"|"+item+"|"+body+"|"+key)
	return nil
}

// review submit --body posts the comment on the item's pull request, then
// records the verdict with the body as the review fact. A body that holds
// a secret, or a comment GitHub refused, records nothing. A verdict with
// no body clears the fact of an earlier round.
func TestReviewSubmitCarriesItsBody(t *testing.T) {
	w := newWorld(t)
	w.red.SetLive("alpha/API_KEY", liveSecret)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)

	id, rev := w.inReview()
	res := w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "body": "the key is " + liveSecret})
	wantCode(t, res, cli.CodeSecretInOutput)
	log.fail = &cli.Refusal{Code: cli.CodeGitHubUnavailable, Rule: "github_unavailable", Hint: "try again"}
	wantCode(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "body": "Fix the test."}), cli.CodeGitHubUnavailable)
	if it := w.show(id); it.Status != InReview || it.Fact(FactReview) != "" || len(log.calls) != 0 {
		t.Fatalf("a refused review wrote: %s %v %v", it.Status, it.Facts, log.calls)
	}

	log.fail = nil
	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "body": "Fix the test."}))
	it := w.show(id)
	if it.Status != Todo || it.Fact(FactReview) != "Fix the test." {
		t.Fatalf("after the reject: %s %v", it.Status, it.Facts)
	}
	if len(log.calls) != 1 || !strings.HasPrefix(log.calls[0], prj+"|"+id+"|Fix the test.|") {
		t.Fatalf("comments posted: %v", log.calls)
	}

	w.claimAs(id, StageImplementer, "impl-2")
	w.must(id, "pr_open", map[string]string{"head": shaA})
	rev = w.claimAs(id, StageCodeReviewer, "rev-2")
	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "pass", "head": shaA}))
	if it := w.show(id); it.Status != ReadyToMerge || it.Fact(FactReview) != "" {
		t.Fatalf("a pass with no body kept the earlier review: %s %v", it.Status, it.Facts)
	}
	if len(log.calls) != 1 {
		t.Fatalf("a pass with no body posted a comment: %v", log.calls)
	}
}

// A code pass reads CI at its head before it posts its comment: a pass the
// gate refuses posts nothing, records nothing, and names the ticket's
// state and its next moves. A reject and a document's pass read no CI.
func TestReviewPassReadsCIBeforeItsComment(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)
	var gated []string
	w.m.SetPassGate(func(_ context.Context, project, item, head string) *cli.Refusal {
		gated = append(gated, project+"|"+item+"|"+head)
		return &cli.Refusal{Code: cli.CodeCINotGreen, Rule: "ci_failed", Hint: "ci failed"}
	})

	id, rev := w.inReview()
	res := w.call("review submit", rev, []string{id}, map[string]string{"verdict": "pass", "head": shaA, "body": "Looks right."})
	wantCode(t, res, cli.CodeCINotGreen)
	if r := res.Refusal; r.Item != id || r.CurrentStatus != InReview || len(r.AllowedNext) == 0 {
		t.Fatalf("the refusal names no ticket state: %+v", *r)
	}
	if it := w.show(id); it.Status != InReview || it.Fact(FactReviewedHead) != "" || len(log.calls) != 0 {
		t.Fatalf("a pass at red CI wrote: %s %v %v", it.Status, it.Facts, log.calls)
	}
	if len(gated) != 1 || gated[0] != prj+"|"+id+"|"+shaA {
		t.Fatalf("the gate read %v", gated)
	}
	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "head": shaA, "body": "Fix the test."}))
	if len(gated) != 1 || len(log.calls) != 1 {
		t.Fatalf("a reject read CI %v or posted %v", gated, log.calls)
	}

	ep := w.create(KindEpic, "prd_submit", nil)
	w.claim(ep, StageDocReviewer, "doc-rev")
	mustOK(t, w.call("review submit", w.runOn(ep, StageDocReviewer, "doc-rev"), []string{ep}, map[string]string{"verdict": "pass", "head": shaA}))
	if got := w.show(ep).Status; got != PRDSignoff || len(gated) != 1 {
		t.Fatalf("a document pass: %s, gate read %v", got, gated)
	}
}

// review submit --head compares the head with the pull request's live head
// at the write, as candidateFeedback in scripts/sdlc/github-health.ts:46-70
// (alpha) did. A verdict at a head the PR no longer holds records nothing,
// posts no comment, reads no CI, and names the ticket's state and its next
// moves.
func TestReviewSubmitRefusesAHeadThePRNoLongerHolds(t *testing.T) {
	const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)
	live := shaB
	var reads, gated []string
	w.m.SetPRHeadReader(func(_ context.Context, project, item string) (string, *cli.Refusal) {
		reads = append(reads, project+"|"+item)
		return live, nil
	})
	w.m.SetPassGate(func(_ context.Context, project, item, head string) *cli.Refusal {
		gated = append(gated, head)
		return nil
	})

	id, rev := w.inReview()
	res := w.call("review submit", rev, []string{id}, map[string]string{"verdict": "pass", "head": shaA, "body": "Looks right."})
	wantCode(t, res, cli.CodeStatusChanged)
	if r := res.Refusal; r.Rule != "head_changed" || !strings.Contains(r.Hint, shaB[:7]) ||
		r.Item != id || r.CurrentStatus != InReview || len(r.AllowedNext) == 0 {
		t.Fatalf("refusal: %+v", *r)
	}
	if it := w.show(id); it.Status != InReview || it.Fact(FactReviewedHead) != "" || len(log.calls) != 0 {
		t.Fatalf("a verdict at a moved head wrote: %s %v %v", it.Status, it.Facts, log.calls)
	}
	if len(reads) != 1 || reads[0] != prj+"|"+id || len(gated) != 0 {
		t.Fatalf("head reads %v, CI reads %v", reads, gated)
	}

	live = shaA
	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "pass", "head": shaA, "body": "Looks right."}))
	if it := w.show(id); it.Status != ReadyToMerge || it.Fact(FactReviewedHead) != shaA || len(log.calls) != 1 {
		t.Fatalf("a verdict at the live head: %s %v %v", it.Status, it.Facts, log.calls)
	}
	if len(gated) != 1 || gated[0] != shaA {
		t.Fatalf("CI reads %v", gated)
	}
}

// work edit replaces the title or the body. The item shows the new title,
// and TaskText, which admission reads for the retry identity (R10), holds
// both.
func TestWorkEditReplacesTheTask(t *testing.T) {
	w := newWorld(t)
	w.red.SetLive("alpha/API_KEY", liveSecret)
	id := w.create(KindTicket, "create", nil)
	wantCode(t, w.call("work edit", coord(), []string{id}, nil), cli.CodeUsage)
	wantCode(t, w.call("work edit", coord(), []string{id}, map[string]string{"body": liveSecret}), cli.CodeSecretInOutput)

	mustOK(t, w.call("work edit", coord(), []string{id}, map[string]string{"title": "New title", "body": "New body."}))
	if it := w.show(id); it.Title != "New title" || it.Status != Todo {
		t.Fatalf("after the edit: %q %s", it.Title, it.Status)
	}
	is, err := w.bc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if title, body := TaskText(is); title != "New title" || body != "New body." {
		t.Fatalf("TaskText = %q, %q", title, body)
	}
	// One field at a time keeps the other.
	mustOK(t, w.call("work edit", coord(), []string{id}, map[string]string{"body": "Second body."}))
	is, _ = w.bc.Get(context.Background(), id)
	if title, body := TaskText(is); title != "New title" || body != "Second body." {
		t.Fatalf("TaskText after a body edit = %q, %q", title, body)
	}
	// An amendment a person gave still enters the task.
	w.force(id, Todo, map[string]string{FactAmendment: "Also do Y."})
	is, _ = w.bc.Get(context.Background(), id)
	if _, body := TaskText(is); body != "Second body.\n\nAlso do Y." {
		t.Fatalf("TaskText with an amendment = %q", body)
	}

	// A terminal item is never edited.
	w.must(id, "merged", map[string]string{"sha": shaA})
	wantCode(t, w.call("work edit", coord(), []string{id}, map[string]string{"title": "Late"}), cli.CodeTransitionNotAllowed)
}

// TaskText of a bead no edit touched is its own title and description.
func TestTaskTextOfAnUneditedBead(t *testing.T) {
	is := beadsx.Issue{Title: "T", Description: "D", Metadata: map[string]string{}}
	if title, body := TaskText(is); title != "T" || body != "D" {
		t.Fatalf("TaskText = %q, %q", title, body)
	}
}

// The reproducer shapes a bug into a ticket: the server creates the
// ticket, which names the bug, and closes the bug as shaped with the
// ticket's id. A retry after the ticket exists reuses it.
func TestWorkShapeCreatesTheTicket(t *testing.T) {
	w := newWorld(t)
	bug := w.create(KindBug, "file", nil)
	run := w.claimAs(bug, StageReproducer, "repro")
	wantCode(t, w.call("work shape", run, []string{bug}, map[string]string{"title": " ", "body": "b"}), cli.CodeUsage)

	res := w.call("work shape", run, []string{bug}, map[string]string{"title": "Fix the crash", "body": "## Files\n"})
	mustOK(t, res)
	data, _ := res.Data.(ShapeResult)
	ticket := data.Ticket
	it := w.show(bug)
	if it.Status != Shaped || ticket == "" || it.Fact(FactTicket) != ticket || it.Owner != "" {
		t.Fatalf("after shape: %s %v %v", it.Status, it.Facts, data)
	}
	tk := w.show(ticket)
	if tk.Kind != KindTicket || tk.Status != Todo || tk.Title != "Fix the crash" || tk.Fact(FactBug) != bug {
		t.Fatalf("the shaped ticket: %+v", tk)
	}
	// A second shape of a closed bug is refused, and makes no ticket.
	wantCode(t, w.call("work shape", run, []string{bug}, map[string]string{"title": "Again", "body": "b"}), cli.CodeTransitionNotAllowed)
	if n := len(w.items(KindTicket)); n != 1 {
		t.Fatalf("%d tickets, want 1", n)
	}

	// A crash after the ticket was made: the retry finds it.
	bug2 := w.create(KindBug, "file", nil)
	run2 := w.claimAs(bug2, StageReproducer, "repro-2")
	made, ref := w.m.CreateBead(context.Background(), CreateRequest{Project: prj, Kind: KindTicket, Title: "Fix it",
		Facts: map[string]string{FactCreateKey: shapeKey(bug2), FactBug: bug2}})
	if ref != nil {
		t.Fatal(ref)
	}
	res = w.call("work shape", run2, []string{bug2}, map[string]string{"title": "Fix it", "body": "b"})
	mustOK(t, res)
	if got := w.show(bug2).Fact(FactTicket); got != made {
		t.Fatalf("the retry shaped %q, want the ticket %s it made before", got, made)
	}
	if it := w.show(made); it.Status != Todo {
		t.Fatalf("the reused ticket is in %s", it.Status)
	}
	// Only a bug is shaped.
	wantCode(t, w.call("work shape", coord(), []string{made}, map[string]string{"title": "x", "body": "b"}), cli.CodeUsage)
}

// The reproducer closes a bug as a duplicate of an existing item.
func TestWorkDuplicateClosesTheBug(t *testing.T) {
	w := newWorld(t)
	first := w.create(KindBug, "file", nil)
	bug := w.create(KindBug, "file", nil)
	run := w.claimAs(bug, StageReproducer, "repro")
	wantCode(t, w.call("work duplicate", run, []string{bug}, map[string]string{"of": bug}), cli.CodeUsage)
	wantCode(t, w.call("work duplicate", run, []string{bug}, map[string]string{"of": "tk-404"}), cli.CodeNotFound)
	mustOK(t, w.call("work duplicate", run, []string{bug}, map[string]string{"of": first}))
	if it := w.show(bug); it.Status != Duplicate || it.Fact(FactDuplicateOf) != first {
		t.Fatalf("after duplicate: %s %v", it.Status, it.Facts)
	}
}

type evidenceLog struct {
	calls []string
	fail  *cli.Refusal
}

func (e *evidenceLog) check(_ context.Context, project, item, kind, ref string) *cli.Refusal {
	e.calls = append(e.calls, project+"|"+item+"|"+kind+"|"+ref)
	return e.fail
}

// work supersede closes a ticket with no work left on checked evidence,
// and the ticket keeps the evidence and the reason. The run that holds the
// ticket, the fixer and the coordinator may close it. Another run's lease,
// a state that holds a pull request, a refused GitHub read or evidence
// that does not hold each refuse, and record nothing.
func TestWorkSupersedeClosesOnCheckedEvidence(t *testing.T) {
	w := newWorld(t)
	sup := func(p rpc.Principal, id string, flags map[string]string) cli.Result {
		return w.call("work supersede", p, []string{id}, flags)
	}
	commit := map[string]string{"commit": "abc1234", "reason": "Commit abc1234 on main already adds the flag."}

	// With no GitHub the server cannot check the pull request.
	id := w.create(KindTicket, "create", nil)
	wantCode(t, sup(coord(), id, commit), cli.CodeInternal)
	ev := &evidenceLog{}
	w.m.SetSupersedeChecker(ev.check)

	for _, flags := range []map[string]string{
		{"reason": "no evidence"},
		{"commit": "abc1234", "done": "tk-1", "reason": "two kinds"},
		{"commit": "abc1234"},
		{"commit": "main", "reason": "not a sha"},
		{"adr": "../adr.md", "reason": "outside the repository"},
		{"done": id, "reason": "itself"},
	} {
		wantCode(t, sup(coord(), id, flags), cli.CodeUsage)
	}
	other := w.create(KindTicket, "create", nil)
	wantCode(t, sup(coord(), id, map[string]string{"done": other, "reason": "not done"}), cli.CodeTransitionNotAllowed)
	ev.fail = &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "open_pr", Hint: "it holds #4"}
	wantCode(t, sup(coord(), id, commit), cli.CodeTransitionNotAllowed)
	if it := w.show(id); it.Status != Todo || it.Fact(FactSupersededBy) != "" {
		t.Fatalf("a refused supersede wrote: %s %v", it.Status, it.Facts)
	}

	ev.fail, ev.calls = nil, nil
	mustOK(t, sup(coord(), id, commit))
	it := w.show(id)
	if it.Status != Canceled || it.Fact(FactSupersededBy) != "commit abc1234" ||
		it.Fact(FactSupersedeReason) != commit["reason"] {
		t.Fatalf("after supersede: %s %v", it.Status, it.Facts)
	}
	if len(ev.calls) != 1 || ev.calls[0] != prj+"|"+id+"|commit|abc1234" {
		t.Fatalf("evidence reads: %v", ev.calls)
	}

	// A done item answers another.
	w.must(other, "merged", map[string]string{"sha": shaA})
	third := w.create(KindTicket, "create", nil)
	mustOK(t, sup(coord(), third, map[string]string{"done": other, "reason": "The merge of the other ticket did it."}))
	if got := w.show(third).Fact(FactSupersededBy); got != "done "+other {
		t.Fatalf("superseded_by = %q", got)
	}

	// The implementer that holds the ticket closes it; the coordinator
	// and another run may not while that lease stands.
	held := w.create(KindTicket, "create", nil)
	impl := w.claimAs(held, StageImplementer, "impl")
	wantCode(t, sup(coord(), held, commit), cli.CodeLeaseHeld)
	fixer := rpc.Principal{Kind: cli.PrincipalRun, RunID: "fix", Project: prj, Stage: "fixer"}
	wantCode(t, sup(fixer, held, commit), cli.CodeLeaseHeld)
	stranger := w.runOn(other, StageImplementer, "impl-x")
	wantCode(t, sup(stranger, held, commit), cli.CodeCapabilityDenied)
	mustOK(t, sup(impl, held, commit))
	if it := w.show(held); it.Status != Canceled || it.Owner != "" {
		t.Fatalf("the holder's supersede: %s owner %q", it.Status, it.Owner)
	}

	// The fixer closes a ticket nobody holds.
	free := w.create(KindTicket, "create", nil)
	mustOK(t, sup(fixer, free, map[string]string{"adr": "docs/adr/0007-drop-x.md", "reason": "ADR 0007 drops the feature."}))
	if got := w.show(free).Fact(FactSupersededBy); got != "adr docs/adr/0007-drop-x.md" {
		t.Fatalf("superseded_by = %q", got)
	}

	// A state that holds a pull request never closes so, and the server
	// reads no evidence for it.
	ev.calls = nil
	inReview, _ := w.inReview()
	wantCode(t, sup(coord(), inReview, commit), cli.CodeTransitionNotAllowed)
	if len(ev.calls) != 0 {
		t.Fatalf("a refused state read evidence: %v", ev.calls)
	}
}

// The reproducer cancels a report it routes as a loop fault, so the
// report never returns to its queue. A raw report that no run holds
// stays.
func TestWorkCancelEndsALoopFaultReport(t *testing.T) {
	w := newWorld(t)
	raw := w.create(KindBug, "file", nil)
	wantCode(t, w.call("work cancel", coord(), []string{raw}, nil), cli.CodeTransitionNotAllowed)
	bug := w.create(KindBug, "file", nil)
	run := w.claimAs(bug, StageReproducer, "repro")
	mustOK(t, w.call("work cancel", run, []string{bug}, nil))
	if it := w.show(bug); it.Status != Canceled || it.Owner != "" {
		t.Fatalf("after cancel: %s, owner %q", it.Status, it.Owner)
	}
}

// A run reaches an item it does not hold only through the fixer's
// allowance, which the capability check grants for the escalated item.
// Even then the machine lets it note the item, and nothing else but the
// supersede TestWorkSupersedeClosesOnCheckedEvidence covers.
func TestABorrowedItemTakesANoteOnly(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	fixer := rpc.Principal{Kind: cli.PrincipalRun, RunID: "fix", Project: prj, Stage: "fixer"}
	mustOK(t, w.call("work note", fixer, []string{id}, map[string]string{"body": "The panel routed it to the loop."}))
	if got := w.show(id).Fact(FactNote); got != "The panel routed it to the loop." {
		t.Fatalf("note = %q", got)
	}
	for path, flags := range map[string]map[string]string{
		"work edit":     {"title": "x"},
		"work cancel":   nil,
		"work unblock":  nil,
		"review submit": {"verdict": "reject"},
	} {
		if res := w.call(path, fixer, []string{id}, flags); res.Refusal == nil || res.Refusal.Code != cli.CodeCapabilityDenied {
			t.Errorf("%s on a borrowed item = %+v, want %s", path, res.Refusal, cli.CodeCapabilityDenied)
		}
	}
}

func (w *world) items(kind Kind) []Item {
	w.t.Helper()
	items, ref := w.m.List(context.Background(), prj, ListFilter{Kind: kind})
	if ref != nil {
		w.t.Fatal(ref)
	}
	return items
}

// review rounds lists each verdict of an item in order, with its head and
// its comment, so an author reads the rejection it answers.
func TestReviewRoundsListsEachVerdict(t *testing.T) {
	w := newWorld(t)
	id, rev := w.inReview()
	reader := w.runOn(id, StageCodeReviewer, "rev")
	if res := w.call("review rounds", reader, []string{id}, nil); res.Exit != cli.ExitNothing {
		t.Fatalf("rounds before any verdict = %+v", res)
	}
	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "head": shaA, "body": "Fix the test."}))
	impl := w.claimAs(id, StageImplementer, "impl-2")
	w.must(id, "pr_open", map[string]string{"head": shaA})
	rev = w.claimAs(id, StageCodeReviewer, "rev-2")
	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "pass", "head": shaA, "body": "Good."}))

	res := w.call("review rounds", impl, []string{id}, nil)
	mustOK(t, res)
	rounds, _ := res.Data.([]Round)
	if len(rounds) != 2 {
		t.Fatalf("rounds = %+v", res.Data)
	}
	if r := rounds[0]; r.Round != 1 || r.Verdict != "reject" || r.Body != "Fix the test." || r.RunID != "rev" {
		t.Fatalf("round 1 = %+v", r)
	}
	if r := rounds[1]; r.Round != 2 || r.Verdict != "pass" || r.Head != shaA || r.Body != "Good." || r.RunID != "rev-2" {
		t.Fatalf("round 2 = %+v", r)
	}
}

// C14: work list --epic lists the items whose epic fact names the id, and
// combines with --status. An id that no item names lists nothing.
func TestC14WorkListEpicListsTheEpicsChildren(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.create(KindEpic, "create_architecture", nil)
	var kids []string
	for range 2 {
		id, res := w.m.Create(ctx, CreateRequest{Project: prj, Kind: KindTicket, Title: "c", Verb: "create",
			Actor: Server("test"), Facts: map[string]string{FactEpic: ep}})
		mustOK(t, res)
		kids = append(kids, id)
	}
	w.create(KindTicket, "create", nil)
	w.force(kids[1], InProgress, nil)
	ids := func(flags map[string]string) []string {
		t.Helper()
		res := w.call("work list", coord(), nil, flags)
		if res.Refusal != nil {
			t.Fatalf("work list %v: %+v", flags, res.Refusal)
		}
		items, _ := res.Data.([]Item)
		var got []string
		for _, it := range items {
			got = append(got, it.ID)
		}
		return got
	}
	if got := ids(map[string]string{"epic": ep}); strings.Join(got, ",") != strings.Join(kids, ",") {
		t.Errorf("--epic %s = %v, want %v", ep, got, kids)
	}
	if got := ids(map[string]string{"epic": ep, "status": "todo"}); strings.Join(got, ",") != kids[0] {
		t.Errorf("--epic %s --status todo = %v, want [%s]", ep, got, kids[0])
	}
	if res := w.call("work list", coord(), nil, map[string]string{"epic": "factorio-none"}); res.Exit != cli.ExitNothing {
		t.Errorf("--epic of an id no item names = %+v, want exit %d", res, cli.ExitNothing)
	}
}
