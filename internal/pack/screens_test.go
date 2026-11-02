package pack_test

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

const decomposerRB = "runbooks/decomposer.md"

// oldDesignRef is the sentence that asked for design references before the
// **Screens:** line. Spec factorio-4lso replaces it in each document runbook.
const oldDesignRef = "when the project keeps them"

// TestC6SpecWriterAsksForTheScreensLine proves C6 of spec factorio-4lso.
// The spec template shows the - Screens: line in its work entry, for an
// epic that holds design_merged, and the planner writes it in each entry.
// Neither file asks for design references "when the project keeps them".
func TestC6SpecWriterAsksForTheScreensLine(t *testing.T) {
	holdsAll(t, "the Work section of the spec template", section(t, read(t, specTemplate), "Work"), []string{
		"In an epic that holds `design_merged`, each work entry holds a `- Screens:` line.",
		"- Screens: <the state ids it builds>, or none. <why the entry draws no screen>",
	})
	holdsAll(t, plannerRB, flat(read(t, plannerRB)), []string{
		"In an epic that holds `design_merged`, write a `- Screens:` line in each work entry.",
		"An entry that draws no screen writes `- Screens: none.` and a reason that shows why.",
	})
	for _, file := range []string{specTemplate, plannerRB} {
		if strings.Contains(flat(read(t, file)), oldDesignRef) {
			t.Errorf("%s still asks for design references %q", file, oldDesignRef)
		}
	}
}

// TestC7DocReviewerChecksEachScreensLineAndReason proves C7 of spec
// factorio-4lso. Section 3 of the doc-reviewer runbook rejects a spec of an
// epic that holds design_merged when a work entry holds no - Screens: line,
// or when a none reason does not show that the entry draws no screen. Each
// reject names the entry.
func TestC7DocReviewerChecksEachScreensLineAndReason(t *testing.T) {
	s := flat(pack.Section(read(t, docReviewerRB), 2, func(title string) bool {
		return strings.HasPrefix(title, "3. Review a spec")
	}))
	if s == "" {
		t.Fatalf("%s holds no section %q", docReviewerRB, "3. Review a spec")
	}
	holdsAll(t, "section 3 of the doc-reviewer runbook", s, []string{
		"When the epic holds `design_merged`, read the `- Screens:` line of each work entry.",
		"Reject when a work entry holds no `- Screens:` line, and name the entry in the comment.",
		"Reject when a `none` reason does not show that the entry draws no screen, and name the entry in the comment.",
	})
}

// TestC8DecomposerCopiesTheScreensLineIntoTheTicket proves C8 of spec
// factorio-4lso. The decomposer copies each entry's - Screens: line into
// the ticket's ## Source as a **Screens:** line, and names the
// ticket_screens refusal. It holds no stop for a screen with no design
// reference.
func TestC8DecomposerCopiesTheScreensLineIntoTheTicket(t *testing.T) {
	s := flat(read(t, decomposerRB))
	holdsAll(t, decomposerRB, s, []string{
		"copy the entry's `- Screens:` line into `## Source` as a `**Screens:**` line",
		"`ticket_screens`:",
	})
	for _, old := range []string{oldDesignRef, "design reference"} {
		if strings.Contains(s, old) {
			t.Errorf("%s still holds %q", decomposerRB, old)
		}
	}
}
