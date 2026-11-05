package pack_test

import (
	"strings"
	"testing"
)

// coordinatorOpening returns the coordinator runbook from its front matter to
// its first numbered section.
func coordinatorOpening(t *testing.T) string {
	t.Helper()
	text := read(t, coordinatorRunbook)
	end := strings.Index(text, "\n## 1. ")
	if end < 0 {
		t.Fatal("the coordinator runbook has no section 1")
	}
	return text[:end]
}

// C28 (spec factorio-1edo): the runbook and the glossary name one
// coordinator for each project, and no longer one resident agent of the
// instance.
func TestC28CoordinatorRunbookNamesOneCoordinatorForEachProject(t *testing.T) {
	for _, name := range []string{coordinatorRunbook, glossarySkill} {
		if strings.Contains(flat(read(t, name)), "one resident agent") {
			t.Errorf("%s still names one resident agent", name)
		}
	}
	const want = "Each registered project with a project config has one coordinator of its own."
	if !strings.Contains(flat(coordinatorOpening(t)), want) {
		t.Errorf("the coordinator runbook's opening does not hold %q", want)
	}
	const gloss = "- **The coordinator**: the resident agent of one project."
	if !strings.Contains(flat(read(t, glossarySkill)), gloss) {
		t.Errorf("the glossary does not hold %q", gloss)
	}
}

// C28: each coordinator reads its project and whether it is the loop
// project's coordinator from its brief.
func TestC28CoordinatorRunbookReadsProjectAndLoopProjectFromTheBrief(t *testing.T) {
	const want = "Read `project` and `loop_project` in `.loop/brief.json` first."
	if !strings.Contains(flat(coordinatorOpening(t)), want) {
		t.Fatalf("the coordinator runbook's opening does not hold %q", want)
	}
}

// C28: the opening lists the four parts that only the loop project's
// coordinator follows, and each part says so where it stands.
func TestC28CoordinatorRunbookMarksTheLoopProjectsWork(t *testing.T) {
	opening := coordinatorOpening(t)
	for _, part := range []string{
		"- § 9, Loop faults",
		"- the `rules` part of § 9",
		"- the host verbs of § 4: `upgrade`, `rollback`, `doctor`, `doctor-github` and `doctor-env`",
		"- the pause in every project of § 3",
	} {
		if !strings.Contains(opening, "\n"+part+"\n") {
			t.Errorf("the opening does not list %q as the loop project's coordinator's work", part)
		}
	}
	for _, c := range []struct{ section, want string }{
		{"3", "Only the loop project's coordinator pauses a stage in every project."},
		{"4", "Only the loop project's coordinator runs the host verbs `upgrade`, `rollback`, `doctor`, `doctor-github` and `doctor-env`."},
		{"9", "Only the loop project's coordinator follows this section, its `rules` part included."},
	} {
		if !strings.Contains(flat(coordinatorSection(t, c.section)), c.want) {
			t.Errorf("section %s does not hold %q", c.section, c.want)
		}
	}
}

// C28: the opening names only the verbs of scope instance as refused with
// loop_project_only, by the spec's scope lists, and gives every coordinator
// the path to report a loop fault outside § 9.
func TestC28CoordinatorRunbookRefusesOnlyTheInstanceWideVerbs(t *testing.T) {
	opening := flat(coordinatorOpening(t))
	for _, want := range []string{
		"The server refuses `fault ack`, `fault resolve`, `retro compared`, `coordinator upgrade` and the host verbs to you with `loop_project_only`.",
		"Every coordinator reports a loop fault with `loop fault report`, by `../loop-fault/SKILL.md`, and files a bug in its own project.",
	} {
		if !strings.Contains(opening, want) {
			t.Errorf("the coordinator runbook's opening does not hold %q", want)
		}
	}
	if strings.Contains(opening, "refuses the verbs of § 9") {
		t.Error("the coordinator runbook's opening still says the server refuses every verb of § 9")
	}
}
