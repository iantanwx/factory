package merge_test

// Parity cases from the alpha suites of the merge gate and the land
// queue (scripts/sdlc/premerge.test.ts, premerge-gate.test.ts,
// land-queue.test.ts and land-queue-retry.test.ts), in their new form.
// Each test names its source. The hand-off job is the new form of both
// premerge and land: it hands a PR to the mechanism only at the head a
// review passed, with loop/eligible posted on that head.
//
// Cases kept elsewhere: a conflicting PR (TestContract), a dequeue that
// re-queues once for each head and then escalates, with the queue command
// right after the label (TestMergifyRequeuesOncePerHeadThenEscalates), a
// source push after the hand-off (TestContract), and the REST pages of a
// PR's files (internal/github). Cases that read Linear, an orca branch's
// attachment, ci.yml's job names or a scope upload have no counterpart:
// the machine is the ticket's record, the item branch names the item, and
// GitHub's branch rules hold the required checks.

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/fake"
	"github.com/iantanwx/loop/internal/merge/mergify"
)

// land-queue.test.ts: a reviewed stale PR enters Mergify once and waits
// without a local rebase or merge. New form: a PR behind its base is
// handed off once, a quiet tick writes nothing, and the merge GitHub
// reports finishes the ticket.
func TestParityStalePREntersOnceAndWaitsWithoutABranchUpdate(t *testing.T) {
	e := newMergify(t)
	id, pr := e.reviewed(sha('1'), true)
	e.world.Update(pr, func(p *gt.PR) { p.MergeableState = merge.MergeableBehind })
	e.tick()
	e.wantStatus(id, machine.Queued)
	first := e.mergeCalls()
	if len(first) == 0 || !has(e.world.Labels(pr.Number), mergify.QueueLabel) {
		t.Fatalf("the stale PR was not handed off: calls %v, labels %v", first, e.world.Labels(pr.Number))
	}
	e.tick()
	if again := e.mergeCalls(); !slices.Equal(again, first) {
		t.Fatalf("a quiet tick wrote %v after %v", again[len(first):], first)
	}
	for _, c := range e.api.Calls() {
		if strings.Contains(c, "update-branch") || strings.Contains(c, "/merge") || strings.Contains(c, "/git/refs") {
			t.Fatalf("the hand-off updated or merged the branch itself: %s", c)
		}
	}
	ghMerge(e, pr)
	e.tick()
	e.wantStatus(id, machine.Done)
}

// land-queue.test.ts: no premerge or land source rebases, force-pushes or
// updates a branch. The mechanism merges onto the current base, and a
// conflict goes back to the implementer, who merges the base into the
// branch. New form: no source of the merge package or its mechanisms
// names a rebase, a branch update, a force or a push.
func TestParityNoMergeSourceRewritesABranch(t *testing.T) {
	banned := []*regexp.Regexp{
		regexp.MustCompile(`(?i)rebase`), regexp.MustCompile(`update-branch`),
		regexp.MustCompile(`--force`), regexp.MustCompile(`"force"`), regexp.MustCompile(`"push"`),
	}
	var read []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		read = append(read, filepath.ToSlash(path))
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Only the code: a comment may say what the job never does.
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ParseComments)
		if err != nil {
			return err
		}
		code := slices.Clone(src)
		for _, g := range f.Comments {
			for i := g.Pos() - 1; i < g.End()-1; i++ {
				if code[i] != '\n' {
					code[i] = ' '
				}
			}
		}
		for _, re := range banned {
			if loc := re.FindIndex(code); loc != nil {
				line := 1 + strings.Count(string(code[:loc[0]]), "\n")
				t.Errorf("%s:%d holds %s", path, line, re)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"handoff.go", "direct/direct.go", "ghqueue/ghqueue.go", "mergify/mergify.go"} {
		if !slices.Contains(read, want) {
			t.Fatalf("the scan read %v, and not %s", read, want)
		}
	}
}

