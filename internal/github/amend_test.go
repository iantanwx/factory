package github

import (
	"context"
	"strconv"
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// specInSignoff drives an epic to spec_signoff with an open spec PR at
// head, and returns the epic and the spec PR.
func (e *env) specInSignoff(head string) (string, *gt.PR) {
	e.t.Helper()
	ctx := context.Background()
	w := e.api.World
	prd := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA, State: "closed", Merged: true, MergeSHA: shaM})
	id := e.epicInReview(prd.Number)
	apply := func(verb string, params map[string]string) {
		mustOK(e.t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: verb, Params: params, Actor: machine.Server("test")}))
	}
	apply("review_pass", map[string]string{"head": shaA})
	apply("doc_merged", map[string]string{"sha": shaM})
	_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StagePlanner, RunID: "run-spec"})
	mustOK(e.t, res)
	spec := w.Add(&gt.PR{Head: SpecBranchFor(id), HeadSHA: head})
	apply("pr_open", map[string]string{"pr": strconv.Itoa(spec.Number)})
	apply("review_pass", map[string]string{"head": head})
	return id, spec
}

// 1.12: the operator comments on a spec in sign-off. The reactor moves
// the epic to the amender, who replies and pushes nothing, so the spec
// returns to sign-off. The reply answers the comment, and the next tick
// moves nothing.
func TestPersonCommentOnASignedOffSpecWakesTheAmender(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	id, spec := e.specInSignoff(shaB)
	r := e.reactor()
	w.Say(spec.Number, gt.Said{Login: "operator", Association: "OWNER", Body: "ADR-0011 is superseded by ADR-0013."})
	e.tick(r)
	it := e.status(id)
	if it.Status != machine.SpecAmending || it.Fact(machine.FactAmendFrom) != machine.SpecSignoff || it.Fact(machine.FactAmendHead) != shaB {
		t.Fatalf("after the comment: %s %v", it.Status, it.Facts)
	}
	_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageReviser, RunID: "run-" + machine.StageReviser})
	mustOK(t, res)
	if _, _, err := e.svc.Reply(ctx, e.project, spec.Number, 0, "The spec now cites ADR-0013 nowhere; it needs no change.", "k1"); err != nil {
		t.Fatal(err)
	}
	mustOK(t, e.runCall("pr open", id, machine.StageReviser, map[string]string{"title": "spec"}, nil))
	if it := e.status(id); it.Status != machine.SpecSignoff || it.Owner != "" {
		t.Fatalf("after the amender's reply: %s owner %q", it.Status, it.Owner)
	}
	e.tick(r)
	if got := e.status(id).Status; got != machine.SpecSignoff {
		t.Fatalf("an answered comment moved the epic to %s", got)
	}
}

// A review thread whose last word is a person's waits for the amender,
// also after the loop answered it once. A run that holds the epic keeps
// it: the comment waits for the next tick after the run ends.
func TestPersonReviewThreadWaitsForTheRunThatHoldsTheEpic(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	prd := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	id := e.epicInReview(prd.Number)
	w.Update(prd, func(p *gt.PR) { p.Head = BranchFor(id) })
	_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageDocReviewer, RunID: "run-rev"})
	mustOK(t, res)
	r := e.reactor()
	root := w.Say(prd.Number, gt.Said{Login: "pat", Association: "COLLABORATOR", Review: true, Body: "US-02 has no label."})
	if _, _, err := e.svc.Reply(ctx, e.project, prd.Number, root, "US-02 carries its label in section 2.", "k1"); err != nil {
		t.Fatal(err)
	}
	w.Say(prd.Number, gt.Said{Login: "pat", Association: "COLLABORATOR", Review: true, InReplyTo: root, Body: "It does not."})
	e.tick(r)
	if got := e.status(id).Status; got != machine.PRDReview {
		t.Fatalf("a held epic moved to %s", got)
	}
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "review_pass", Params: map[string]string{"head": shaA}, Actor: machine.Server("test")}))
	e.tick(r)
	if it := e.status(id); it.Status != machine.PRDAmending || it.Fact(machine.FactAmendFrom) != machine.PRDSignoff {
		t.Fatalf("after the review ended: %s %v", it.Status, it.Facts)
	}
}

// The amendment poll reads every amendable epic on every tick, and needs
// only who spoke last. So it reads REST alone: a review thread whose state
// GraphQL does not name still wakes the amender, and stops no other epic.
func TestAmendmentPollNeedsNoThreadState(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	prd := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	id := e.epicInReview(prd.Number)
	w.Update(prd, func(p *gt.PR) { p.Head = BranchFor(id) })
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: "review_pass", Params: map[string]string{"head": shaA}, Actor: machine.Server("test")}))
	r := e.reactor()
	root := w.Say(prd.Number, gt.Said{Login: "pat", Association: "COLLABORATOR", Review: true, Body: "US-02 has no label."})
	w.SetThread(root, false, false, true)
	e.tick(r)
	if it := e.status(id); it.Status != machine.PRDAmending {
		t.Fatalf("after the comment: %s %v", it.Status, it.Facts)
	}
}

// Another program's comment is nobody's question: a deployment notice
// after the loop's own comment leaves the loop with the last word.
func TestBotCommentWakesNoStage(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	prd := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
	id := e.epicInReview(prd.Number)
	w.Update(prd, func(p *gt.PR) { p.Head = BranchFor(id) })
	r := e.reactor()
	if _, _, err := e.svc.Reply(context.Background(), e.project, prd.Number, 0, "The doc-reviewer's verdict follows.", "k1"); err != nil {
		t.Fatal(err)
	}
	w.Say(prd.Number, gt.Said{Login: "vercel[bot]", Association: "NONE", Bot: true, Body: "Preview ready."})
	e.tick(r)
	if got := e.status(id).Status; got != machine.PRDReview {
		t.Fatalf("a bot's comment moved the epic to %s", got)
	}
}

