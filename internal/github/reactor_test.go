package github

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb1"
	shaM = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

func (e *env) reactor() *Reactor {
	e.t.Helper()
	r := NewReactor(e.svc)
	if err := r.Reconcile(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) tick(r *Reactor) {
	e.t.Helper()
	if err := r.Tick(context.Background()); err != nil {
		e.t.Fatalf("tick: %v", err)
	}
}

// AE1: a ticket passed review at A and is ready to merge. A new commit B
// lands on the PR: the ticket returns to review, and no merge can start at
// B without a new pass.
func TestNewHeadAfterAPassReturnsTheTicketToReview(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.ReadyToMerge, shaA)
	pr := w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA})
	r := e.reactor()
	if got := e.status(id).Status; got != machine.ReadyToMerge {
		t.Fatalf("status %s before the push", got)
	}
	w.Update(pr, func(p *gt.PR) { p.HeadSHA = shaB })
	e.tick(r)
	it := e.status(id)
	if it.Status != machine.InReview || it.Fact(machine.FactReviewedHead) != "" || it.Fact(machine.FactHead) != shaB {
		t.Fatalf("after the push: %s %v", it.Status, it.Facts)
	}
	res := e.m.Apply(context.Background(), machine.Request{Project: testProject, Item: id, Verb: "enqueue",
		Params: map[string]string{"head": shaB}, Actor: machine.Server("test")})
	if res.Refusal == nil || res.Refusal.Code != cli.CodeTransitionNotAllowed {
		t.Fatalf("enqueue at B: %+v", res)
	}
	// The server did not push B, so an audit alert follows.
	if e.alerts(AlertForeignHead) != 1 {
		t.Fatal("no foreign-head alert")
	}
}

// C11 of spec factorio-nhb5: a ticket in in_qa at A gets a new commit B.
// The ticket returns to review with no reviewed head.
func TestNewHeadInQAReturnsTheTicketToReview(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InReview, shaA)
	mustOK(t, e.m.Apply(context.Background(), machine.Request{Project: testProject, Item: id, Verb: "review_pass",
		Params: map[string]string{"head": shaA, "qa": "on"}, Actor: machine.Server("test")}))
	pr := w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA})
	r := e.reactor()
	w.Update(pr, func(p *gt.PR) { p.HeadSHA = shaB })
	e.tick(r)
	if it := e.status(id); it.Status != machine.InReview || it.Fact(machine.FactReviewedHead) != "" || it.Fact(machine.FactHead) != shaB {
		t.Fatalf("after the push: %s %v", it.Status, it.Facts)
	}
}

// A head the server pushed raises no alert.
func TestOwnPushRaisesNoAlert(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InReview, shaA)
	pid, err := e.svc.recordPush(context.Background(), testProject, id, BranchFor(id), shaA, shaB, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.settlePush(context.Background(), pid, "done"); err != nil {
		t.Fatal(err)
	}
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaB})
	e.reactor()
	if e.alerts(AlertForeignHead) != 0 {
		t.Fatal("the server's own push raised an alert")
	}
}

// A merge into the base moves a non-terminal ticket to done from any
// state, and a merge into another branch finishes nothing.
func TestMergeMovesTheTicketToDone(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	done := e.ticket(machine.InReview, shaA)
	other := e.ticket(machine.InReview, shaA)
	w.Add(&gt.PR{Head: BranchFor(done), HeadSHA: shaA, State: "closed", Merged: true, MergeSHA: shaM})
	w.Add(&gt.PR{Head: BranchFor(other), HeadSHA: shaA, Base: "release", State: "closed", Merged: true, MergeSHA: shaM})
	if err := NewReactor(e.svc).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if it := e.status(done); it.Status != machine.Done || it.Fact(machine.FactMergedSHA) != shaM {
		t.Fatalf("merged ticket: %s %v", it.Status, it.Facts)
	}
	if got := e.status(other).Status; got != machine.InReview {
		t.Fatalf("a merge into release moved the ticket to %s", got)
	}
}

