package machine

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

// claim grants the item to run for stage and returns the epoch.
func (w *world) claim(id, stage, run string) int64 {
	w.t.Helper()
	l, res := w.m.Claim(context.Background(), ClaimRequest{Project: prj, Item: id, Stage: stage, RunID: run})
	mustOK(w.t, res)
	if l.Owner != run {
		w.t.Fatalf("lease owner = %q, want %s", l.Owner, run)
	}
	return l.Epoch
}

// runWrite is a write by run, fenced at epoch.
func (w *world) runWrite(id, run string, epoch int64, verb string, params map[string]string) cli.Result {
	w.t.Helper()
	return w.m.Apply(context.Background(), Request{Project: prj, Item: id, Verb: verb, Params: params,
		Actor: Actor{Name: "run:" + run, RunID: run, Fenced: true, Epoch: epoch}})
}

// ticketInReview walks a new ticket to in_review the way the loop does.
func (w *world) ticketInReview(head string) string {
	id := w.create(KindTicket, "create", nil)
	w.claim(id, StageImplementer, "r-impl")
	w.must(id, "pr_open", map[string]string{"head": head})
	return id
}

func TestEscalateStoresTheResumeTargetAndAnswerReturnsToIt(t *testing.T) {
	w := newWorld(t)
	id := w.ticketInReview("h1")
	w.must(id, "escalate", nil)
	it := w.show(id)
	if it.Status != Escalated || it.Fact(FactResume) != Todo || it.Fact(FactResumeEntry) != EntryRework {
		t.Fatalf("escalate must store the resume target in its own write: %+v", it)
	}
	if h := w.history(id); h[len(h)-1].Set[factPrefix+FactResume] != Todo {
		t.Fatalf("the escalate transition carries no resume target: %+v", h[len(h)-1])
	}
	w.must(id, "answer", nil)
	it = w.show(id)
	if it.Status != Todo || it.Fact(FactEntry) != EntryRework || it.Fact(FactResume) != "" {
		t.Fatalf("answer must land on the stored target and clear it: %+v", it)
	}

	// An epic held in spec_drafting resumes to the queue it came from.
	ep := w.create(KindEpic, "create_architecture", nil)
	w.must(ep, "adr_landed", map[string]string{"path": "docs/adr/0001-x.md", "added": "a1"})
	w.claim(ep, StageSpecAuthor, "r-spec")
	w.must(ep, "escalate", nil)
	if got := w.show(ep).Fact(FactResume); got != SpecNeeded {
		t.Fatalf("spec_drafting resumes to %q, want spec_needed", got)
	}
	w.must(ep, "answer", nil)
	if got := w.show(ep); got.Status != SpecNeeded || got.Owner != "" {
		t.Fatalf("epic after answer: %+v", got)
	}
}

// C10: an answer returns a ticket to backlog when it was escalated from
// backlog. Every other state resumes to todo: rework when a pull request
// was open, resume otherwise.
func TestC10EscalatedTicketResumesByItsEscalateState(t *testing.T) {
	w := newWorld(t)
	cases := []struct {
		from      string
		make      func() string
		to, entry string
	}{
		{Backlog, func() string { return w.create(KindTicket, "create", map[string]string{"blocked": "true"}) }, Backlog, ""},
		{Todo, func() string { return w.create(KindTicket, "create", nil) }, Todo, EntryResume},
		{InProgress, func() string {
			id := w.create(KindTicket, "create", nil)
			w.claim(id, StageImplementer, "r-impl")
			return id
		}, Todo, EntryResume},
		{InReview, func() string { return w.ticketInReview("h1") }, Todo, EntryRework},
		{Repair, func() string {
			id := w.ticketInReview("h1")
			w.must(id, VerbCIRed, nil)
			return id
		}, Todo, EntryRework},
	}
	for _, c := range cases {
		id := c.make()
		if got := w.show(id).Status; got != c.from {
			t.Fatalf("%s: the setup left the ticket in %s", c.from, got)
		}
		w.must(id, "escalate", nil)
		w.must(id, "answer", nil)
		it := w.show(id)
		if it.Status != c.to || it.Fact(FactEntry) != c.entry || it.Fact(FactResume) != "" {
			t.Fatalf("escalated from %s, answered: status %s entry %q resume %q, want %s entry %q",
				c.from, it.Status, it.Fact(FactEntry), it.Fact(FactResume), c.to, c.entry)
		}
	}
}

