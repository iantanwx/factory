package pack_test

import (
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

const faultSkill = "skills/loop-fault/SKILL.md"

// The fault skill ships as a shared skill, so every run, every room and
// every session on a host with the pack installed receives it
// (factorio-uyp).
func TestFaultSkillShipsAsASharedSkill(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Skills, "loop-fault") {
		t.Fatal("the manifest does not list loop-fault as a shared skill")
	}
	tree, err := pack.Layout(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(tree, "loop-fault/SKILL.md"); err != nil {
		t.Fatalf("the layout holds no loop-fault/SKILL.md: %v", err)
	}
}

// An agent in a client project, outside every run, has no brief and no
// runbook that sends it to the turn skill. The description is the only
// text that makes the harness load the fault skill there.
func TestFaultSkillDescriptionNamesASessionOutsideARun(t *testing.T) {
	text := read(t, faultSkill)
	_, rest, ok := strings.Cut(text, "\ndescription:")
	if !ok {
		t.Fatal("the fault skill holds no description")
	}
	desc, _, _ := strings.Cut(rest, "\n")
	for _, want := range []string{"outside a run", "client project", "run", "room", "coordinator"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the fault skill's description does not name %q: %s", want, desc)
		}
	}
}

// The skill holds each step of a report: read the open faults first,
// write the report to a private file, report it as a host caller from a
// client project, keep secrets out, and name the id where the work goes on.
func TestFaultSkillNamesEachStepOfAReport(t *testing.T) {
	text := strings.Join(strings.Fields(read(t, faultSkill)), " ")
	for _, want := range []string{
		"`loop fault list`",
		"`loop fault show F-<n>`",
		"`mktemp -d`",
		"loop fault report --file",
		"host caller",
		"`.loop/token`",
		"secret",
		"Never work around a loop fault in silence",
		"runbook or a gate rule is wrong",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the fault skill does not hold %s", want)
		}
	}
}

// One home for the rule: each skill that tells an agent to report a loop
// fault links the fault skill.
func TestEachSkillThatReportsAFaultLinksTheFaultSkill(t *testing.T) {
	for _, skill := range []string{"loop-sdlc-turn", "loop-reflect", "loop-encode-lessons"} {
		if !strings.Contains(read(t, "skills/"+skill+"/SKILL.md"), "../loop-fault/SKILL.md") {
			t.Errorf("%s does not link ../loop-fault/SKILL.md", skill)
		}
	}
}

// The turn skill keeps only what a run does apart from any other session:
// the faults in the brief, and a finish that names the fault. The steps of
// a report live in the fault skill alone.
func TestTurnSkillKeepsOnlyTheRunStepsOfAFault(t *testing.T) {
	s := strings.Join(strings.Fields(turnSection(t, "7")), " ")
	for _, want := range []string{"faults in your brief", "loop run finish --outcome blocked --fault F-<n>"} {
		if !strings.Contains(s, want) {
			t.Errorf("section 7 of the turn skill does not hold %q", want)
		}
	}
	for _, gone := range []string{"mktemp -d", "loop fault report --file"} {
		if strings.Contains(s, gone) {
			t.Errorf("section 7 of the turn skill still holds %q; the fault skill is its one home", gone)
		}
	}
}
