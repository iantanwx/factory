package pack_test

import (
	"strings"
	"testing"
)

// reviewSection returns the level-2 section of loop-sdlc-review whose
// heading starts with prefix, with each run of white space as one space,
// so a wrapped line still matches.
func reviewSection(t *testing.T, prefix string) string {
	t.Helper()
	text := read(t, reviewSkill)
	_, rest, ok := strings.Cut(text, "\n## "+prefix)
	if !ok {
		t.Fatalf("%s holds no section %q", reviewSkill, prefix)
	}
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return strings.Join(strings.Fields(rest), " ")
}

// TestReviewSkillLetsSubagentsFindAndOnlyTheReviewerJudge proves C10 of
// factorio-4mb8 (A14, A15). A doc review starts readers as subagents, so
// the old ban on a subagent that reviews goes. The verdict still has one
// author: the reviewer, who read all of the work and judged each finding.
func TestReviewSkillLetsSubagentsFindAndOnlyTheReviewerJudge(t *testing.T) {
	never := reviewSection(t, "9. Never")
	if strings.Contains(never, "Never start a subagent that reviews") {
		t.Errorf("§ 9 of %s still forbids a subagent that reviews: %s", reviewSkill, never)
	}
	holdsAll(t, "§ 9 of loop-sdlc-review", never, []string{
		"A subagent may find.",
		"Only the reviewer judges each finding and submits the verdict.",
		"The verdict needs one reader who read all of it, and that reader is the reviewer.",
	})
}

// TestReviewSkillLetsAConfirmedProseFindingRejectADocument proves C11 of
// factorio-4mb8, the operator's "Yes, may reject" answer at the PRD
// sign-off. A doc review has prose readers and no prose linter, so a prose
// finding that the reviewer confirmed may reject alone. The exception names
// the doc review, so the code-reviewer keeps the rule as it was.
func TestReviewSkillLetsAConfirmedProseFindingRejectADocument(t *testing.T) {
	judge := reviewSection(t, "6. Judge the work")
	holdsAll(t, "§ 6 of loop-sdlc-review", judge, []string{
		"Never reject on style that no linter flagged.",
		"In a doc review, a reader's prose finding may reject alone when you check it against the text and it holds.",
		"A code review has no such exception.",
	})
}
