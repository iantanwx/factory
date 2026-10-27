package github

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// C17: pr pending lists each item pull request that owes a reply. Of three
// open pull requests, only the ticket's lists: its last review-thread word
// is a person's. The App answered the person on the spec's, and a person's
// own branch maps to no item. The App's reply on the ticket's thread then
// empties the list.
func TestPendingListsEachItemPullRequestThatOwesAReply(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	_, spec := e.specInSignoff(shaB)
	ticket := e.ticket(machine.InReview, shaA)
	pr := w.Add(&gt.PR{Head: BranchFor(ticket), HeadSHA: shaA})
	own := w.Add(&gt.PR{Head: "pat/idea", HeadSHA: shaA})

	root := w.Say(pr.Number, gt.Said{Login: "pat", Association: "COLLABORATOR", Review: true, Body: "Name the cap."})
	since := w.Now()
	w.Say(spec.Number, gt.Said{Login: "operator", Association: "OWNER", Body: "Cite ADR-0013."})
	if _, _, err := e.svc.Reply(ctx, e.project, spec.Number, 0, "The spec cites ADR-0013 in section 2.", "k1"); err != nil {
		t.Fatal(err)
	}
	w.Say(own.Number, gt.Said{Login: "pat", Association: "OWNER", Body: "Draft, do not merge."})

	got, err := e.svc.Pending(ctx, e.project)
	if err != nil {
		t.Fatal(err)
	}
	want := []Pending{{Project: testProject, Item: ticket, Status: machine.InReview, Number: pr.Number, Branch: BranchFor(ticket),
		Since: since, Count: 1, Link: fmt.Sprintf("https://example.invalid/pull/%d", pr.Number)}}
	if !slices.Equal(got, want) {
		t.Fatalf("pending = %+v\nwant %+v", got, want)
	}

	if _, _, err := e.svc.Reply(ctx, e.project, pr.Number, root, "The cap is 20 in section 4.", "k2"); err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.Pending(ctx, e.project); err != nil || len(got) != 0 {
		t.Fatalf("pending after the App's reply = %+v, %v", got, err)
	}
}

// unanswered names the last word of each thread that a person had: one
// review thread by its root, and the conversation as one thread.
func TestUnansweredNamesEachThreadsLastPersonsWord(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 10, 24, 9, m, 0, 0, time.UTC) }
	cs := []Comment{
		{ID: 1, Kind: "review", AuthorClass: AuthorOther, CreatedAt: at(1)},
		{ID: 2, Kind: "review", InReplyTo: 1, AuthorClass: AuthorApp, bot: true, CreatedAt: at(2)},
		{ID: 3, Kind: "review", InReplyTo: 1, AuthorClass: AuthorOperator, CreatedAt: at(3)},
		{ID: 4, Kind: "review", AuthorClass: AuthorOther, CreatedAt: at(4)},
		{ID: 5, Kind: "review", InReplyTo: 4, AuthorClass: AuthorApp, bot: true, CreatedAt: at(5)},
		{ID: 6, Kind: "conversation", AuthorClass: AuthorOther, CreatedAt: at(6)},
		{ID: 7, Kind: "conversation", AuthorClass: AuthorOther, CreatedAt: at(7)},
		{ID: 8, Kind: "conversation", AuthorClass: AuthorOther, bot: true, CreatedAt: at(8)},
	}
	var ids []int64
	for _, c := range unanswered(cs) {
		ids = append(ids, c.ID)
	}
	slices.Sort(ids)
	if want := []int64{3, 7}; !slices.Equal(ids, want) {
		t.Fatalf("unanswered = %v, want %v", ids, want)
	}
}

// C18: the reactor and pr pending read one rule. Over 1,000 generated
// comment lists of persons, the App and other programs, in review threads
// and the conversation, awaitsAnswer agrees with unanswered.
func TestAwaitsAnswerAndUnansweredReadOneRule(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 0xc6d))
	classes := []string{AuthorApp, AuthorOperator, AuthorOther}
	owing := 0
	for range 1000 {
		var cs []Comment
		for i := range rng.IntN(8) {
			c := Comment{ID: int64(i + 1), Kind: "conversation", AuthorClass: classes[rng.IntN(len(classes))],
				CreatedAt: time.Date(2026, 10, 24, 9, rng.IntN(4), 0, 0, time.UTC)}
			c.bot = c.AuthorClass == AuthorApp || rng.IntN(4) == 0
			if rng.IntN(2) == 0 {
				c.Kind = "review"
				if i > 0 {
					c.InReplyTo = int64(rng.IntN(i + 1))
				}
			}
			cs = append(cs, c)
		}
		u := unanswered(cs)
		if awaitsAnswer(cs) != (len(u) > 0) {
			t.Fatalf("awaitsAnswer = %v and unanswered = %+v for %+v", awaitsAnswer(cs), u, cs)
		}
		if len(u) > 0 {
			owing++
		}
	}
	if owing == 0 || owing == 1000 {
		t.Fatalf("%d of 1000 lists owe a reply; the generator covers one side only", owing)
	}
}
