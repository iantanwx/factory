package pack_test

import (
	"strings"
	"testing"
)

// TestCodeReviewerNeverRejectsForADefectTheMergeBaseHolds proves C-bug-1
// of factorio-5ne. A defect that the merge base already holds is no fault
// of the ticket's diff, and its author cannot repair it inside the ticket.
// So the runbook puts it outside every round, round 1 included: the
// reviewer names it and records it, and never rejects for it. Without the
// rule, round 1 reads it as "a fault that no line covers", rejects, and
// each later round fails the same named finding again.
func TestCodeReviewerNeverRejectsForADefectTheMergeBaseHolds(t *testing.T) {
	rb := read(t, "runbooks/code-reviewer.md")
	var rule string
	for _, para := range strings.Split(rb, "\n\n") {
		flat := strings.Join(strings.Fields(para), " ")
		if strings.Contains(flat, "the merge base already holds") {
			rule = flat
			break
		}
	}
	if rule == "" {
		t.Fatal("the code-reviewer runbook has no rule for a defect that the merge base already holds")
	}
	for _, want := range []string{"round 1", "Never reject for it"} {
		if !strings.Contains(rule, want) {
			t.Errorf("the merge-base defect rule does not say %q: %s", want, rule)
		}
	}
}
