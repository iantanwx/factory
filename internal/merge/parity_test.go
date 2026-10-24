package merge_test

// Parity cases from the alpha suites of the merge gate and the land
// queue, scripts/sdlc/premerge.test.ts and land-queue.test.ts, in their
// new form. The hand-off job is the new form of both premerge and land:
// it hands a PR to the mechanism only at the head a review passed, with
// loop/eligible posted on that head. Each case of the two suites is in
// one place below: a test in this file, which names its source, an
// existing test, or a case with no counterpart and its reason.
// internal/machine/parity_test.go holds the cases that the state machine
// decides. Single cases of premerge-gate.test.ts and
// land-queue-retry.test.ts that it cites are not an account of those
// suites, which are outside this file.
//
// premerge.test.ts:
//   - L29 a clean PR with every required check green is ready: TestContract
//     ("an eligible ticket is handed off and moves to queued") and
//     TestDirectMergesAtTheReviewedHeadWithAMergeCommit.
//   - L35 a missing required check is a refusal. A divergence: the loop
//     holds no list of required checks. Where the plan offers rulesets,
//     GitHub's required status checks hold the list, and GitHub refuses
//     the merge of a PR that lacks one. With no rulesets, the direct
//     merge reads every check on the reviewed head, and it merges when
//     none is red or pending (CheckRules: "the loop checks CI itself"). So
//     a renamed job that never reports does not stop a direct merge, and
//     a ci_required line that fails in loop doctor does not stop
//     `project add` (TestAddHookChecksTheBranchRules, case "nothing").
//   - L45 a failing required check and L58 a required check still
//     running are refused: TestDirectChecksCIBeforeItMerges. Red CI under
//     every mechanism: TestContract ("red CI moves the ticket to repair").
//   - L71 a failing check that is not required does not block. A
//     deliberate divergence: every red check on the head is red CI, and it
//     dequeues the PR (TestDirectChecksCIBeforeItMerges). The loop's rule is
//     that a pass needs every check green at that head (the code-reviewer
//     runbook, section 7).
//   - L80 every REST page of a PR's files: TestPRFilesReadEveryPage in
//     internal/github.
//   - L96 a conflicting PR: TestContract ("a PR that conflicts at the
//     hand-off goes to rework and is never handed off").
//   - L102 a draft PR: TestParityADraftPRIsNeverHandedOff.
//   - L108 a PR on another base: TestParityAPROnAnotherBaseIsNeverHandedOff.
//   - L114 a PR that is not open: TestParityAClosedPRIsNeverHandedOff.
//   - L122 every required check has a job in ci.yml: the required_jobs
//     line of loop doctor, TestDoctorFailsOnARequiredCheckNoWorkflowJobReports
//     in internal/github. The ruleset holds the list of check names, and
//     the line compares it with the jobs of every workflow on the default
//     branch. A plan with no rulesets requires no check by name, so no
//     name can drift there.
//
// land-queue.test.ts:
//   - L8 a stale PR enters once and waits:
//     TestParityStalePREntersOnceAndWaitsWithoutABranchUpdate.
//   - L18 a moved source head never authorizes: machine
//     TestParityOnlyAReviewPassedHeadEntersTheQueue. The failed upload has
//     no counterpart: the loop uploads no scopes.
//   - L34 pending CI waits: TestDirectChecksCIBeforeItMerges. A refusal
//     with no current-head pass: machine
//     TestParityOnlyAReviewPassedHeadEntersTheQueue. A dequeue and a close:
//     machine TestParityQueuedOutcomesStayExplicit.
//   - L56 queued eligibility is revoked:
//     TestParityQueuedPRIsWithdrawnWhenItsTicketLeavesQueued.
//   - L70 a policy reversal during the scope upload. No counterpart: the
//     loop uploads no scopes.
//   - L85 an unknown admission revokes, and L157 a failed policy read
//     revokes a dequeued PR. A divergence: alpha's read was Linear, and a
//     failed read withdrew the PR. The loop's read is its own machine,
//     which writes every move out of queued. When that read fails, the
//     tick fails, the PR stays with the mechanism, and the next tick reads
//     again (withdrawStale). When beads cannot be read for the project,
//     the job lists no queued ticket either, so the direct merge makes no
//     merge call. Mergify and the GitHub queue keep the PR, and can merge
//     it, until a read succeeds.
//   - L102 a failed revocation is explicit: TestParityAFailedWithdrawalIsExplicit.
//   - L114 a queued CI rerun waits:
//     TestParityAQueuedCIRerunWaitsWithoutANewHandOffOrAWithdrawal.
//   - L123 a source push revokes the queued approval: TestContract ("a push
//     after the hand-off withdraws the PR and returns the ticket to
//     review").
//   - L135 a queued observation rechecks the policy and does not repeat the
//     admission. Each tick reads the ticket again and withdraws a PR whose
//     ticket left queued (TestParityQueuedPRIsWithdrawnWhenItsTicketLeavesQueued),
//     and a quiet tick hands nothing off again
//     (TestParityStalePREntersOnceAndWaitsWithoutABranchUpdate).
//   - L143 a delayed dequeue label cannot revoke a valid retry. A
//     divergence: the re-queue posts Mergify's queue command with the
//     label, which clears Mergify's memory of the failed attempt, and the
//     loop reads no label times. So a dequeued label after a re-queue at
//     one head is a second failure, and the ticket escalates
//     (TestMergifyRequeuesOncePerHeadThenEscalates). The PR is never merged
//     without a pass, and a person reads the escalation. A changed policy
//     still revokes after a retry:
//     TestParityAPolicyChangeAfterARetryStillWithdraws.
//   - L174 no source rebases, forces or updates a branch:
//     TestParityNoMergeSourceRewritesABranch.
//   - L192 a dequeue recovery posts the queue command right after the
//     label, and L201 a PR queued again posts no second retry:
//     TestMergifyRequeuesOncePerHeadThenEscalates.

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
	"time"

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

