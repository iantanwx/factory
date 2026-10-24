package overwatch

import (
	"strings"
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// C25: in a project whose config names health.audit_workflow, the newest
// base-branch run of that workflow is work when it failed, or when it is
// older than 36 hours (STALE_HOURS, overwatch-audit.ts). The key names
// the project, the workflow and that run's id.
func TestC25AFailedOrStaleAuditIsWork(t *testing.T) {
	key := "overwatch:audit:alpha:audit-yml@9101"
	for _, c := range []struct {
		name       string
		age        time.Duration
		conclusion string
		code       Code
		says       string
	}{
		{"a pass 35 hours old", 35 * time.Hour, "success", Quiet, "passed"},
		{"a pass 37 hours old", 37 * time.Hour, "success", Work, "36h"},
		{"a failure an hour old", time.Hour, "failure", Work, "failure"},
		{"a time-out an hour old", time.Hour, "timed_out", Work, "timed_out"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newGHWorld(t, alpha)
			w.auditWorkflow("alpha", "audit.yml")
			w.prs().AddRun(&gt.Run{ID: 9101, Workflow: "audit.yml", Event: "schedule", HeadSHA: headA, Conclusion: c.conclusion,
				CreatedAt: w.now.Add(-c.age)})
			w.prs().AddRun(&gt.Run{ID: 9102, Workflow: "ci.yml", HeadSHA: headA, Conclusion: "success", CreatedAt: w.now.Add(-time.Minute)})
			pr := sweepOne(t, w, partNamed(t, "audit"))
			expect(t, pr, c.code, "audit.yml", c.says)
			if got := unfiled(pr, key); c.code == Work && got != 1 || c.code == Quiet && got != 0 {
				t.Fatalf("%d unfiled lines quote %s: %v", got, key, pr.Lines)
			}
		})
	}
}

// C25: what ends the work, and what does not.
func TestC25WhatEndsAFailedAudit(t *testing.T) {
	t.Run("an open bug names the key", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.auditWorkflow("alpha", "audit.yml")
		w.prs().AddRun(&gt.Run{ID: 9101, Workflow: "audit.yml", HeadSHA: headA, Conclusion: "failure", CreatedAt: w.now.Add(-time.Hour)})
		w.add("alpha", machine.Item{ID: "alpha-b2", Kind: machine.KindBug, Status: machine.Reported},
			"advisories (overwatch:audit:alpha:audit-yml@9101)")
		expect(t, sweepOne(t, w, partNamed(t, "audit")), Quiet, "overwatch:audit:alpha:audit-yml@9101", "filed in alpha-b2")
	})
	t.Run("a newer run passed", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.auditWorkflow("alpha", "audit.yml")
		w.prs().AddRun(&gt.Run{ID: 9101, Workflow: "audit.yml", HeadSHA: headA, Conclusion: "failure", CreatedAt: w.now.Add(-25 * time.Hour)})
		w.prs().AddRun(&gt.Run{ID: 9102, Workflow: "audit.yml", HeadSHA: headA, Conclusion: "success", CreatedAt: w.now.Add(-time.Hour)})
		expect(t, sweepOne(t, w, partNamed(t, "audit")), Quiet, "9102", "passed")
	})
	t.Run("a newer run that fails is a new key", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.auditWorkflow("alpha", "audit.yml")
		w.prs().AddRun(&gt.Run{ID: 9101, Workflow: "audit.yml", HeadSHA: headA, Conclusion: "failure", CreatedAt: w.now.Add(-25 * time.Hour)})
		w.add("alpha", machine.Item{ID: "alpha-b2", Kind: machine.KindBug, Status: machine.Reported}, "overwatch:audit:alpha:audit-yml@9101")
		w.prs().AddRun(&gt.Run{ID: 9102, Workflow: "audit.yml", HeadSHA: headA, Conclusion: "failure", CreatedAt: w.now.Add(-time.Hour)})
		pr := sweepOne(t, w, partNamed(t, "audit"))
		expect(t, pr, Work)
		if unfiled(pr, "overwatch:audit:alpha:audit-yml@9102") != 1 {
			t.Fatalf("lines = %v", pr.Lines)
		}
	})
	t.Run("a run of another branch is not the audit", func(t *testing.T) {
		w := newGHWorld(t, alpha)
		w.auditWorkflow("alpha", "audit.yml")
		w.prs().AddRun(&gt.Run{ID: 9101, Workflow: "audit.yml", HeadSHA: headA, Conclusion: "success", CreatedAt: w.now.Add(-time.Hour)})
		w.prs().AddRun(&gt.Run{ID: 9102, Workflow: "audit.yml", Branch: "loop/alpha-x1", HeadSHA: headOld, Conclusion: "failure",
			CreatedAt: w.now.Add(-time.Minute)})
		expect(t, sweepOne(t, w, partNamed(t, "audit")), Quiet, "9101")
	})
}

// C25 states work for a newest run that failed or is stale. A named
// workflow with no completed base-branch run has no such run, so it reads
// code 0 with one line, and no key.
func TestC25AnAuditWithNoCompletedRunIsQuiet(t *testing.T) {
	w := newGHWorld(t, alpha)
	w.auditWorkflow("alpha", "audit.yml")
	w.prs().AddRun(&gt.Run{ID: 9101, Workflow: "audit.yml", HeadSHA: headA, Status: "in_progress", CreatedAt: w.now.Add(-time.Minute)})
	pr := sweepOne(t, w, partNamed(t, "audit"))
	expect(t, pr, Quiet, "alpha", "audit.yml", "no completed run")
	if len(pr.Lines) != 1 {
		t.Fatalf("lines = %v", pr.Lines)
	}
	if strings.Contains(pr.Lines[0], "overwatch:audit:") {
		t.Fatalf("a workflow with no completed run holds a key: %q", pr.Lines[0])
	}
}

// C25: a project that names no audit workflow reads code 0, with one line
// that says so, and makes no GitHub read.
func TestC25NoAuditWorkflowIsQuiet(t *testing.T) {
	w := newGHWorld(t, alpha)
	w.auditWorkflow("alpha", "")
	pr := sweepOne(t, w, partNamed(t, "audit"))
	expect(t, pr, Quiet, "alpha", "health.audit_workflow")
	if len(pr.Lines) != 1 {
		t.Fatalf("lines = %v", pr.Lines)
	}
	for _, c := range w.api.Calls() {
		if strings.Contains(c, "/actions/") {
			t.Fatalf("a project with no audit workflow read %s", c)
		}
	}
}

// C26: a 404 for the workflow that health.audit_workflow names is code 2,
// and the line names the workflow. So is a project config that cannot be
// read.
func TestC26AnAuditWorkflowGitHubDoesNotKnowIsUnread(t *testing.T) {
	w := newGHWorld(t, alpha)
	w.auditWorkflow("alpha", "nightly-audit.yml")
	w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "success", CreatedAt: w.now.Add(-time.Hour)})
	pr := sweepOne(t, w, partNamed(t, "audit"))
	expect(t, pr, Unread, "UNREAD", "nightly-audit.yml", "404")

	w = newGHWorld(t, alpha)
	pr = sweepOne(t, w, partNamed(t, "audit"))
	expect(t, pr, Unread, "UNREAD", "alpha", "config")
}