// A person who submits a review with a body and no line comment leaves
// the feedback a phone leaves most often. It wakes the amender like a
// conversation comment, and the loop's conversation reply answers it. An
// approval with no body is a click, not feedback, and wakes nothing.
func TestPersonReviewSummaryWakesTheAmender(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	id, spec := e.specInSignoff(shaB)
	r := e.reactor()
	w.Say(spec.Number, gt.Said{Login: "operator", Association: "OWNER", Summary: "APPROVED"})
	e.tick(r)
	if got := e.status(id).Status; got != machine.SpecSignoff {
		t.Fatalf("an approval with no body moved the epic to %s", got)
	}
	w.Say(spec.Number, gt.Said{Login: "operator", Association: "OWNER", Summary: "CHANGES_REQUESTED", Body: "Name the cap."})
	e.tick(r)
	if it := e.status(id); it.Status != machine.SpecAmending || it.Fact(machine.FactAmendFrom) != machine.SpecSignoff {
		t.Fatalf("after the review summary: %s %v", it.Status, it.Facts)
	}
	_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageReviser, RunID: "run-" + machine.StageReviser})
	mustOK(t, res)
	if _, _, err := e.svc.Reply(ctx, e.project, spec.Number, 0, "The cap is named in section 4.", "k1"); err != nil {
		t.Fatal(err)
	}
	mustOK(t, e.runCall("pr open", id, machine.StageReviser, map[string]string{"title": "spec"}, nil))
	e.tick(r)
	if got := e.status(id).Status; got != machine.SpecSignoff {
		t.Fatalf("an answered review summary left the epic in %s", got)
	}
}

func TestAwaitsAnswer(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 10, 24, 9, m, 0, 0, time.UTC) }
	person := func(id, reply int64, kind string, m int) Comment {
		return Comment{ID: id, InReplyTo: reply, Kind: kind, AuthorClass: AuthorOther, CreatedAt: at(m)}
	}
	ours := func(id, reply int64, kind string, m int) Comment {
		return Comment{ID: id, InReplyTo: reply, Kind: kind, AuthorClass: AuthorApp, bot: true, CreatedAt: at(m)}
	}
	bot := func(id int64, kind string, m int) Comment {
		return Comment{ID: id, Kind: kind, AuthorClass: AuthorOther, bot: true, CreatedAt: at(m)}
	}
	cases := []struct {
		name string
		cs   []Comment
		want bool
	}{
		{"no comment", nil, false},
		{"a person's loose comment", []Comment{person(1, 0, "conversation", 1)}, true},
		{"our answer after it", []Comment{person(1, 0, "conversation", 1), ours(2, 0, "conversation", 2)}, false},
		{"a bot after our answer", []Comment{person(1, 0, "conversation", 1), ours(2, 0, "conversation", 2), bot(3, "conversation", 3)}, false},
		{"a bot after a person", []Comment{ours(1, 0, "conversation", 1), person(2, 0, "conversation", 2), bot(3, "conversation", 3)}, true},
		{"only bots", []Comment{bot(1, "conversation", 1)}, false},
		{"a thread we answered", []Comment{person(1, 0, "review", 1), ours(2, 1, "review", 2)}, false},
		{"a second thread nobody answered", []Comment{person(1, 0, "review", 1), ours(2, 1, "review", 2), person(3, 0, "review", 3)}, true},
		{"a thread nobody answered before one we answered", []Comment{person(1, 0, "review", 1), person(2, 0, "review", 2), ours(3, 2, "review", 3)}, true},
		{"a person's last word in a thread", []Comment{person(1, 0, "review", 1), ours(2, 1, "review", 2), person(3, 1, "review", 3)}, true},
		{"our loose comment answers no thread", []Comment{person(1, 0, "review", 1), ours(2, 0, "conversation", 2)}, true},
		{"the operator's comment", []Comment{{ID: 1, Kind: "conversation", AuthorClass: AuthorOperator, CreatedAt: at(1)}}, true},
	}
	for _, c := range cases {
		if got := awaitsAnswer(c.cs); got != c.want {
			t.Errorf("%s: awaitsAnswer = %v, want %v", c.name, got, c.want)
		}
	}
}

// The amender pushes a revision of a signed-off spec, and GitHub's PR
// still names the old head when pr open reads it. The server's own push
// is the head, so the revision goes to review and never back to sign-off.
func TestRevisionReturnsToReviewWhileGitHubLagsThePush(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	id, spec := e.specInSignoff(shaB)
	r := e.reactor()
	w.Say(spec.Number, gt.Said{Login: "operator", Association: "OWNER", Body: "Name the cap."})
	e.tick(r)
	_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageReviser, RunID: "run-" + machine.StageReviser})
	mustOK(t, res)
	const shaC = "cccccccccccccccccccccccccccccccccccccccc"
	pid, err := e.svc.recordPush(ctx, testProject, id, SpecBranchFor(id), shaB, shaC, "run-"+machine.StageReviser)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.settlePush(ctx, pid, "done"); err != nil {
		t.Fatal(err)
	}
	mustOK(t, e.runCall("pr open", id, machine.StageReviser, map[string]string{"title": "spec"}, nil))
	if got := e.status(id).Status; got != machine.SpecReview {
		t.Fatalf("a pushed revision landed on %s", got)
	}
}