// premerge.test.ts: a draft PR is refused. New form: the job never hands
// off a draft PR, whatever the mechanism. The ticket waits in
// ready_to_merge, and the PR is handed off once it is ready for review.
func TestParityADraftPRIsNeverHandedOff(t *testing.T) {
	for _, d := range drivers() {
		t.Run(d.name, func(t *testing.T) {
			e := newEnv(t, d.mech)
			id, pr := e.reviewed(sha('1'), true)
			e.world.Update(pr, func(p *gt.PR) { p.Draft = true })
			e.tick()
			e.tick()
			e.wantStatus(id, machine.ReadyToMerge)
			if calls := e.mergeCalls(); len(calls) != 0 {
				t.Fatalf("hand-off calls for a draft PR: %v", calls)
			}
			if d.name == "fake" && fk(e).HandedOff(pr.Number) {
				t.Fatal("the fake took a draft PR")
			}
			e.world.Update(pr, func(p *gt.PR) { p.Draft = false })
			e.tick()
			e.wantStatus(id, machine.Queued)
			if !d.handedOff(e, pr) {
				t.Fatalf("%s did not take the PR once it was ready", d.name)
			}
		})
	}
}

// premerge.test.ts: a PR targeting something other than the base is
// refused. New form: the job never hands off a PR whose base is not the
// project's default branch, whatever the mechanism: a merge there is no
// merge of the ticket.
func TestParityAPROnAnotherBaseIsNeverHandedOff(t *testing.T) {
	for _, d := range drivers() {
		t.Run(d.name, func(t *testing.T) {
			e := newEnv(t, d.mech)
			id, pr := e.reviewed(sha('1'), true)
			e.world.Update(pr, func(p *gt.PR) { p.Base = "develop" })
			e.tick()
			e.tick()
			e.wantStatus(id, machine.ReadyToMerge)
			if calls := e.mergeCalls(); len(calls) != 0 {
				t.Fatalf("hand-off calls for a PR on develop: %v", calls)
			}
			if d.name == "fake" && fk(e).HandedOff(pr.Number) {
				t.Fatal("the fake took a PR on develop")
			}
			e.world.Update(pr, func(p *gt.PR) { p.Base = "main" })
			e.tick()
			e.wantStatus(id, machine.Queued)
		})
	}
}

// premerge.test.ts: a PR that is not open is refused. New form: the job
// hands off no closed PR, and leaves the ticket to the reactor, which
// escalates it (R8).
func TestParityAClosedPRIsNeverHandedOff(t *testing.T) {
	e := newEnv(t, func(*env) merge.Mechanism { return fake.New() })
	id, pr := e.reviewed(sha('1'), true)
	e.world.Update(pr, func(p *gt.PR) { p.State = "closed" })
	e.tick()
	e.wantStatus(id, machine.ReadyToMerge)
	if fk(e).HandedOff(pr.Number) {
		t.Fatal("the fake took a closed PR")
	}
}

// land-queue.test.ts: queued eligibility is revoked when the current
// ticket or review refuses it. New form: a queued ticket that leaves
// queued by any move, here an escalation, has its PR withdrawn on the next
// tick, and the PR leaves the pipeline.
func TestParityQueuedPRIsWithdrawnWhenItsTicketLeavesQueued(t *testing.T) {
	e := newEnv(t, func(*env) merge.Mechanism { return fake.New() })
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	e.wantStatus(id, machine.Queued)
	e.apply(id, "escalate", map[string]string{"reason": "review withdrawn"})
	e.tick()
	if fk(e).HandedOff(pr.Number) || !fk(e).Withdrawn(pr.Number) {
		t.Fatal("the fake still holds the PR of an escalated ticket")
	}
	e.wantTracked(pr, "withdrawn", "", 1)
}

// land-queue.test.ts: a failed revocation is explicit, and never reports
// a completed refusal. New form: a withdrawal the mechanism refuses fails
// the tick, the PR stays in the pipeline, and the next tick withdraws it.
func TestParityAFailedWithdrawalIsExplicit(t *testing.T) {
	e := newEnv(t, func(*env) merge.Mechanism { return fake.New() })
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	e.apply(id, "escalate", map[string]string{"reason": "review withdrawn"})
	errWithdraw := errors.New("GitHub could not revoke eligibility")
	fk(e).WithdrawErr = errWithdraw
	if err := e.job.TickProject(e.ctx, testProject); !errors.Is(err, errWithdraw) {
		t.Fatalf("tick = %v; want the failed withdrawal", err)
	}
	if !fk(e).HandedOff(pr.Number) {
		t.Fatal("the fake let go of the PR")
	}
	e.wantTracked(pr, "handed", "", 1)
	fk(e).WithdrawErr = nil
	e.tick()
	e.wantTracked(pr, "withdrawn", "", 1)
}
