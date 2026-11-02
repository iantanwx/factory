package pack_test

import (
	"strings"
	"testing"
)

// TestC15CoordinatorRecordsAnAcceptanceWithTheOperatorsWords proves C15
// of spec factorio-4lso on the pack side. Section 8 of the coordinator
// runbook runs the acceptance only on the operator's word, with those
// words, on an epic in execution. It names the fact that holds them and
// each refusal of C16.
func TestC15CoordinatorRecordsAnAcceptanceWithTheOperatorsWords(t *testing.T) {
	s := flat(coordinatorSection(t, "8"))
	if !strings.Contains(s, `loop epic accept-difference <epic> --state <id> --request "<the operator's words>"`) {
		t.Errorf("section 8 of %s runs no acceptance with the operator's words", coordinatorRunbook)
	}
	for _, w := range []string{"`execution`", "`accepted_differences`", "`no_operator_request`", "`state_unknown`", "`no_design_record`"} {
		if !strings.Contains(s, w) {
			t.Errorf("section 8 of %s does not name %s", coordinatorRunbook, w)
		}
	}
}