func TestSecondReviewRejectEscalatesTheModel(t *testing.T) {
	w := newWorld(t)
	id := w.ticketInReview("h1")
	w.must(id, "review_reject", nil)
	if it := w.show(id); it.Fact(FactModelTier) != "" || it.Fact(FactEntry) != EntryRework || it.Status != Todo {
		t.Fatalf("after one reject: %+v", it)
	}
	w.claim(id, StageImplementer, "r-2")
	w.must(id, "pr_open", map[string]string{"head": "h2"})
	w.must(id, "review_reject", nil)
	if it := w.show(id); it.Fact(FactModelTier) != ModelTierEscalated || it.Fact(FactRejects) != "2" {
		t.Fatalf("a second reject must set model_tier escalated: %+v", it)
	}
}

// AE1: a new commit after a pass returns the ticket to review and clears
// the reviewed head in the same write, and no merge starts at B.
func TestHeadChangeAfterAPassReturnsToReview(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.ticketInReview("A")
	w.must(id, "review_pass", map[string]string{"head": "A"})
	if it := w.show(id); it.Status != ReadyToMerge || it.Fact(FactReviewedHead) != "A" {
		t.Fatalf("after the pass: %+v", it)
	}
	if res := w.m.HeadChanged(ctx, prj, id, "A"); res.Exit != cli.ExitNothing {
		t.Fatalf("the reviewed head is no change: %+v", res)
	}
	mustOK(t, w.m.HeadChanged(ctx, prj, id, "B"))
	it := w.show(id)
	if it.Status != InReview || it.Fact(FactReviewedHead) != "" || it.Fact(FactHead) != "B" {
		t.Fatalf("after head B: %+v", it)
	}
	h := w.history(id)
	last := h[len(h)-1]
	if last.Verb != "head_changed" || last.To != InReview || len(last.Unset) != 1 || last.Unset[0] != factPrefix+FactReviewedHead {
		t.Fatalf("the status and the cleared head must be one write: %+v", last)
	}
	// No merge can start at B without a new pass.
	w.force(id, ReadyToMerge, nil)
	ref := wantCode(t, w.apply(id, "enqueue", map[string]string{"head": "B"}), cli.CodeTransitionNotAllowed)
	if ref.Rule != "head_not_reviewed" {
		t.Fatalf("rule = %s", ref.Rule)
	}
}

func TestMergeDuringRepairFinishesTheTicketAndFencesTheOldRun(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.ticketInReview("h1")
	w.must(id, "ci_red", nil)
	epoch := w.claim(id, StageImplementer, "r-repair")
	mustOK(t, w.runWrite(id, "r-repair", epoch, "note", map[string]string{"body": "fixing"}))

	mustOK(t, w.m.MergeObserved(ctx, MergeEvent{Project: prj, Item: id, Base: "main", Into: "main", SHA: "m1"}))
	it := w.show(id)
	if it.Status != Done || it.Epoch != epoch+1 || it.Owner != "" || it.Fact(FactMergedSHA) != "m1" {
		t.Fatalf("after the merge: %+v (epoch was %d)", it, epoch)
	}
	ref := wantCode(t, w.runWrite(id, "r-repair", epoch, "note", map[string]string{"body": "late"}), cli.CodeLeaseNotHeld)
	if ref.Rule != "stale_epoch" {
		t.Fatalf("rule = %s", ref.Rule)
	}
	// A second observation of the same merge changes nothing.
	if res := w.m.MergeObserved(ctx, MergeEvent{Project: prj, Item: id, Base: "main", Into: "main"}); res.Exit != cli.ExitNothing {
		t.Fatalf("a merge of a done ticket: %+v", res)
	}
}

func TestMergeIntoAnotherBranchChangesNothing(t *testing.T) {
	w := newWorld(t)
	id := w.ticketInReview("h1")
	before := w.show(id)
	res := w.m.MergeObserved(context.Background(), MergeEvent{Project: prj, Item: id, Base: "main", Into: "release/1"})
	if res.Exit != cli.ExitNothing || res.Refusal != nil {
		t.Fatalf("got %+v", res)
	}
	if after := w.show(id); after.Status != before.Status || after.Seq != before.Seq || after.Epoch != before.Epoch {
		t.Fatalf("the item changed: %+v", after)
	}
}

func TestFactsWithAStoredSecretAreRefusedBeforeTheIntent(t *testing.T) {
	w := newWorld(t)
	w.red.SetLive("project/alpha/API_TOKEN", "tok-0123456789abcdef")
	id := w.create(KindTicket, "create", nil)
	intents := w.count(`SELECT count(*) FROM intents`)
	ref := wantCode(t, w.apply(id, "note", map[string]string{"body": "the key is tok-0123456789abcdef"}), cli.CodeSecretInOutput)
	if ref.Rule != "secret_in_facts" {
		t.Fatalf("rule = %s", ref.Rule)
	}
	if n := w.count(`SELECT count(*) FROM intents`); n != intents {
		t.Fatalf("a refused secret recorded an intent: %d, want %d", n, intents)
	}
	if w.show(id).Fact(FactNote) != "" {
		t.Fatal("the secret reached beads")
	}
}

