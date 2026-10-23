package merge_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/direct"
)

func newDirect(t *testing.T, opts ...func(*github.Config)) *env {
	return newEnv(t, func(e *env) merge.Mechanism { return direct.New(e.svc) }, opts...)
}

// An eligible PR merges at its reviewed head with a merge commit.
func TestDirectMergesAtTheReviewedHeadWithAMergeCommit(t *testing.T) {
	e := newDirect(t)
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	e.wantStatus(id, machine.Queued)
	e.tick()
	it := e.wantStatus(id, machine.Done)
	if it.Fact(machine.FactMergedSHA) != gt.MergeSHA(pr.Number) {
		t.Fatalf("merged_sha = %q", it.Fact(machine.FactMergedSHA))
	}
	got := e.world.Merges()
	if len(got) != 1 || got[0] != (gt.MergeCall{PR: pr.Number, SHA: sha('1'), Method: "merge", Status: 200}) {
		t.Fatalf("merge calls = %+v; want one merge commit at the reviewed head", got)
	}
}

// A head that moved makes the merge fail, and the ticket returns to
// review.
func TestDirectRefusesAMovedHeadAndReturnsTheTicketToReview(t *testing.T) {
	e := newDirect(t)
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	e.world.Update(pr, func(p *gt.PR) { p.HeadSHA = sha('3') })
	r, err := e.mech.Poll(e.ctx, merge.PR{Project: e.project, Item: id, Number: pr.Number, Head: sha('1'), Base: "main"})
	if err != nil || r.Outcome == merge.Merged {
		t.Fatalf("poll at a moved head = %+v, %v; want no merge", r, err)
	}
	if got := e.world.Merges(); len(got) != 1 || got[0].SHA != sha('1') || got[0].Status != http.StatusConflict {
		t.Fatalf("merge calls = %+v; want one refused call at the reviewed head", got)
	}
	e.tick()
	e.wantStatus(id, machine.InReview)
	if e.world.PRs()[0].Merged {
		t.Fatal("the moved head merged")
	}
}

// A branch that GitHub refuses to merge because it is behind the base
// moves the ticket to todo with entry rework: with no queue, only a
// merge of the base into the branch lands it.
func TestDirectSendsABranchBehindTheBaseToRework(t *testing.T) {
	e := newDirect(t)
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	e.world.Update(pr, func(p *gt.PR) { p.MergeableState = merge.MergeableBehind })
	e.tick()
	it := e.wantStatus(id, machine.Todo)
	if it.Fact(machine.FactEntry) != machine.EntryRework {
		t.Fatalf("entry = %q; want rework", it.Fact(machine.FactEntry))
	}
}

// A refused merge while GitHub still computes the mergeable state is no
// verdict: the PR stays queued, and the next poll tries again.
func TestDirectRetriesARefusalWhileTheMergeableStateIsUnknown(t *testing.T) {
	rt := &afterMerge{}
	e := newDirect(t, func(c *github.Config) { c.HTTP = &http.Client{Transport: rt} })
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	// GitHub refuses the merge, and the read after it is not settled.
	e.world.Update(pr, func(p *gt.PR) { p.MergeableState = "blocked" })
	rt.fn = func() { e.world.Update(pr, func(p *gt.PR) { p.MergeableState = merge.MergeableUnknown }) }
	e.tick()
	e.wantStatus(id, machine.Queued)
	e.wantTracked(pr, "handed", "", 1)
	if n := len(e.world.Merges()); n != 1 {
		t.Fatalf("%d merge calls; want 1", n)
	}
}

// afterMerge runs fn after each merge request.
type afterMerge struct{ fn func() }

func (a *afterMerge) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := http.DefaultTransport.RoundTrip(r)
	if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/merge") && a.fn != nil {
		a.fn()
	}
	return res, err
}

// slowMerges holds each merge request open for a while and records how
// many were open at one time.
type slowMerges struct {
	mu        sync.Mutex
	open, max int
}

func (s *slowMerges) RoundTrip(r *http.Request) (*http.Response, error) {
	merging := r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/merge")
	if merging {
		s.mu.Lock()
		s.open++
		s.max = max(s.max, s.open)
		s.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		defer func() { s.mu.Lock(); s.open--; s.mu.Unlock() }()
	}
	return http.DefaultTransport.RoundTrip(r)
}

// Two eligible tickets on one project merge one at a time, even when two
// ticks overlap.
func TestDirectMergesOneAtATimeForEachProject(t *testing.T) {
	rt := &slowMerges{}
	e := newDirect(t, func(c *github.Config) { c.HTTP = &http.Client{Transport: rt} })
	a, prA := e.reviewed(sha('1'), true)
	b, prB := e.reviewed(sha('2'), true)
	e.tick()
	e.wantStatus(a, machine.Queued)
	e.wantStatus(b, machine.Queued)
	var wg sync.WaitGroup
	for _, x := range []struct {
		id string
		pr *gt.PR
	}{{a, prA}, {b, prB}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.mech.Poll(e.ctx, merge.PR{Project: e.project, Item: x.id, Number: x.pr.Number, Head: x.pr.HeadSHA, Base: "main"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if rt.max != 1 {
		t.Fatalf("%d merges were in flight at once; want 1", rt.max)
	}
	if n := len(e.world.Merges()); n != 2 {
		t.Fatalf("%d merge calls; want 2", n)
	}
	e.tick()
	e.wantStatus(a, machine.Done)
	e.wantStatus(b, machine.Done)
}

// With no ruleset to stop it, the direct merge reads CI on the reviewed
// head first: pending waits, and red dequeues with no merge call.
func TestDirectChecksCIBeforeItMerges(t *testing.T) {
	e := newDirect(t)
	id, pr := e.reviewed(sha('1'), true)
	poll := func() merge.Result {
		r, err := e.mech.Poll(e.ctx, merge.PR{Project: e.project, Item: id, Number: pr.Number, Head: sha('1'), Base: "main"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	e.world.SetRuns(sha('1'), map[string]any{"id": 1, "name": "ci", "head_sha": sha('1'), "status": "in_progress"})
	if r := poll(); r.Outcome != merge.Pending {
		t.Fatalf("poll with CI running = %+v", r)
	}
	e.world.SetRuns(sha('1'), map[string]any{"id": 2, "name": "ci", "head_sha": sha('1'), "status": "completed", "conclusion": "failure"})
	if r := poll(); r.Outcome != merge.Dequeued || r.Reason != merge.Red {
		t.Fatalf("poll with red CI = %+v", r)
	}
	if got := e.world.Merges(); len(got) != 0 {
		t.Fatalf("merge calls = %+v; want none", got)
	}
}
