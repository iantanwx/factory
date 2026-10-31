package pack_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

// The ADR test of epic factorio-ku99 (spec work entry 1). One section of
// the documents skill states it, and every other pack file cites that
// section by its name.
const (
	adrTestTitle   = "When a decision needs an ADR"
	adrTemplate    = "skills/loop-sdlc-documents/adr.md"
	brainstormFile = "skills/loop-brainstorm/SKILL.md"
	specAuthorRB   = "runbooks/spec-author.md"
	architectRB    = "runbooks/architect-room.md"
	docReviewerRB  = "runbooks/doc-reviewer.md"
)

// adrTestSection returns the level-2 section of the documents skill whose
// title, after its number, starts with adrTestTitle.
func adrTestSection(t *testing.T) string {
	t.Helper()
	s := pack.Section(read(t, documentsSkill), 2, func(title string) bool {
		_, rest, _ := strings.Cut(title, ". ")
		return strings.HasPrefix(title, adrTestTitle) || strings.HasPrefix(rest, adrTestTitle)
	})
	if s == "" {
		t.Fatalf("%s holds no section %q", documentsSkill, adrTestTitle)
	}
	return flat(s)
}

// unquoted drops the quote marker that starts each line, so a sentence that
// wraps across two quoted lines reads as one sentence.
func unquoted(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(strings.TrimLeft(l, " "), ">")
	}
	return strings.Join(lines, "\n")
}

// C1: the test has four parts, and an ADR needs all four.
func TestC1TheADRTestHasFourPartsAndNeedsAll(t *testing.T) {
	s := adrTestSection(t)
	holdsAll(t, "the ADR test", s, []string{
		"1. **More than one epic.** It binds more than one epic, or the whole project.",
		"2. **A rejected alternative.**",
		"3. **Costly to undo.**",
		"4. **How, not what.** It decides how, not what. A PRD requirement can be its source.",
		"only when all four parts hold",
	})
}

// C2: each decision that fails the test has one place, and part 3 lists
// each costly change of US-01.
func TestC2EachDecisionThatFailsTheTestHasOnePlace(t *testing.T) {
	s := adrTestSection(t)
	holdsAll(t, "the places of the ADR test", s, []string{
		"| A choice inside one epic | The spec's `## Decisions` section |",
		"| A choice with no alternative | The spec's `## Assumptions` section |",
		"| A product requirement | The PRD |",
		"| A rule for agents | A runbook, a skill or `.loop/policy/<role>.md` |",
		"| A fact that a check can enforce | A gate |",
		"| A bug fix | The shaped bug ticket |",
	})
	holdsAll(t, "the costly changes of the ADR test", s, []string{
		"It changes a data format, a schema or stored state.",
		"It changes a public interface, such as a CLI, an API, a wire format or a file layout that others read.",
		"It changes a dependency, a framework or a vendor.",
		"It changes the deployment or process topology.",
		"It changes a trust or security boundary.",
		"It sets a rule that every later epic must follow.",
	})
}

