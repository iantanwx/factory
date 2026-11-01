package github

import (
	"context"
	"slices"
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// qaRule is a required loop/qa from the loop App.
var qaRule = map[string]any{"context": QAContext, "integration_id": testAppID}

// TestC30APassWithGreenCIAndNoLoopQAIsNotRefused: in a project whose
// ruleset requires loop/qa, a review pass needs green CI and never loop/qa,
// which only the QA stage posts after the pass.
func TestC30APassWithGreenCIAndNoLoopQAIsNotRefused(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	e.api.SetRules(requires("ci", qaRule))
	w.SetRuns(shaA, gt.CheckRun("ci", shaA, "completed", "success", time.Now()))
	if r := e.svc.PassCI(context.Background(), testProject, "tk-1", shaA); r != nil {
		t.Fatalf("a pass with green CI and no %s = %+v, want no refusal", QAContext, *r)
	}
	required, err := e.svc.RequiredChecks(context.Background(), e.project)
	if err != nil || !slices.Equal(required, []string{"ci"}) {
		t.Fatalf("RequiredChecks = %v %v, want [ci]", required, err)
	}
}

// TestC30AFailedLoopQAIsNotRedCI: a loop/qa check run with the conclusion
// failure leaves the head's CI green.
func TestC30AFailedLoopQAIsNotRedCI(t *testing.T) {
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	rep := Classify(shaA, []CheckRun{
		{ID: 1, Name: "ci", HeadSHA: shaA, Status: "completed", Conclusion: "success", StartedAt: &at},
		{ID: 2, Name: QAContext, HeadSHA: shaA, Status: "completed", Conclusion: "failure", StartedAt: &at},
	}, nil)
	if rep.State != CIGreen || len(rep.Checks) != 1 || rep.Checks[0].Name != "ci" {
		t.Fatalf("Classify = %s %+v, want green with ci only", rep.State, rep.Checks)
	}
}

// TestC30TheJobCheckLeavesOutLoopQA: no workflow job reports loop/qa, and
// the required_jobs line does not ask for one, whatever its source.
func TestC30TheJobCheckLeavesOutLoopQA(t *testing.T) {
	e := newEnv(t)
	e.api.SetRules(requires("ci", QAContext))
	e.api.World.SetFile("main", ".github/workflows/ci.yml", "jobs:\n  ci: {}\n")
	line, err := e.svc.CheckRequiredJobs(context.Background(), e.project)
	if err != nil || !line.OK {
		t.Fatalf("required_jobs = %+v %v, want ok", line, err)
	}
}

// TestC31DoctorChecksLoopQAInTheRuleset: the qa_required line passes only
// on a ruleset that requires loop/qa from the loop App, and the CI line
// counts neither loop context.
func TestC31DoctorChecksLoopQAInTheRuleset(t *testing.T) {
	for _, c := range []struct {
		name   string
		rules  []any
		qa, ci bool
	}{
		{"loop/qa from the App", requires("ci", qaRule), true, true},
		{"no loop/qa", requires("ci"), false, true},
		{"loop/qa from anyone", requires("ci", QAContext), false, true},
		{"only the loop's contexts", requires(qaRule), true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.api.SetRules(c.rules)
			lines, err := e.svc.CheckRules(context.Background(), e.project)
			if err != nil {
				t.Fatal(err)
			}
			ok := map[string]bool{}
			for _, l := range lines {
				ok[l.Check] = l.OK
			}
			if q, found := ok["qa_required"]; !found || q != c.qa || ok["ci_required"] != c.ci {
				t.Fatalf("lines = %+v, want qa_required %v and ci_required %v", lines, c.qa, c.ci)
			}
		})
	}
}
