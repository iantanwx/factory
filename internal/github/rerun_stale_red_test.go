package github

import (
	"testing"
	"time"
)

// F-69: a rerun of a failed workflow run starts attempt 2 at the same
// head. While attempt 2 runs, a job that has not started again still
// shows its attempt-1 failure. That failure belongs to a superseded
// attempt, so the head is pending, not red, until attempt 2 reports.
func TestARerunsSupersededAttemptIsPendingNotRed(t *testing.T) {
	const head = "c8bca9c13f95573d8fad8cab34a06be57f127a1b"
	at := func(h, m int) *time.Time { t := time.Date(2026, 11, 1, h, m, 0, 0, time.UTC); return &t }
	job := func(id string) string {
		return "https://github.com/slothware-labs/loop/actions/runs/41292919362/job/" + id
	}
	runs := []CheckRun{
		// Attempt 1: gates and verify failed, acceptance passed.
		{ID: 1, Name: "acceptance", HeadSHA: head, Status: "completed", Conclusion: "success", StartedAt: at(9, 10), HTMLURL: job("124809435600")},
		{ID: 2, Name: "gates", HeadSHA: head, Status: "completed", Conclusion: "failure", StartedAt: at(9, 10), HTMLURL: job("124809435605")},
		{ID: 3, Name: "verify", HeadSHA: head, Status: "completed", Conclusion: "failure", StartedAt: at(9, 11), HTMLURL: job("124809435611")},
		// Attempt 2, started by `loop pr rerun`: gates runs again, and
		// verify has not started yet.
		{ID: 4, Name: "gates", HeadSHA: head, Status: "in_progress", StartedAt: at(9, 37), HTMLURL: job("124811960000")},
	}

	rep := Classify(head, runs, nil)

	if rep.State != CIPending {
		t.Fatalf("head state = %q while attempt 2 runs; want %q. checks: %+v", rep.State, CIPending, rep.Checks)
	}
	for _, c := range rep.Checks {
		if c.State == CIRed {
			t.Errorf("check %s (%s) is red from a superseded attempt; want pending", c.Name, c.URL)
		}
	}
}
