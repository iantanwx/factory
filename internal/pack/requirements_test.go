package pack_test

import (
	"strings"
	"testing"
)

const (
	documentsSkill = "skills/loop-sdlc-documents/SKILL.md"
	reviewSkill    = "skills/loop-sdlc-review/SKILL.md"
)

// The two requirement rules of tickertown's AGENTS.md and
// docs/rules/requirements.md (parity 3.22). Each writer reads them in
// loop-sdlc-documents, and each reviewer applies them from loop-sdlc-review.
const (
	vendorRule   = "Assert a vendor's outcome, never a vendor's mechanism"
	laterADRRule = "A later ADR wins a conflict with the ADR it amends"
)

// subsection returns the text of the level-3 section whose heading is
// heading, up to the next heading of level 3 or less, with each run of
// white space as one space, so a wrapped line still matches.
func subsection(t *testing.T, name, heading string) string {
	t.Helper()
	text := read(t, name)
	_, rest, ok := strings.Cut(text, "\n### "+heading+"\n")
	if !ok {
		t.Fatalf("%s holds no section %q", name, heading)
	}
	if end := strings.Index(rest, "\n#"); end >= 0 {
		rest = rest[:end]
	}
	return strings.Join(strings.Fields(rest), " ")
}

func holdsAll(t *testing.T, where, text string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("%s does not hold %q", where, want)
		}
	}
}

// A criterion that asserts a vendor's hostname, record value or internal
// step fails on the vendor's schedule. The writer asserts the outcome, and
// names the mechanism only as the place a check looks.
func TestDocumentsSkillAssertsAVendorsOutcomeNotItsMechanism(t *testing.T) {
	s := subsection(t, documentsSkill, vendorRule)
	holdsAll(t, "the vendor rule of loop-sdlc-documents", s, []string{
		"hostname, record value or internal step",
		"Assert the outcome you depend on.",
		"Never assert the value behind the outcome.",
		"Name a vendor's mechanism only as the place you look.",
		"Never read this rule as a ban on checks.",
	})
}

// Both rules bind each heading that holds a requirement, and no gate reads
// either one, so the reviewers hold them.
func TestDocumentsSkillNamesWhereEachRequirementLives(t *testing.T) {
	text := read(t, documentsSkill)
	_, rest, ok := strings.Cut(text, "\n## 5. Two rules every requirement keeps\n")
	if !ok {
		t.Fatalf("%s holds no section on the requirement rules", documentsSkill)
	}
	intro, _, _ := strings.Cut(rest, "\n### ")
	holdsAll(t, "the requirement rules of loop-sdlc-documents", strings.Join(strings.Fields(intro), " "), []string{
		"`## Requirements` of a PRD",
		"`## Consequences` and `## Acceptance criteria` of an ADR",
		"`## Data` and `## Contracts` of a spec",
		"No gate reads either rule",
		"`../loop-sdlc-review/SKILL.md`",
	})
}

// Two ADRs can hold requirements that disagree. The later one wins, by
// number, whatever word it chooses for itself, and the rule edits neither
// ADR and reaches no PRD.
func TestDocumentsSkillLetsTheLaterADRWin(t *testing.T) {
	s := subsection(t, documentsSkill, laterADRRule)
	holdsAll(t, "the later-ADR rule of loop-sdlc-documents", s, []string{
		"Follow the later ADR.",
		"higher number",
		"Read the amendment before the ADR it amends.",
		"Never follow the earlier requirement.",
		"The word an ADR chooses for itself decides nothing.",
		"gives no document permission to edit either ADR",
		"Never apply this rule outside `docs/adr/`.",
		"No gate reports this fault.",
	})
}

// Each reviewer rejects a requirement that breaks either rule, and the
// later ADR is the rule that picks a side of a conflict between two ADRs,
// so that finding is level 3 and stays in the loop.
func TestReviewSkillAppliesBothRequirementRules(t *testing.T) {
	s := subsection(t, reviewSkill, "The two requirement rules")
	holdsAll(t, "the requirement rules of loop-sdlc-review", s, []string{
		vendorRule,
		laterADRRule,
		"`../loop-sdlc-documents/SKILL.md`",
		"Reject a requirement that asserts a vendor's hostname, record value or internal step.",
		"Reject work that follows the earlier ADR",
		"level 3, `derived`",
	})
}
