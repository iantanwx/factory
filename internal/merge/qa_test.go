package merge_test

import (
	"testing"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/fake"
	"github.com/iantanwx/loop/internal/sched"
)

// C7: a ticket whose review passed with the QA stage on waits in in_qa,
// and one tick of the hand-off gives its pull request to no mechanism. A
// ticket that skipped QA in the same tick is handed off, so the tick ran.
func TestC7NoTicketInQAMerges(t *testing.T) {
	e := newEnv(t, func(*env) merge.Mechanism { return fake.New() })
	inQA, qaPR := e.passed(sha('1'), map[string]string{"head": sha('1'), "qa": "on"})
	ready, readyPR := e.reviewed(sha('2'), true)
	e.postEligibility()
	e.tick()
	e.wantStatus(inQA, machine.InQA)
	if fk(e).HandedOff(qaPR.Number) {
		t.Fatalf("the hand-off gave the pull request of %s in in_qa to the mechanism", inQA)
	}
	e.wantStatus(ready, machine.Queued)
	if !fk(e).HandedOff(readyPR.Number) {
		t.Fatalf("the ticket that skipped QA was not handed off, so the tick read nothing")
	}
}

// C15: a queued ticket that the QA stage turns on for goes back to in_qa
// by the dependency-advance sweep, and the next tick of the hand-off
// withdraws its pull request from the mechanism.
func TestC15TheHandOffWithdrawsATicketTheSweepSendsToQA(t *testing.T) {
	e := newEnv(t, func(*env) merge.Mechanism { return fake.New() })
	id, pr := e.reviewed(sha('1'), true)
	e.tick()
	e.wantStatus(id, machine.Queued)
	if !fk(e).HandedOff(pr.Number) {
		t.Fatalf("the queued ticket %s was not handed off", id)
	}
	if _, errs := sched.FollowQASetting(e.ctx, e.m, testProject, true); len(errs) > 0 {
		t.Fatalf("the sweep with the stage on: %v", errs)
	}
	e.wantStatus(id, machine.InQA)
	e.tick()
	if !fk(e).Withdrawn(pr.Number) {
		t.Fatalf("the hand-off kept the pull request of %s in the mechanism after it left queued", id)
	}
}
