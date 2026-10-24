package overwatch

import (
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// rerun is a ci.yml run at head whose job "Go tests (race)" concluded
// first in attempt 1 and then in attempt 2. The job lint passed in both.
func rerun(head, first, then string, at time.Time) *gt.Run {
	return &gt.Run{Workflow: "ci.yml", HeadSHA: head, Attempt: 2, Conclusion: then, CreatedAt: at, Jobs: [][]gt.Job{
		{{Name: "lint", Conclusion: "success"}, {Name: "Go tests (race)", Conclusion: first}},
		{{Name: "lint", Conclusion: "success"}, {Name: "Go tests (race)", Conclusion: then}},
	}}
}

// C28: a job that failed in one attempt of a run and passed in a later
// attempt at the same head, inside the 48-hour window, is work. The line
// names the workflow, the job and the head. The key names the project, the
// workflow and the job, each a slug, so a job name with spaces is safe.
// A job that failed and never passed reads code 0, because main owns it.
func TestC28ATestThatFailedAndPassedAtOneCommitIsWork(t *testing.T) {
	key := "overwatch:flaky:alpha:ci-yml/go-tests-race"
	for _, c := range []struct {
		name  string
		first string
		then  string
		age   time.Duration
		code  Code
	}{
		{"a rerun that passed", "failure", "success", time.Hour, Work},
		{"a rerun after a time-out that passed", "timed_out", "success", time.Hour, Work},
		{"a rerun that failed again", "failure", "failure", time.Hour, Quiet},
		{"a rerun that passed outside the window", "failure", "success", 49 * time.Hour, Quiet},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newGHWorld(t, alpha)
			w.prs().AddRun(rerun(headA, c.first, c.then, w.now.Add(-c.age)))
			pr := sweepOne(t, w, partNamed(t, "flaky"))
			if c.code == Work {
				expect(t, pr, Work, "ci.yml", "Go tests (race)", headA[:12])
			} else {
				expect(t, pr, Quiet)
			}
			if got := unfiled(pr, key); c.code == Work && got != 1 || c.code == Quiet && got != 0 {
				t.Fatalf("%d unfiled lines quote %s: %v", got, key, pr.Lines)
			}
		})
	}
}

// C28: what ends the work, and what keeps one key.
func TestC28WhatEndsAFlakyJob(t *testing.T) {
	key := "overwatch:flaky:alpha:ci-yml/go-tests-race"
	t.Run("an open bug names the key", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.prs().AddRun(rerun(headA, "failure", "success", w.now.Add(-time.Hour)))
		w.add("alpha", machine.Item{ID: "alpha-b4", Kind: machine.KindBug, Status: machine.Reported},
			"flaky race tests ("+key+")")
		expect(t, sweepOne(t, w, partNamed(t, "flaky")), Quiet, key, "filed in alpha-b4")
	})
	t.Run("a later flake at a new head keeps the one key", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.prs().AddRun(rerun(headOld, "failure", "success", w.now.Add(-3*time.Hour)))
		w.prs().AddRun(rerun(headA, "failure", "success", w.now.Add(-time.Hour)))
		pr := sweepOne(t, w, partNamed(t, "flaky"))
		expect(t, pr, Work, headA[:12])
		if got := unfiled(pr, key); got != 1 {
			t.Fatalf("%d unfiled lines quote %s, want 1: %v", got, key, pr.Lines)
		}
	})
}
