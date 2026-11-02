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

const (
	implementerRB  = "runbooks/implementer.md"
	codeReviewerRB = "runbooks/code-reviewer.md"
	reviewRoomRB   = "runbooks/review-room.md"
)

// numbered returns section n of the runbook name, flattened.
func numbered(t *testing.T, name, n string) string {
	t.Helper()
	s := flat(pack.Section(read(t, name), 2, func(title string) bool { return strings.HasPrefix(title, n+". ") }))
	if s == "" {
		t.Fatalf("%s holds no section %s", name, n)
	}
	return s
}

// inOrder fails the test unless text holds each of wants, each after the
// one before it.
func inOrder(t *testing.T, where, text string, wants []string) {
	t.Helper()
	at := 0
	for _, w := range wants {
		i := strings.Index(text[at:], w)
		if i < 0 {
			t.Fatalf("%s holds no %q after %q", where, w, text[:at])
		}
		at += i + len(w)
	}
}

// TestC19ImplementerCapturesAndPushesAtEachHead proves C19 of spec
// factorio-4lso. Section 4 of the implementer runbook tells a run whose
// brief holds screens to commit, take the captures into a directory from
// mktemp -d, and push them with the head, at each head in every kind of
// work. It names the captures_missing refusal.
func TestC19ImplementerCapturesAndPushesAtEachHead(t *testing.T) {
	s := numbered(t, implementerRB, "4")
	inOrder(t, "section 4 of "+implementerRB, s, []string{
		"Commit.",
		"When the brief holds `screens`, take the captures of the head you committed.",
		"Make a directory with `mktemp -d`, and run `loop captures take --out <dir>`.",
		"Push with `loop branch push --captures <dir>`.",
	})
	holdsAll(t, "section 4 of "+implementerRB, s, []string{
		"Take the captures again at each head you push, in every kind of work.",
		"`captures_missing`",
	})
}

// TestC29ImplementerPushesTheSameHeadAgainForAnAcceptedState proves C29
// of spec factorio-4lso on the pack side. Section 3 of the implementer
// runbook tells a run whose red build is a pixel_over on an accepted state
// to push the captures of the same head again, then run loop pr rerun.
func TestC29ImplementerPushesTheSameHeadAgainForAnAcceptedState(t *testing.T) {
	s := numbered(t, implementerRB, "3")
	inOrder(t, "section 3 of "+implementerRB, s, []string{
		"When the red build is a `pixel_over` on a state that `screens.accepted` in the brief holds, change no code.",
		"Take the captures of the same head into a directory from `mktemp -d`, and push them again with `loop branch push --captures <dir>`.",
		"Then run `loop pr rerun`.",
	})
}

// TestC20CodeReviewerComparesStructureAndRejectsAnUnacceptedDifference
// proves C20 of spec factorio-4lso. The code-reviewer runbook fetches the
// capture branch with the captures part of loop branch sync, reads each
// capture of the reviewed head beside the record's picture, compares
// structure and no pixels, and rejects a missing capture, an unaccepted
// difference and a failed pixel gate.
func TestC20CodeReviewerComparesStructureAndRejectsAnUnacceptedDifference(t *testing.T) {
	s := numbered(t, codeReviewerRB, "5")
	inOrder(t, "section 5 of "+codeReviewerRB, s, []string{
		"When the brief holds `screens`, check each cited state at the head you review.",
		"Run the `fetch` command of the `captures` part of `loop branch sync`.",
		"`git show origin/<capture branch>:<head>/<state id>.png`",
		"Open it beside the record's picture of the state, as a PNG.",
	})
	holdsAll(t, "section 5 of "+codeReviewerRB, s, []string{
		"Reject a cited state with no capture at the reviewed head, and name the state.",
		"Compare the elements, the copy, the flow and the states that exist. Never compare pixels.",
		"Reject a difference that no acceptance of the state in `screens.accepted` describes, and name the state and the difference.",
		"Pass a difference that an acceptance of the state describes.",
		"Reject a head whose `pixel` gate failed, and name each state and its share.",
	})
}

// TestC28ReviewRoomCapturesAndPushesACitingTicket proves C28 of spec
// factorio-4lso on the pack side. Section 2 of the review room runbook
// tells a room whose ticket's item facts hold screens to commit, take the
// captures into a directory from mktemp -d, and push them, before each
// push.
func TestC28ReviewRoomCapturesAndPushesACitingTicket(t *testing.T) {
	s := numbered(t, reviewRoomRB, "2")
	inOrder(t, "section 2 of "+reviewRoomRB, s, []string{
		"Commit,",
		"When the item facts in `.loop/brief.json` hold `screens`, take the captures of the new head before each push.",
		"Make a directory with `mktemp -d`, and run `loop captures take --out <dir>`.",
		"Push with `loop branch push --captures <dir>`.",
	})
	holdsAll(t, "section 2 of "+reviewRoomRB, s, []string{"`captures_missing`"})
}
