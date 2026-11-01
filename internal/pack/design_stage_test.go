package pack_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/shape"
)

// TestC11PRDWritersSetTheUILine proves C11 of spec factorio-a4yq. The PRD
// template holds a UI line under its story, where the prd gate reads it.
// The brainstorm skill and the brainstorm room set the line of each story
// with the operator, in a project whose config holds a ui key. The
// unattended brainstorm writes each line from the request. Each file says
// so in one paragraph, so words scattered over the file do not pass.
func TestC11PRDWritersSetTheUILine(t *testing.T) {
	stories := shape.Stories(read(t, "skills/loop-brainstorm/prd.md"))
	if len(stories) != 1 || stories[0].UI == shape.UIMissing {
		t.Errorf("the PRD template's stories = %+v, want one story with a **UI:** line under it", stories)
	}
	for file, words := range map[string][]string{
		"skills/loop-brainstorm/SKILL.md":   {"`**UI:** yes`", "`**UI:** no`", "`ui` key", "operator"},
		"runbooks/brainstorm-room.md":       {"`**UI:**`", "`ui` key", "operator"},
		"runbooks/unattended-brainstorm.md": {"`**UI:**`", "`ui` key", "`request` fact"},
	} {
		if !slices.ContainsFunc(strings.Split(read(t, file), "\n\n"), func(p string) bool {
			return !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(p, w) })
		}) {
			t.Errorf("%s holds no paragraph that names each of %q", file, words)
		}
	}
}

// TestC23DocReviewerChecksEachVisibleCriterionAgainstAState proves C23 of
// spec factorio-a4yq. The doc-reviewer runbook holds a section that starts
// "Review a design record". It reads each criterion of each UI story, and
// rejects a criterion that a user can see, or a refusal, with no state
// that shows it. It submits with loop review submit, and never merges.
// Its description names the design record, because the skill list shows
// only that line.
func TestC23DocReviewerChecksEachVisibleCriterionAgainstAState(t *testing.T) {
	_, rest, _ := strings.Cut(read(t, docReviewerRB), "\ndescription:")
	if desc, _, _ := strings.Cut(rest, "\n"); !strings.Contains(desc, "design record") {
		t.Errorf("the description of %s does not name the design record: %s", docReviewerRB, desc)
	}
	s := flat(pack.Section(read(t, docReviewerRB), 2, func(title string) bool {
		_, rest, _ := strings.Cut(title, ". ")
		return strings.HasPrefix(rest, "Review a design record")
	}))
	if s == "" {
		t.Fatalf("%s holds no section that starts %q", docReviewerRB, "Review a design record")
	}
	holdsAll(t, "the design review of the doc-reviewer runbook", s, []string{
		"Read each acceptance criterion of each `UI: yes` story of the PRD.",
		"Reject when a criterion that a user can see has no state, and name the criterion in the comment.",
		"Reject when a criterion that says the product refuses something has no state that shows the refusal.",
		`loop review submit <item> --verdict pass|reject --head <sha> --body "<comment>"`,
		"Never merge the design record.",
	})
}

// TestC16CoordinatorSkipsTheDesignStageWithTheOperatorsWords proves C16 of
// spec factorio-a4yq on the pack side. The coordinator runbook runs the
// skip only on the operator's word, with those words, on an epic in
// design. It says that an open design pull request holds the skip.
func TestC16CoordinatorSkipsTheDesignStageWithTheOperatorsWords(t *testing.T) {
	s := flat(coordinatorSection(t, "8"))
	if !strings.Contains(s, `loop epic skip-design <epic> --request "<the operator's words>"`) {
		t.Errorf("section 8 of %s runs no skip with the operator's words", coordinatorRunbook)
	}
	for _, w := range []string{"`design`", "`spec_needed`", "`design_skipped`", "`design_pr_open`"} {
		if !strings.Contains(s, w) {
			t.Errorf("section 8 of %s does not name %s", coordinatorRunbook, w)
		}
	}
}