// land-queue.test.ts: a queued CI rerun waits without renewing or
// revoking approval. New form: when CI on the reviewed head of a queued
// ticket runs again, every mechanism waits. The job hands nothing off
// again and withdraws nothing, and the ticket stays queued.
func TestParityAQueuedCIRerunWaitsWithoutANewHandOffOrAWithdrawal(t *testing.T) {
	for _, d := range drivers() {
		t.Run(d.name, func(t *testing.T) {
			e := newEnv(t, d.mech)
			id, pr := e.reviewed(sha('1'), true)
			e.world.SetRuns(pr.HeadSHA, gt.CheckRun("ci", pr.HeadSHA, "in_progress", "", time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)))
			e.tick()
			e.wantStatus(id, machine.Queued)
			first := handOffWrites(e)
			e.world.SetRuns(pr.HeadSHA, gt.CheckRun("ci", pr.HeadSHA, "in_progress", "", time.Date(2026, 10, 21, 11, 0, 0, 0, time.UTC)))
			e.tick()
			e.tick()
			e.wantStatus(id, machine.Queued)
			if again := handOffWrites(e); !slices.Equal(again, first) {
				t.Fatalf("a CI rerun wrote %v after %v", again[len(first):], first)
			}
			if !d.handedOff(e, pr) {
				t.Fatalf("%s let go of the PR while CI ran again", d.name)
			}
			e.wantTracked(pr, "handed", "", 1)
		})
	}
}

// handOffWrites lists what a hand-off writes: label, comment and merge
// calls, and GraphQL mutations. A GraphQL query reads, and is left out.
func handOffWrites(e *env) []string {
	out := slices.DeleteFunc(e.mergeCalls(), func(c string) bool { return strings.Contains(c, "/graphql") })
	return append(out, e.world.Mutations()...)
}

// land-queue.test.ts: a delayed dequeue label cannot revoke a valid
// retry, but changed policy still revokes. New form, the second half: a
// PR that Mergify holds again after a re-queue is withdrawn once its
// ticket leaves queued, as a PR handed off once is. The first half has no
// counterpart, as the header states.
func TestParityAPolicyChangeAfterARetryStillWithdraws(t *testing.T) {
	e := newMergify(t)
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	e.world.Update(pr, func(p *gt.PR) {
		p.Labels = slices.DeleteFunc(p.Labels, func(l string) bool { return l == mergify.QueueLabel })
		p.Labels = append(p.Labels, mergify.DequeuedLabel)
	})
	e.tick()
	e.wantTracked(pr, "requeued", merge.Other, 2)
	if !has(e.world.Labels(pr.Number), mergify.QueueLabel) {
		t.Fatalf("the PR was not queued again: labels %v", e.world.Labels(pr.Number))
	}
	e.apply(id, "escalate", map[string]string{"reason": "review withdrawn"})
	e.tick()
	if has(e.world.Labels(pr.Number), mergify.QueueLabel) {
		t.Fatalf("Mergify still holds the PR of an escalated ticket: labels %v", e.world.Labels(pr.Number))
	}
	e.wantTracked(pr, "withdrawn", merge.Other, 2)
}