// The server crashes after it reads a merge and before it records the
// done intent. After a restart the ticket reaches done.
func TestMergeReadBeforeACrashStillLands(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.ReadyToMerge, shaA)
	r := e.reactor()
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA, State: "closed", Merged: true, MergeSHA: shaM})
	// The reactor reads the merge, and beads cannot take the write: the
	// process dies with no done intent recorded.
	e.bx.SetUnavailable(true)
	if err := r.Tick(context.Background()); err == nil {
		t.Fatal("the tick hid the failed merge")
	}
	if c, err := r.cursor(context.Background(), testProject); err != nil || !c.IsZero() {
		t.Fatalf("the cursor advanced past an unapplied merge: %v %v", c, err)
	}
	e.bx.SetUnavailable(false)
	e.m = e.newMachine()
	if err := e.m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.restart()
	e.reactor()
	if got := e.status(id).Status; got != machine.Done {
		t.Fatalf("after the restart the ticket is %s", got)
	}
}

// A PR closed without merge escalates the ticket and raises an alert for
// the inbox. The loop never reopens the PR.
func TestClosedPREscalates(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InReview, shaA)
	pr := w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA})
	r := e.reactor()
	w.Update(pr, func(p *gt.PR) { p.State = "closed" })
	e.tick(r)
	e.tick(r)
	it := e.status(id)
	if it.Status != machine.Escalated || it.Fact(machine.FactResume) != machine.Todo {
		t.Fatalf("closed PR: %s %v", it.Status, it.Facts)
	}
	if e.alerts(AlertPRClosed) != 1 {
		t.Fatal("no single pr_closed alert")
	}
	for _, c := range e.api.Calls() {
		if strings.HasPrefix(c, "PATCH ") || strings.Contains(c, "/reopen") {
			t.Fatalf("the reactor wrote to the PR: %s", c)
		}
	}
}

// A PR from a fork that names an item's branch belongs to no item.
func TestForkPRIsIgnored(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InReview, shaA)
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA, Fork: true, State: "closed", Merged: true, MergeSHA: shaM})
	e.reactor()
	if got := e.status(id).Status; got != machine.InReview {
		t.Fatalf("a fork's merge moved the ticket to %s", got)
	}
}

// CI cancelled by a newer push is superseded, not red. A failure on the
// current head sends the ticket to repair.
func TestCancelledCIIsNotRed(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.InReview, shaB)
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaB})
	t0 := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	w.SetRuns(shaB, gt.CheckRun("ci", shaA, "completed", "cancelled", t0),
		gt.CheckRun("ci", shaB, "in_progress", "", t0.Add(time.Minute)))
	r := e.reactor()
	e.tick(r)
	if got := e.status(id).Status; got != machine.InReview {
		t.Fatalf("a cancelled run moved the ticket to %s", got)
	}
	w.SetRuns(shaB, gt.CheckRun("ci", shaB, "completed", "cancelled", t0),
		gt.CheckRun("ci", shaB, "completed", "failure", t0.Add(time.Minute)))
	e.tick(r)
	if got := e.status(id).Status; got != machine.Repair {
		t.Fatalf("red CI left the ticket in %s", got)
	}
}

func TestClassify(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { x := t0.Add(d); return &x }
	cases := map[string]struct {
		runs     []CheckRun
		statuses []CommitStatus
		want     string
	}{
		"cancelled by a newer push": {runs: []CheckRun{{ID: 1, Name: "ci", HeadSHA: shaA, Status: "completed", Conclusion: "cancelled", StartedAt: at(0)}},
			want: CINone},
		"cancelled, then rerun green": {runs: []CheckRun{
			{ID: 1, Name: "ci", HeadSHA: shaB, Status: "completed", Conclusion: "cancelled", StartedAt: at(0)},
			{ID: 2, Name: "ci", HeadSHA: shaB, Status: "completed", Conclusion: "success", StartedAt: at(time.Minute)}}, want: CIGreen},
		"cancelled latest": {runs: []CheckRun{{ID: 1, Name: "ci", HeadSHA: shaB, Status: "completed", Conclusion: "cancelled", StartedAt: at(0)}},
			want: CIPending},
		"red, then rerun green": {runs: []CheckRun{
			{ID: 1, Name: "ci", HeadSHA: shaB, Status: "completed", Conclusion: "failure", StartedAt: at(0)},
			{ID: 2, Name: "ci", HeadSHA: shaB, Status: "completed", Conclusion: "success", StartedAt: at(time.Minute)}}, want: CIGreen},
		"one red": {runs: []CheckRun{
			{ID: 1, Name: "lint", HeadSHA: shaB, Status: "completed", Conclusion: "success"},
			{ID: 2, Name: "test", HeadSHA: shaB, Status: "completed", Conclusion: "timed_out"}}, want: CIRed},
		"running":              {runs: []CheckRun{{ID: 1, Name: "ci", HeadSHA: shaB, Status: "queued"}}, want: CIPending},
		"status red":           {statuses: []CommitStatus{{Context: "ext", State: "error"}}, want: CIRed},
		"own status is not CI": {statuses: []CommitStatus{{Context: EligibleContext, State: "success"}}, want: CINone},
	}
	for name, c := range cases {
		if got := Classify(shaB, c.runs, c.statuses).State; got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

// A review pass posts loop/eligible on the reviewed sha only.
func TestReviewPassPostsEligibleOnTheReviewedSHA(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.ReadyToMerge, shaA)
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA})
	other := e.ticket(machine.InReview, shaB) // no pass, no status
	w.Add(&gt.PR{Head: BranchFor(other), HeadSHA: shaB})
	r := e.reactor()
	e.tick(r)
	if got := w.Posted(); len(got) != 1 || got[0] != shaA+" "+EligibleContext+" success" {
		t.Fatalf("posted %v, want one success on %s", got, shaA)
	}
}

