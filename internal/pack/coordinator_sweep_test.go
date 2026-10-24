package pack_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/overwatch"
	"github.com/iantanwx/loop/internal/pack"
)

const coordinatorRunbook = "runbooks/coordinator.md"

// flat joins each run of white space into one space, so a wrapped line
// still matches.
func flat(text string) string { return strings.Join(strings.Fields(text), " ") }

// coordinatorSection returns section n of the coordinator runbook, from its
// heading to the next level-2 heading.
func coordinatorSection(t *testing.T, n string) string {
	t.Helper()
	s := pack.Section(read(t, coordinatorRunbook), 2, func(title string) bool { return strings.HasPrefix(title, n+". ") })
	if s == "" {
		t.Fatalf("the coordinator runbook has no section %s", n)
	}
	return s
}

// C29: the first command in section 1 is the sweep, and the inbox list is
// the second.
func TestC29CoordinatorSectionOneRunsTheSweepFirst(t *testing.T) {
	s := coordinatorSection(t, "1")
	var cmds []string
	in := false
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "```") {
			in = !in
			continue
		}
		if in && strings.HasPrefix(l, "loop ") {
			cmds = append(cmds, strings.TrimSpace(l))
		}
	}
	if len(cmds) < 2 || cmds[0] != "loop overwatch sweep" || cmds[1] != "loop inbox list" {
		t.Fatalf("section 1 runs %q, want loop overwatch sweep, then loop inbox list", cmds)
	}
}

// C29: the part table names each name in PartNames once, in PartNames
// order, and no other, and each row names a section the runbook holds.
func TestC29CoordinatorPartTableNamesEachPartOnce(t *testing.T) {
	text := read(t, coordinatorRunbook)
	rows := pack.Table(coordinatorSection(t, "1"), "part", "section", "what you do")
	if len(rows) == 0 {
		t.Fatal("section 1 holds no table with the header Part | Section | What you do")
	}
	var names []string
	for _, r := range rows {
		names = append(names, pack.Bare(r.Cells[0]))
		n := pack.Bare(r.Cells[1])
		if !regexp.MustCompile(`^[0-9]+$`).MatchString(n) || !strings.Contains(text, fmt.Sprintf("\n## %s. ", n)) {
			t.Errorf("line %d: part %s names section %q, which the runbook does not hold", r.Line, r.Cells[0], r.Cells[1])
		}
	}
	if !slices.Equal(names, overwatch.PartNames) {
		t.Fatalf("the part table names %q, want %q", names, overwatch.PartNames)
	}
}

// C29: the coordinator quotes a finding's key in each record it files for
// the finding, so the sweep reads the finding as filed.
func TestC29CoordinatorQuotesTheFindingKey(t *testing.T) {
	s := flat(coordinatorSection(t, "1"))
	const want = "When you file a bug, report a fault, prepare a question or file a decision for a finding, quote the finding's key in its body."
	if !strings.Contains(s, want) {
		t.Fatalf("section 1 does not hold %q", want)
	}
}

// C29: the reading list puts a project's health limits in project config's
// health block, and the policy keeps no limit.
func TestC29CoordinatorReadingListNamesTheHealthBlock(t *testing.T) {
	text := flat(read(t, coordinatorRunbook))
	for _, want := range []string{
		"A project's health limits live in the `health` block of its project config `.loop/config.yaml`.",
		"The policy keeps no limit.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the reading list does not hold %q", want)
		}
	}
	if strings.Contains(text, "standing authorizations, health limits and report additions") {
		t.Error("the reading list still says that the policy names health limits")
	}
}

// C30: the glossary defines the sweep's four words.
func TestC30GlossaryNamesTheSweepsWords(t *testing.T) {
	text := read(t, "skills/loop-sdlc-glossary/SKILL.md")
	for _, word := range []string{"Overwatch", "Sweep", "Part", "Finding key"} {
		if !strings.Contains(text, "- **"+word+"**: ") {
			t.Errorf("the glossary does not define %q", word)
		}
	}
}

// C33: section 6 acts on the decisions part's notes, and it offers
// `loop decision defer` only for a decision in needs_human, as the decision
// table allows.
func TestC33CoordinatorSectionSixActsOnTheDecisionsPart(t *testing.T) {
	s := coordinatorSection(t, "6")
	if !strings.Contains(s, "`decisions` part") {
		t.Error("section 6 does not name the `decisions` part")
	}
	if strings.Contains(s, "loop work list --kind decision") {
		t.Error("section 6 still lists the decisions itself")
	}
	defers := 0
	for i, l := range strings.Split(s, "\n") {
		if !strings.Contains(l, "decision defer") {
			continue
		}
		defers++
		if !strings.Contains(l, "`needs_human`") {
			t.Errorf("section 6, line %d names decision defer and not `needs_human`: %q", i+1, l)
		}
	}
	if defers == 0 {
		t.Error("section 6 names no `loop decision defer`")
	}
}

// C33: section 9 step 3 files a bug for each fault that the bugs part
// names, and for no other fault.
func TestC33CoordinatorSectionNineFilesTheFaultsOfTheBugsPart(t *testing.T) {
	s := flat(coordinatorSection(t, "9"))
	i := strings.Index(s, " 3. ")
	j := strings.Index(s, " 4. ")
	if i < 0 || j < i {
		t.Fatal("section 9 holds no step 3")
	}
	step := s[i:j]
	for _, want := range []string{"`bugs` part", "for no other fault"} {
		if !strings.Contains(step, want) {
			t.Errorf("section 9 step 3 does not hold %q: %q", want, step)
		}
	}
}
