package overwatch

import (
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// ciBudget sets a project's health.ci_budget.
func (w *ghWorld) ciBudget(project string, d time.Duration) {
	w.configs[project] = &config.ProjectSnapshot{Config: &config.ProjectConfig{Health: config.HealthConfig{CIBudget: d}}}
}

// C27: in a project whose config names health.ci_budget, the newest
// completed run of the base branch, or of the merge queue, that took
// longer than the budget is work. A run at the budget fits it. The key
// names the project and the event.
func TestC27CIOverItsBudgetIsWork(t *testing.T) {
	for _, c := range []struct {
		name string
		run  gt.Run
		code Code
		key  string
		says string
	}{
		{"a push run at the budget", gt.Run{Took: 5 * time.Minute}, Quiet, "overwatch:ci:alpha:push", "5m0s"},
		{"a push run one second over", gt.Run{Took: 5*time.Minute + time.Second}, Work, "overwatch:ci:alpha:push", "5m1s"},
		{"a merge-queue run one second over", gt.Run{Event: "merge_group", Branch: "gh-readonly-queue/main/pr-4-" + headA,
			Took: 5*time.Minute + time.Second}, Work, "overwatch:ci:alpha:merge-group", "merge_group"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newGHWorld(t, alpha)
			w.ciBudget("alpha", 5*time.Minute)
			run := c.run
			run.Workflow, run.HeadSHA, run.Conclusion, run.CreatedAt = "ci.yml", headA, "success", w.now.Add(-time.Hour)
			w.prs().AddRun(&run)
			pr := sweepOne(t, w, partNamed(t, "ci"))
			expect(t, pr, c.code, c.says, "budget")
			if got := unfiled(pr, c.key); c.code == Work && got != 1 || c.code == Quiet && got != 0 {
				t.Fatalf("%d unfiled lines quote %s: %v", got, c.key, pr.Lines)
			}
		})
	}
}

// C27: what ends the work, and what reads no budget at all.
func TestC27WhatEndsCIOverItsBudget(t *testing.T) {
	t.Run("an open bug names the key", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.ciBudget("alpha", 5*time.Minute)
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "success", Took: 9 * time.Minute, CreatedAt: w.now.Add(-time.Hour)})
		w.add("alpha", machine.Item{ID: "alpha-b3", Kind: machine.KindBug, Status: machine.Reported},
			"slow CI (overwatch:ci:alpha:push)")
		expect(t, sweepOne(t, w, partNamed(t, "ci")), Quiet, "overwatch:ci:alpha:push", "filed in alpha-b3")
	})
	t.Run("the newest run fits the budget", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.ciBudget("alpha", 5*time.Minute)
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headOld, Conclusion: "success", Took: 9 * time.Minute, CreatedAt: w.now.Add(-2 * time.Hour)})
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "success", Took: 4 * time.Minute, CreatedAt: w.now.Add(-time.Hour)})
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Status: "in_progress", CreatedAt: w.now.Add(-time.Minute)})
		pr := sweepOne(t, w, partNamed(t, "ci"))
		expect(t, pr, Quiet, "4m0s")
	})
	t.Run("a run of another branch is not the base branch's", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.ciBudget("alpha", 5*time.Minute)
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", Branch: "loop/alpha-x1", HeadSHA: headA, Conclusion: "success", Took: 9 * time.Minute,
			CreatedAt: w.now.Add(-time.Hour)})
		expect(t, sweepOne(t, w, partNamed(t, "ci")), Quiet, "no completed push run")
	})
	t.Run("no budget", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.configs["alpha"] = &config.ProjectSnapshot{Config: &config.ProjectConfig{}}
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "success", Took: time.Hour, CreatedAt: w.now.Add(-time.Hour)})
		pr := sweepOne(t, w, partNamed(t, "ci"))
		expect(t, pr, Quiet, "names no health.ci_budget", "median 1h0m0s", "90th percentile 1h0m0s")
	})
}

// C27: the lines give the median and the 90th percentile of the 48-hour
// window's completed runs, by nearest rank. A run outside the window and a
// cancelled run measure nothing.
func TestC27TheLinesGiveTheWindowsMedianAndNinetiethPercentile(t *testing.T) {
	w := newGHWorld(t, alpha)
	w.ciBudget("alpha", time.Hour)
	for i := 1; i <= 10; i++ {
		w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "success", Took: time.Duration(i) * time.Minute,
			CreatedAt: w.now.Add(-time.Duration(20-i) * time.Hour)})
	}
	w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headOld, Conclusion: "success", Took: 50 * time.Minute, CreatedAt: w.now.Add(-49 * time.Hour)})
	w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "cancelled", Took: time.Second, CreatedAt: w.now.Add(-time.Minute)})
	expect(t, sweepOne(t, w, partNamed(t, "ci")), Quiet, "10m0s", "median 5m0s", "90th percentile 9m0s", "10 runs")
}
