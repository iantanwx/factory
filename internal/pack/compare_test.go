package pack_test

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

// rulesSection returns the subsection of coordinator section 9 that acts
// on the `rules` part, as one joined line.
func rulesSection(t *testing.T) string {
	t.Helper()
	s := pack.Section(coordinatorSection(t, "9"), 3, func(title string) bool { return strings.Contains(title, "`rules` part") })
	if s == "" {
		t.Fatal("coordinator section 9 has no subsection for the `rules` part")
	}
	return flat(s)
}

// C26: the rules section reads the open faults before any report, links
// an open fault that names the rule, files one fault that names both
// projects and both rules when another project holds it, and records a
// comparison with no fault when none does.
func TestC26CoordinatorFilesOneFaultForASharedRuleAndNoneOtherwise(t *testing.T) {
	s := rulesSection(t)
	steps := []string{
		"Read the merged change of the learning's ticket.",
		"Read the project policy files and the userspace skills of each other registered project, at its default branch.",
		"Run `loop fault list --status open`.",
		"When an open fault already names the rule, run `loop retro compared <learning> --fault F-<n>`, and file nothing.",
		"When another project holds the same rule, file one fault with `loop fault report`.",
		"It names both projects, both rules and the finding key of each learning.",
		"Then run `loop retro compared <learning> --fault F-<n>` for each learning it names.",
		"When no other project holds the rule, run `loop retro compared <learning>`, and file nothing.",
	}
	at := -1
	for _, want := range steps {
		i := strings.Index(s, want)
		if i < 0 {
			t.Fatalf("the rules section does not say %q: %s", want, s)
		}
		if i < at {
			t.Errorf("the rules section says %q before the step that comes first", want)
		}
		at = i
	}
	list, report := strings.Index(s, "loop fault list --status open"), strings.Index(s, "loop fault report")
	if list < 0 || report < 0 || list > report {
		t.Errorf("the rules section must run `loop fault list --status open` before the first `loop fault report`: %s", s)
	}
}

// C26: the same section applies the operator's answer to a `learning`
// entry with the operator's words, through a ticket, a fault or nothing.
func TestC26CoordinatorAppliesTheAnswerToALearningEntry(t *testing.T) {
	s := rulesSection(t)
	for _, want := range []string{
		"a `learning` entry",
		"`loop work create --project <project> --title \"<title>\" --body \"<body>\" --request \"<the operator's words>\"`",
		"`loop fault report`",
		"When the answer drops the learning, do nothing more.",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the rules section does not say %q: %s", want, s)
		}
	}
}
