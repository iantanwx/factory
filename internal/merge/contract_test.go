package merge_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/direct"
	"github.com/iantanwx/loop/internal/merge/fake"
	"github.com/iantanwx/loop/internal/merge/ghqueue"
	"github.com/iantanwx/loop/internal/merge/mergify"
)

// driver plays one mechanism's side of the world: what the mechanism or
// GitHub does to a handed-off PR, and what the test reads back.
type driver struct {
	name string
	mech func(e *env) merge.Mechanism
	// conflict, red and merge make the mechanism report that outcome on
	// its next read. merge is nil when the job itself merges (direct).
	conflict  func(e *env, pr *gt.PR)
	red       func(e *env, pr *gt.PR)
	merge     func(e *env, pr *gt.PR)
	handedOff func(e *env, pr *gt.PR) bool
	withdrawn func(e *env, pr *gt.PR) bool
}

func ghMerge(e *env, pr *gt.PR) {
	e.world.Update(pr, func(p *gt.PR) { p.Merged, p.State, p.MergeSHA = true, "closed", gt.MergeSHA(p.Number) })
}

func drivers() []driver {
	return []driver{
		{
			name: "fake",
			mech: func(*env) merge.Mechanism { return fake.New() },
			conflict: func(e *env, pr *gt.PR) {
				fk(e).Set(pr.Number, merge.Result{Outcome: merge.Dequeued, Reason: merge.Conflict})
			},
			red: func(e *env, pr *gt.PR) {
				fk(e).Set(pr.Number, merge.Result{Outcome: merge.Dequeued, Reason: merge.Red})
			},
			merge: func(e *env, pr *gt.PR) {
				ghMerge(e, pr)
				fk(e).Set(pr.Number, merge.Result{Outcome: merge.Merged, SHA: gt.MergeSHA(pr.Number)})
			},
			handedOff: func(e *env, pr *gt.PR) bool { return fk(e).HandedOff(pr.Number) },
			withdrawn: func(e *env, pr *gt.PR) bool { return fk(e).Withdrawn(pr.Number) },
		},
		{
			name: "mergify",
			mech: func(e *env) merge.Mechanism { return mergify.New(e.svc) },
			conflict: func(e *env, pr *gt.PR) {
				e.world.Update(pr, func(p *gt.PR) {
					p.Labels, p.MergeableState = append(p.Labels, mergify.DequeuedLabel), "dirty"
				})
			},
			red: func(e *env, pr *gt.PR) {
				e.world.Update(pr, func(p *gt.PR) { p.Labels = append(p.Labels, mergify.DequeuedLabel) })
				e.world.SetRuns(pr.HeadSHA, failing(pr.HeadSHA))
			},
			merge:     ghMerge,
			handedOff: func(e *env, pr *gt.PR) bool { return has(e.world.Labels(pr.Number), mergify.QueueLabel) },
			withdrawn: func(e *env, pr *gt.PR) bool { return !has(e.world.Labels(pr.Number), mergify.QueueLabel) },
		},
		{
			name: "github-queue",
			mech: func(e *env) merge.Mechanism { return ghqueue.New(e.svc) },
			conflict: func(e *env, pr *gt.PR) {
				e.world.Unqueue(pr.Number)
				e.world.Update(pr, func(p *gt.PR) { p.MergeableState = "dirty" })
			},
			red: func(e *env, pr *gt.PR) {
				e.world.Unqueue(pr.Number)
				e.world.SetRuns(pr.HeadSHA, failing(pr.HeadSHA))
			},
			merge: func(e *env, pr *gt.PR) {
				e.world.Unqueue(pr.Number)
				ghMerge(e, pr)
			},
			handedOff: func(e *env, pr *gt.PR) bool { return slices.Contains(e.world.Queue(), pr.Number) },
			withdrawn: func(e *env, pr *gt.PR) bool {
				return !slices.Contains(e.world.Queue(), pr.Number) && has(e.world.Mutations(), fmt.Sprintf("dequeue #%d", pr.Number))
			},
		},
		{
			name:     "direct",
			mech:     func(e *env) merge.Mechanism { return direct.New(e.svc) },
			conflict: func(e *env, pr *gt.PR) { e.world.Update(pr, func(p *gt.PR) { p.MergeableState = "dirty" }) },
			red: func(e *env, pr *gt.PR) {
				e.world.Update(pr, func(p *gt.PR) { p.MergeableState = "blocked" })
				e.world.SetRuns(pr.HeadSHA, failing(pr.HeadSHA))
			},
			// direct holds a PR in the loop's own queue until its next tick.
			handedOff: func(*env, *gt.PR) bool { return true },
			withdrawn: func(e *env, pr *gt.PR) bool {
				return !slices.ContainsFunc(e.world.Merges(), func(c gt.MergeCall) bool { return c.PR == pr.Number })
			},
		},
	}
}

