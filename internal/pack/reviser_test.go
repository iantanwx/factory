package pack_test

import (
	"strings"
	"testing"
)

// C18: the reviser keeps its right to disagree. Section 2 still offers
// agree and disagree. Section 4 lands every agreed change in one commit,
// commits nothing when the run agrees with no finding, and names the
// refusal of a split edit set and its recovery. The turn skill lists the
// code.
func TestC18ReviserKeepsItsRightToDisagree(t *testing.T) {
	reviser := read(t, "runbooks/reviser.md")
	answer := section(t, reviser, "2. Answer each finding once")
	for _, want := range []string{"**Agree.** Change the document", "**Disagree.** Change nothing."} {
		if !strings.Contains(answer, want) {
			t.Errorf("reviser section 2 does not offer %q", want)
		}
	}
	land := section(t, reviser, "4. Land the revision")
	for _, want := range []string{
		"Every change you agree with lands in one commit.",
		"A run that agrees with no finding commits nothing.",
		"`EDIT_SET_SPLIT`",
		"`git reset --soft` to the item head",
	} {
		if !strings.Contains(land, want) {
			t.Errorf("reviser section 4 does not state %q", want)
		}
	}
	turn := section(t, read(t, "skills/loop-sdlc-turn/SKILL.md"), "2. Every command answers")
	if !strings.Contains(turn, "`EDIT_SET_SPLIT`") {
		t.Error("loop-sdlc-turn section 2 does not list EDIT_SET_SPLIT")
	}
}