func TestBeadEpochAheadRaisesSQLiteAndAlerts(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.create(KindTicket, "create", nil)
	epoch := w.claim(id, StageImplementer, "r-1")
	// Another server instance granted twice more, and SQLite never heard.
	is, _ := w.bc.Get(ctx, id)
	if _, err := w.bc.Update(ctx, id, beadsx.UpdateSpec{IfStatus: is.Status, IfAssignee: &is.Assignee,
		SetMetadata: map[string]string{beadsx.MetaEpoch: itoa(epoch + 2)}}); err != nil {
		t.Fatal(err)
	}
	w.restart()
	l, err := w.m.Lease(ctx, prj, id)
	if err != nil || l.Epoch != epoch+2 {
		t.Fatalf("lease = %+v, %v; want epoch %d", l, err, epoch+2)
	}
	if w.alerts(AlertEpochAhead) != 1 {
		t.Fatal("no epoch alert")
	}
}

func TestReadoptedRunKeepsItsEpoch(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.create(KindTicket, "create", nil)
	epoch := w.claim(id, StageImplementer, "r-1")
	w.restart()
	l, err := w.m.Renew(ctx, prj, id, "r-1")
	if err != nil || l.Epoch != epoch || l.Owner != "r-1" {
		t.Fatalf("renew = %+v, %v", l, err)
	}
	mustOK(t, w.runWrite(id, "r-1", epoch, "note", map[string]string{"body": "still here"}))
	if _, err := w.m.Renew(ctx, prj, id, "r-other"); err == nil {
		t.Fatal("a run that holds no lease renewed it")
	}
}

func TestEpicInExecutionWithAnOpenChildRefusesDone(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.create(KindEpic, "create_architecture", nil)
	w.force(ep, Execution, nil)
	child, res := w.m.Create(ctx, CreateRequest{Project: prj, Kind: KindTicket, Title: "c", Verb: "create",
		Actor: Server("test"), Facts: map[string]string{FactEpic: ep}})
	mustOK(t, res)
	ref := wantCode(t, w.apply(ep, "complete", nil), cli.CodeDependencyOpen)
	if ref.Rule != "children_open" || w.show(ep).Status != Execution {
		t.Fatalf("refusal %+v", ref)
	}
	w.force(child, Escalated, nil)
	// The last child's cancel applies the epic's complete row.
	w.must(child, "cancel", map[string]string{"reason": "dropped"})
	if w.show(ep).Status != Done {
		t.Fatal("the epic did not close")
	}
}

func TestSecondPRDRejectWaitsForAPerson(t *testing.T) {
	// AE7: the amender revises once, and the second reject escalates.
	w := newWorld(t)
	ep := w.create(KindEpic, "prd_submit", nil)
	w.must(ep, "review_reject", nil)
	if w.show(ep).Status != PRDAmending {
		t.Fatal("the first reject goes to the amender")
	}
	w.must(ep, "prd_amended", nil)
	w.must(ep, "review_reject", nil)
	it := w.show(ep)
	if it.Status != Escalated || it.Fact(FactResume) != PRDReview {
		t.Fatalf("the second reject: %+v", it)
	}
	w.must(ep, "answer", nil)
	w.must(ep, "review_reject", nil)
	if w.show(ep).Status != Escalated {
		t.Fatal("no unattended stage revises the PRD again")
	}
}

func TestExpectedStatusThatMovedIsStatusChanged(t *testing.T) {
	w := newWorld(t)
	id := w.ticketInReview("h1")
	res := w.m.Apply(context.Background(), Request{Project: prj, Item: id, Verb: "note", Params: map[string]string{"body": "x"},
		ExpectedStatus: InProgress, Actor: Server("test")})
	ref := wantCode(t, res, cli.CodeStatusChanged)
	if ref.CurrentStatus != InReview || ref.ExpectedStatus != InProgress {
		t.Fatalf("refusal %+v", ref)
	}
}

func TestACommandPerformsOnlyItsOwnRows(t *testing.T) {
	w := newWorld(t)
	id := w.ticketInReview("h1")
	res := w.m.Apply(context.Background(), Request{Project: prj, Item: id, Verb: "merged", Command: "review submit", Actor: Server("test")})
	wantCode(t, res, cli.CodeTransitionNotAllowed)
	if w.show(id).Status != InReview {
		t.Fatal("a CLI verb wrote done")
	}
}