// The server crashes after a review pass is applied and before
// loop/eligible is posted. After a restart the status is posted, and a
// failed post stays runnable: an unknown result is never done.
func TestEligibleSurvivesACrashAndAFailedPost(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	e.reactor()
	id := e.ticket(machine.ReadyToMerge, shaA)
	// The process dies here: the pass is applied, and nothing is posted.
	e.restart()
	w.FailNext("POST /repos/"+testRepo+"/statuses/"+shaA, 502)
	r := NewReactor(e.svc)
	// The first pass posts loop/qa, and the second tries loop/eligible.
	_ = r.Reconcile(context.Background())
	_ = r.Reconcile(context.Background())
	if len(w.Posted()) != 0 {
		t.Fatal("the failed post was recorded")
	}
	var state string
	var attempts int
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		return tx.QueryRow(`SELECT state, attempts FROM outbox WHERE effect_kind = 'status'`).Scan(&state, &attempts)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "runnable" || attempts != 1 {
		t.Fatalf("after a 502 the effect is %s after %d attempts", state, attempts)
	}
	// The retry is due after its backoff.
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		_, err := tx.Exec(`UPDATE outbox SET next_attempt_at = 0`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.tick(r)
	if got := w.Posted(); len(got) != 1 || !strings.HasPrefix(got[0], shaA) {
		t.Fatalf("posted %v", got)
	}
	e.tick(r)
	if len(w.Posted()) != 1 {
		t.Fatal("the status was posted twice")
	}
	_ = id
}

// After a restart the reactor resumes from its cursor, and its start-up
// reconcile reads the PR of every non-terminal item.
func TestRestartResumesFromTheCursor(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	a := e.ticket(machine.InReview, shaA)
	b := e.ticket(machine.InReview, shaA)
	w.Add(&gt.PR{Head: BranchFor(a), HeadSHA: shaA})
	pb := w.Add(&gt.PR{Head: BranchFor(b), HeadSHA: shaA})
	r := e.reactor()
	e.tick(r)
	cur, err := r.cursor(context.Background(), testProject)
	if err != nil || !cur.Equal(pb.UpdatedAt) {
		t.Fatalf("cursor = %v %v, want %v", cur, err, pb.UpdatedAt)
	}
	e.restart()
	before := len(e.api.Calls())
	r = e.reactor()
	var heads []string
	for _, c := range e.api.Calls()[before:] {
		if strings.Contains(c, "head=acme%3Aloop%2F") {
			heads = append(heads, c)
		}
	}
	if len(heads) != 2 {
		t.Fatalf("the start-up reconcile read %d item PRs: %v", len(heads), heads)
	}
	again, _ := r.cursor(context.Background(), testProject)
	if !again.Equal(cur) {
		t.Fatalf("the restart lost the cursor: %v", again)
	}
}

// The reactor tick holds while the shared cooldown holds.
func TestTickWaitsForTheCooldown(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	r := e.reactor()
	e.svc.cool.extend(time.Now().Add(time.Minute))
	before := len(e.api.Calls())
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.api.Calls()) != before {
		t.Fatal("a tick read GitHub during the cooldown")
	}
}