// TestContract runs the shared cases of KTD29 against every mechanism.
func TestContract(t *testing.T) {
	for _, d := range drivers() {
		t.Run(d.name, func(t *testing.T) {
			t.Run("an eligible ticket is handed off and moves to queued", func(t *testing.T) {
				e := newEnv(t, d.mech)
				id, pr := e.reviewed(sha('1'), true)
				waiting, _ := e.reviewed(sha('2'), false) // loop/eligible not posted yet
				e.tick()
				e.wantStatus(id, machine.Queued)
				if !d.handedOff(e, pr) {
					t.Fatalf("%s did not take the PR", d.name)
				}
				e.wantStatus(waiting, machine.ReadyToMerge)
			})

			for _, reactorFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("a push after the hand-off withdraws the PR and returns the ticket to review (reactor first %v)", reactorFirst), func(t *testing.T) {
					e := newEnv(t, d.mech)
					id, pr := e.reviewed(sha('1'), true)
					e.tick()
					e.wantStatus(id, machine.Queued)
					e.world.Update(pr, func(p *gt.PR) { p.HeadSHA = sha('3') })
					if reactorFirst {
						e.reactorTick()
					}
					e.tick()
					it := e.wantStatus(id, machine.InReview)
					if it.Fact(machine.FactReviewedHead) != "" {
						t.Fatalf("reviewed_head = %q; want it cleared", it.Fact(machine.FactReviewedHead))
					}
					if !d.withdrawn(e, pr) {
						t.Fatalf("%s still holds the PR after the push", d.name)
					}
				})
			}

			t.Run("a conflict moves the ticket to todo with entry rework", func(t *testing.T) {
				e := newEnv(t, d.mech)
				id, pr := e.reviewed(sha('1'), true)
				e.tick()
				d.conflict(e, pr)
				e.tick()
				it := e.wantStatus(id, machine.Todo)
				if it.Fact(machine.FactEntry) != machine.EntryRework {
					t.Fatalf("entry = %q; want rework", it.Fact(machine.FactEntry))
				}
			})

			t.Run("a PR that conflicts at the hand-off goes to rework and is never handed off", func(t *testing.T) {
				e := newEnv(t, d.mech)
				id, pr := e.reviewed(sha('1'), true)
				e.world.Update(pr, func(p *gt.PR) { p.MergeableState = merge.MergeableDirty })
				e.tick()
				it := e.wantStatus(id, machine.Todo)
				if it.Fact(machine.FactEntry) != machine.EntryRework {
					t.Fatalf("entry = %q; want rework", it.Fact(machine.FactEntry))
				}
				if calls := e.mergeCalls(); len(calls) != 0 {
					t.Fatalf("hand-off calls for a conflicted PR: %v", calls)
				}
				e.wantTracked(pr, "dequeued", merge.Conflict, 0)
			})

			t.Run("a PR that conflicts while the mechanism holds it goes to rework", func(t *testing.T) {
				if d.name == "fake" {
					t.Skip("the fake reads no GitHub")
				}
				e := newEnv(t, d.mech)
				id, pr := e.reviewed(sha('1'), true)
				e.tick()
				e.wantStatus(id, machine.Queued)
				// The mechanism neither merges nor dequeues the PR: the
				// Mergify template's -conflict condition skips it.
				e.world.Update(pr, func(p *gt.PR) { p.MergeableState = merge.MergeableDirty })
				e.tick()
				e.wantStatus(id, machine.Todo)
				e.wantTracked(pr, "dequeued", merge.Conflict, 1)
			})

			t.Run("an unknown mergeable state waits for the next tick", func(t *testing.T) {
				e := newEnv(t, d.mech)
				id, pr := e.reviewed(sha('1'), true)
				e.world.Update(pr, func(p *gt.PR) { p.MergeableState = merge.MergeableUnknown })
				e.tick()
				e.wantStatus(id, machine.Queued)
				if calls := e.mergeCalls(); len(calls) != 0 {
					t.Fatalf("hand-off calls before GitHub computed the mergeable state: %v", calls)
				}
				e.world.Update(pr, func(p *gt.PR) { p.MergeableState = "clean" })
				e.tick()
				e.wantStatus(id, machine.Queued)
				if !d.handedOff(e, pr) {
					t.Fatalf("%s did not take the PR once GitHub computed its state", d.name)
				}
			})

			t.Run("red CI moves the ticket to repair", func(t *testing.T) {
				e := newEnv(t, d.mech)
				id, pr := e.reviewed(sha('1'), true)
				e.tick()
				d.red(e, pr)
				e.tick()
				e.wantStatus(id, machine.Repair)
			})

			t.Run("an observed merge writes done exactly once when the reactor and the job both see it", func(t *testing.T) {
				e := newEnv(t, d.mech)
				id, pr := e.reviewed(sha('1'), true)
				e.tick()
				if d.merge != nil {
					d.merge(e, pr)
				}
				e.tick()
				e.reactorTick()
				e.tick()
				e.reactorTick()
				it := e.wantStatus(id, machine.Done)
				if n := e.transitions(id, "merged"); n != 1 {
					t.Fatalf("%d merged transitions; want exactly 1", n)
				}
				if it.Fact(machine.FactMergedSHA) != gt.MergeSHA(pr.Number) {
					t.Fatalf("merged_sha = %q", it.Fact(machine.FactMergedSHA))
				}
			})

			t.Run("a document PR is never handed off", func(t *testing.T) {
				e := newEnv(t, d.mech)
				epic, res := e.m.Create(e.ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindEpic, Title: "prd",
					Verb: "prd_submit", Actor: machine.Server("test")})
				e.mustOK(res)
				e.world.Add(&gt.PR{Head: github.BranchFor(epic), HeadSHA: sha('4'), Title: epic})
				e.apply(epic, "review_pass", map[string]string{"head": sha('4')})
				e.postEligibility()
				e.tick()
				e.tick()
				e.wantStatus(epic, machine.PRDSignoff)
				if calls := e.mergeCalls(); len(calls) != 0 {
					t.Fatalf("hand-off calls for a document PR: %v", calls)
				}
				if d.name == "fake" && fk(e).HandedOff(1) {
					t.Fatal("the fake took a document PR")
				}
			})
		})
	}
}

func fk(e *env) *fake.Mechanism { return e.mech.(*fake.Mechanism) }
