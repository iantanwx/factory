package overwatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
)

// ciStallsWorld is the stalls world with GitHub reads: one open pull
// request and the workflow runs at its head.
type ciStallsWorld struct {
	*stallsWorld
	gh *ciReads
}

func (w *ciStallsWorld) GitHub() GitHubReads { return w.gh }

type ciReads struct {
	pulls []github.PR
	runs  []github.RunView
}

func (r *ciReads) OpenPulls(context.Context, config.Project) ([]github.PR, error) {
	return r.pulls, nil
}
func (r *ciReads) WorkflowRuns(_ context.Context, _ config.Project, q github.RunQuery) ([]github.RunView, error) {
	var out []github.RunView
	for _, run := range r.runs {
		if (q.HeadSHA == "" || run.HeadSHA == q.HeadSHA) && (q.Branch == "" || run.Branch == q.Branch) && (q.Event == "" || run.Event == q.Event) {
			out = append(out, run)
		}
	}
	return out, nil
}
func (r *ciReads) RunJobs(context.Context, config.Project, int64, int) ([]github.JobView, error) {
	return nil, errFake
}

// C-bug-1 (factorio-mze8): admission refuses the code review of a ticket
// whose head is not its simplify_from until CI at that head is green
// (rule simplify_head_ci). A fire that admission refused so, while CI at
// the head runs inside the CI budget, does not count. Once CI there is
// red, or runs past the budget, the fires count. factorio-t1 entered
// in_review at 08:58 with simplify_from 79ba1763 and the head aa2a0097
// that its simplify run left. CI at aa2a0097 started at 08:59. The
// code-reviewer fires at 09:00 and 09:05 started no run. The clock reads
// 09:05, and the CI budget is 10 minutes.
func TestC10AFireThatWaitsForCIAtTheSimplifyHeadIsNoStall(t *testing.T) {
	cases := []struct {
		name               string
		status, conclusion string
		started            time.Time
		work               bool
	}{
		{"CI runs inside the budget", "in_progress", "", at(8, 59), false},
		{"CI is red", "completed", "failure", at(8, 59), true},
		{"CI runs past the budget", "in_progress", "", at(8, 50), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sw := newStallsWorld(t)
			sw.configs["factorio"].Health.CIBudget = 10 * time.Minute
			sw.add("factorio", machine.Item{ID: "factorio-t1", Kind: machine.KindTicket, Status: machine.InReview, Updated: at(8, 58),
				Facts: map[string]string{machine.FactSimplifyFrom: "79ba1763"}}, "")
			sw.entered(t, "factorio-t1", machine.InProgress, machine.InReview, at(8, 58))
			pr := github.PR{Number: 372, State: "open"}
			pr.Head.Ref, pr.Head.SHA = github.BranchFor("factorio-t1"), "aa2a0097"
			w := &ciStallsWorld{stallsWorld: sw, gh: &ciReads{
				pulls: []github.PR{pr},
				runs: []github.RunView{{ID: 41449767922, Workflow: "ci.yml", Event: "pull_request", Branch: pr.Head.Ref, HeadSHA: "aa2a0097",
					Attempt: 1, Status: c.status, Conclusion: c.conclusion, CreatedAt: c.started, StartedAt: c.started, UpdatedAt: at(9, 4)}},
			}}
			r := readStallsPart(t, w)
			if f, ok := findingOf(r, "overwatch:stalls:factorio-t1@in_review"); ok != c.work {
				t.Fatalf("work = %v, want %v: %q %+v", ok, c.work, f.Line, r)
			}
			if !c.work {
				mustContain(t, noteOf(t, r, "factorio-t1"), "aa2a0097")
			}
		})
	}
}

// failingRuns is ciReads whose workflow run read fails.
type failingRuns struct{ ciReads }

func (*failingRuns) WorkflowRuns(context.Context, config.Project, github.RunQuery) ([]github.RunView, error) {
	return nil, errFake
}

// C-bug-1: a CI read that fails proves no wait, so the fires count, and an
// UNREAD note names the item and the head.
func TestC10AFireCountsWhenCIAtTheSimplifyHeadDoesNotRead(t *testing.T) {
	sw := newStallsWorld(t)
	sw.configs["factorio"].Health.CIBudget = 10 * time.Minute
	sw.add("factorio", machine.Item{ID: "factorio-t1", Kind: machine.KindTicket, Status: machine.InReview, Updated: at(8, 58),
		Facts: map[string]string{machine.FactSimplifyFrom: "79ba1763"}}, "")
	sw.entered(t, "factorio-t1", machine.InProgress, machine.InReview, at(8, 58))
	pr := github.PR{Number: 372, State: "open"}
	pr.Head.Ref, pr.Head.SHA = github.BranchFor("factorio-t1"), "aa2a0097"
	r := readStallsPart(t, &failingWorld{stallsWorld: sw, gh: &failingRuns{ciReads{pulls: []github.PR{pr}}}})
	if _, ok := findingOf(r, "overwatch:stalls:factorio-t1@in_review"); !ok {
		t.Fatalf("no stalls finding on factorio-t1: %+v", r)
	}
	note := noteOf(t, r, "factorio-t1")
	if !strings.HasPrefix(note, "UNREAD") {
		t.Fatalf("note = %q, want an UNREAD note", note)
	}
	mustContain(t, note, "aa2a0097")
}

// failingWorld is the stalls world whose CI read fails.
type failingWorld struct {
	*stallsWorld
	gh *failingRuns
}

func (w *failingWorld) GitHub() GitHubReads { return w.gh }