// C3: no pack file states a second rule, and each file that asks when a
// decision needs an ADR cites the section.
func TestC3NoPackFileStatesASecondADRRule(t *testing.T) {
	second := []string{
		"Use one when a choice has more than one defensible answer",
		"A feature does not need an ADR",
		"Never write an ADR for a feature.",
	}
	err := fs.WalkDir(pack.Source(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		text := flat(unquoted(read(t, path)))
		for _, rule := range second {
			if strings.Contains(text, rule) {
				t.Errorf("%s states a second rule: %q", path, rule)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cite := "`../loop-sdlc-documents/SKILL.md` § 6, " + adrTestTitle
	for _, name := range []string{adrTemplate, coordinatorRunbook, specAuthorRB, architectRB} {
		if !strings.Contains(flat(read(t, name)), cite) {
			t.Errorf("%s does not cite %q", name, cite)
		}
	}
}

// C4: the brainstorm skill sends a decision with no product source to the
// test, and to no unnamed decision record.
func TestC4BrainstormSendsADecisionWithNoProductSourceToTheTest(t *testing.T) {
	s := flat(pack.Section(read(t, brainstormFile), 2, func(title string) bool {
		return strings.HasPrefix(title, "3. ")
	}))
	if strings.Contains(s, "Record it as a decision") {
		t.Errorf("section 3 of %s records a decision with no place", brainstormFile)
	}
	holdsAll(t, "section 3 of the brainstorm skill", s, []string{
		"A decision with no product source",
		"`../loop-sdlc-documents/SKILL.md` § 6, " + adrTestTitle,
	})
}

// C5: a new ADR supersedes, and the earlier one stays. The template offers
// no Status value that edits the earlier ADR.
func TestC5ANewADRSupersedesAndTheEarlierStays(t *testing.T) {
	holdsAll(t, "the ADR test", adrTestSection(t), []string{
		"a new ADR with a higher number supersedes it",
		"Nobody edits the earlier ADR.",
	})
	var status []string
	for _, l := range strings.Split(read(t, adrTemplate), "\n") {
		if strings.HasPrefix(l, "**Status:**") {
			status = append(status, l)
		}
	}
	if len(status) != 1 || status[0] != "**Status:** Proposed | Accepted" {
		t.Errorf("the Status lines of %s = %q, want one line %q", adrTemplate, status, "**Status:** Proposed | Accepted")
	}
}

// C20: the spec-author's source is the PRD and each ADR whose Ticket line
// names the epic, and the spec's Source line names both.
func TestC20TheSpecAuthorsSourceIsThePRDAndEachADR(t *testing.T) {
	s := flat(pack.Section(read(t, specAuthorRB), 2, func(title string) bool {
		return strings.HasPrefix(title, "1. ")
	}))
	holdsAll(t, "section 1 of the spec-author runbook", s, []string{
		"A PRD epic that went through the ADR stage",
		"the PRD and each ADR in `docs/adr/` whose `**Ticket:**` line names the epic",
	})
	source := flat(pack.Section(read(t, specTemplate), 2, func(title string) bool { return title == "Source" }))
	holdsAll(t, "the Source section of the spec template", source, []string{
		"For a PRD epic that went through the ADR stage, name the PRD and each such ADR.",
	})
}

// C24: the spec-author runs the test on each Decisions entry, and names
// each entry that passes in a Needs an ADR open question.
func TestC24TheSpecAuthorNamesEachPassingDecision(t *testing.T) {
	holdsAll(t, "the spec-author runbook", flat(read(t, specAuthorRB)), []string{
		"Run the test of `../loop-sdlc-documents/SKILL.md` § 6, " + adrTestTitle + ", on each Decisions entry.",
		"write an open question that starts `**Needs an ADR:**` and names the entry",
	})
	open := flat(pack.Section(read(t, specTemplate), 2, func(title string) bool { return title == "Open questions" }))
	holdsAll(t, "the Open questions section of the spec template", open, []string{
		"> - **Needs an ADR:**",
		"`**Needs an ADR:**`",
	})
}

// C21: the doc-reviewer runs the test on each PRD requirement, and names
// each one that passes in the --adr-needed flag of its pass.
func TestC21ThePRDReviewRunsTheTestAndNamesTheFlag(t *testing.T) {
	s := flat(pack.Section(read(t, docReviewerRB), 2, func(title string) bool {
		return strings.HasPrefix(title, "2. Review a PRD")
	}))
	holdsAll(t, "section 2 of the doc-reviewer runbook", s, []string{
		"Run the test of `../loop-sdlc-documents/SKILL.md` § 6, " + adrTestTitle + ", on each requirement.",
		`loop review submit <item> --verdict pass --head <sha> --body "<comment>" --adr-needed "<requirements>"`,
		"A pass with no `--adr-needed` clears the list of an earlier pass.",
		"the PRD merge lands the epic on `architecture`, not on `spec_needed`",
	})
}
