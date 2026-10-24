package overwatch

import (
	"strings"
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// Two heads of the base branch, newest last.
var (
	headOld = strings.Repeat("0", 40)
	headA   = "1a2b3c4d5e6f" + strings.Repeat("7", 28)
)

// C24: the newest completed run of a workflow at the base head that
// concluded failure or timed_out is work, keyed by the project, the
// workflow and the head. cancelled, skipped and neutral read code 0
// (overwatch-main.ts, alpha, SILENT), and so does success.
func TestC24ARedBaseBranchIsWork(t *testing.T) {
	key := "overwatch:main:alpha:ci-yml@1a2b3c4d5e6f"
	for _, c := range []struct {
		conclusion string
		code       Code
	}{
		{"failure", Work}, {"timed_out", Work},
		{"cancelled", Quiet}, {"skipped", Quiet}, {"neutral", Quiet}, {"success", Quiet},
	} {
		t.Run(c.conclusion, func(t *testing.T) {
			w := newGHWorld(t, alpha)
			w.prs().AddRun(&gt.Run{ID: 501, Workflow: "ci.yml", HeadSHA: headA, Conclusion: c.conclusion, CreatedAt: w.now.Add(-time.Hour)})
			pr := sweepOne(t, w, partNamed(t, "main"))
			expect(t, pr, c.code, "ci.yml", c.conclusion)
			if got := unfiled(pr, key); c.code == Work && got != 1 || c.code == Quiet && got != 0 {
				t.Fatalf("%d unfiled lines quote %s: %v", got, key, pr.Lines)
			}
		})
	}
}

// C24: what ends the work, and what does not.
func TestC24WhatEndsARedBaseBranch(t *testing.T) {
	key := "overwatch:main:alpha:ci-yml@1a2b3c4d5e6f"
	t.Run("a later run at the head passed", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "failure", CreatedAt: w.now.Add(-2 * time.Hour)})
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Event: "workflow_dispatch", Conclusion: "success",
			CreatedAt: w.now.Add(-time.Hour)})
		expect(t, sweepOne(t, w, partNamed(t, "main")), Quiet, "success")
	})
	t.Run("a run still going leaves the newest completed run red", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "failure", CreatedAt: w.now.Add(-2 * time.Hour)})
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Status: "in_progress", CreatedAt: w.now.Add(-time.Minute)})
		pr := sweepOne(t, w, partNamed(t, "main"))
		expect(t, pr, Work)
		if unfiled(pr, key) != 1 {
			t.Fatalf("no line quotes %s: %v", key, pr.Lines)
		}
	})
	t.Run("the head moved", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headOld, Conclusion: "failure", CreatedAt: w.now.Add(-2 * time.Hour)})
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "success", CreatedAt: w.now.Add(-time.Hour)})
		expect(t, sweepOne(t, w, partNamed(t, "main")), Quiet)
	})
	t.Run("a new head that is red again is a new key", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headOld, Conclusion: "failure", CreatedAt: w.now.Add(-2 * time.Hour)})
		w.add("alpha", machine.Item{ID: "alpha-b1", Kind: machine.KindBug, Status: machine.Reported},
			"red: overwatch:main:alpha:ci-yml@"+headOld[:12])
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "failure", CreatedAt: w.now.Add(-time.Hour)})
		pr := sweepOne(t, w, partNamed(t, "main"))
		expect(t, pr, Work)
		if unfiled(pr, key) != 1 {
			t.Fatalf("no line quotes %s: %v", key, pr.Lines)
		}
	})
	t.Run("an open bug names the key", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "failure", CreatedAt: w.now.Add(-time.Hour)})
		w.add("alpha", machine.Item{ID: "alpha-b1", Kind: machine.KindBug, Status: machine.Reported}, "main is red: "+key+".")
		expect(t, sweepOne(t, w, partNamed(t, "main")), Quiet, key, "filed in alpha-b1")
	})
}

// C24: each workflow at the head is judged on its own, and runs of
// another branch are not read.
func TestC24EachWorkflowAtTheBaseHead(t *testing.T) {
	w := newGHWorld(t, alpha)
	w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "success", CreatedAt: w.now.Add(-2 * time.Hour)})
	w.prs().AddRun(&gt.Run{Workflow: "release.yml", HeadSHA: headA, Conclusion: "timed_out", CreatedAt: w.now.Add(-time.Hour)})
	w.prs().AddRun(&gt.Run{Workflow: "ci.yml", Branch: "loop/alpha-x1", HeadSHA: strings.Repeat("9", 40), Conclusion: "failure",
		CreatedAt: w.now.Add(-time.Minute)})
	pr := sweepOne(t, w, partNamed(t, "main"))
	expect(t, pr, Work, "ci.yml")
	if unfiled(pr, "overwatch:main:alpha:release-yml@1a2b3c4d5e6f") != 1 {
		t.Fatalf("lines = %v", pr.Lines)
	}
	for _, l := range pr.Lines {
		if strings.Contains(l, "999999") || strings.Contains(l, "overwatch:main:alpha:ci-yml") {
			t.Fatalf("a line reads another branch or a green workflow as work: %q", l)
		}
	}
}

// A base branch with no run yet is no work, and says so in one line.
func TestMainWithNoRunIsQuiet(t *testing.T) {
	w := newGHWorld(t, alpha)
	pr := sweepOne(t, w, partNamed(t, "main"))
	expect(t, pr, Quiet, "alpha", "no workflow run")
	if len(pr.Lines) != 1 {
		t.Fatalf("lines = %v", pr.Lines)
	}
}
