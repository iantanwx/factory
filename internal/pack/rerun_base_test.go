package pack_test

import (
	"strings"
	"testing"
)

// TestImplementerMergesTheBaseWhenTheBaseCausedTheRedBuild proves C-bug-1
// of factorio-smk0. A rerun of a pull_request run keeps the merge commit
// GitHub made when the run first started, so it tests the old base. It
// can repair a flaky failure, never a failure that the base caused and
// the base later fixed. So the repair rule that names `loop pr rerun`
// must say a rerun tests the old base, and must send a base-caused
// failure to a merge of the base, which section 2 then allows with no
// conflict.
func TestImplementerMergesTheBaseWhenTheBaseCausedTheRedBuild(t *testing.T) {
	rb := read(t, "runbooks/implementer.md")
	var rerun, merge string
	for _, para := range strings.Split(rb, "\n\n") {
		flat := strings.Join(strings.Fields(para), " ")
		if rerun == "" && strings.Contains(flat, "`loop pr rerun`") {
			rerun = flat
		}
		if merge == "" && strings.Contains(flat, "Merge the base only when") {
			merge = flat
		}
	}
	if rerun == "" {
		t.Fatal("the implementer runbook has no repair rule that names `loop pr rerun`")
	}
	for _, want := range []string{"old base", "merge the base"} {
		if !strings.Contains(strings.ToLower(rerun), want) {
			t.Errorf("the rerun rule does not say %q: %s", want, rerun)
		}
	}
	if merge == "" {
		t.Fatal("the implementer runbook has no rule for when to merge the base")
	}
	if !strings.Contains(merge, "the base caused") {
		t.Errorf("the merge-the-base rule allows no merge for a failure the base caused: %s", merge)
	}
}
