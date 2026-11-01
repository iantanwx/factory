package merge_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// F-66: a ticket that passed review, whose head lacks only a base commit
// that a clean merge brings in and that touches no file of its PR, is not
// sent back to the implementer for a new review round. It stays with the
// merge mechanism until it merges.
func TestDirectLandsAPassedTicketWhenTheBaseMovedWithNoConflict(t *testing.T) {
	e := newDirect(t)
	first, _ := e.reviewed(sha('1'), true)
	second, _ := e.reviewed(sha('2'), true)
	e.tick()
	e.wantStatus(first, machine.Queued)
	e.wantStatus(second, machine.Queued)
	// The first ticket merges, and the base moves past the second head.
	for range 3 {
		e.tick()
	}
	e.wantStatus(first, machine.Done)
	it := e.item(second)
	if it.Status != machine.Queued && it.Status != machine.Done {
		t.Fatalf("%s is %s with entry %q; want queued or done, with no new review round",
			second, it.Status, it.Fact(machine.FactEntry))
	}
}

// C-bug-1: the server merges the moved base into a passed head that
// shares no file with the base change. The ticket stays queued at the
// merge commit with no new review round, and it lands once CI passes on
// that head, which holds the tip of the base.
func TestDirectMergesTheMovedBaseInAndLandsOnGreenCI(t *testing.T) {
	e := newDirect(t)
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	e.wantStatus(id, machine.Queued)
	e.world.AdvanceMain("docs/other.md")
	e.tick()
	it := e.wantStatus(id, machine.Queued)
	head := e.world.PRs()[0].HeadSHA
	if head != gt.BaseMergeSHA(1) || it.Fact(machine.FactReviewedHead) != head {
		t.Fatalf("PR head %s, reviewed_head %s; want both at the merge of the base %s",
			head, it.Fact(machine.FactReviewedHead), gt.BaseMergeSHA(1))
	}
	if got := e.world.Merges(); len(got) != 0 {
		t.Fatalf("merge calls = %+v; want none before CI passes on the merge of the base", got)
	}
	e.tick()
	e.wantStatus(id, machine.Queued)
	e.world.SetRuns(head, gt.CheckRun("ci", head, "completed", "success", time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)))
	e.tick()
	e.wantStatus(id, machine.Queued) // loop/eligible is not posted on the merge of the base yet
	e.postEligibility()
	e.tick()
	e.wantStatus(id, machine.Done)
	if got := e.world.Merges(); len(got) != 1 || got[0] != (gt.MergeCall{PR: pr.Number, SHA: head, Method: "merge", Status: 200}) {
		t.Fatalf("merge calls = %+v; want one merge at the merge of the base", got)
	}
	if n := e.transitions(id, "review_pass"); n != 1 {
		t.Fatalf("%d review passes; want 1", n)
	}
}

// Red CI on the head that holds the merge of the base sends the ticket
// to repair, as red CI on any queued head does.
func TestDirectSendsRedCIOnTheMergeOfTheBaseToRepair(t *testing.T) {
	e := newDirect(t)
	id, _ := e.reviewed(sha('1'), true)
	e.tick()
	e.world.AdvanceMain("docs/other.md")
	e.tick()
	head := e.world.PRs()[0].HeadSHA
	e.world.SetRuns(head, failing(head))
	e.tick()
	e.wantStatus(id, machine.Repair)
}

// A base change that touches a file of the PR is no merge the server
// makes on its own: the ticket goes to todo with entry rework, and the
// branch is left as it was.
func TestDirectSendsABehindHeadToReworkWhenTheBaseChangedAFileOfThePR(t *testing.T) {
	e := newDirect(t)
	id, pr := e.reviewed(sha('1'), true)
	e.world.Update(pr, func(p *gt.PR) { p.Files = []string{"internal/a.go"} })
	e.tick()
	e.world.AdvanceMain("internal/a.go")
	e.tick()
	it := e.wantStatus(id, machine.Todo)
	if it.Fact(machine.FactEntry) != machine.EntryRework {
		t.Fatalf("entry = %q; want rework", it.Fact(machine.FactEntry))
	}
	if head := e.world.PRs()[0].HeadSHA; head != sha('1') {
		t.Fatalf("the branch moved to %s; want it left at the reviewed head", head)
	}
}

// The reactor can read the PR between the server's merge of the base and
// the move of the ticket to that head, as after a crash between the two.
// It reads the new head as the server's own: no foreign-head alert, and
// no return to review. The next hand-off tick moves the ticket there.
func TestReactorLeavesTheServersMergeOfTheBaseToTheHandOff(t *testing.T) {
	e := newDirect(t)
	id, _ := e.reviewed(sha('1'), true)
	e.tick()
	e.world.AdvanceMain("docs/other.md")
	merged, err := e.svc.MergeBase(e.ctx, e.project, id, "main", sha('1'))
	if err != nil || merged != gt.BaseMergeSHA(1) {
		t.Fatalf("merge of the base = %s, %v", merged, err)
	}
	e.reactorTick()
	e.wantStatus(id, machine.Queued)
	var alerts int
	if err := e.st.Read(e.ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(e.ctx, `SELECT count(*) FROM alerts WHERE kind = ?`, github.AlertForeignHead).Scan(&alerts)
	}); err != nil {
		t.Fatal(err)
	}
	if alerts != 0 {
		t.Fatalf("%d foreign-head alerts; want none", alerts)
	}
	e.tick()
	it := e.wantStatus(id, machine.Queued)
	if it.Fact(machine.FactReviewedHead) != merged {
		t.Fatalf("reviewed_head = %s; want the merge of the base %s", it.Fact(machine.FactReviewedHead), merged)
	}
	if n := e.transitions(id, "head_changed"); n != 0 {
		t.Fatalf("%d head_changed moves; want none", n)
	}
}
