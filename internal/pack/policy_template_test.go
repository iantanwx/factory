package pack_test

import (
	"strings"
	"testing"
)

// A policy file names a userspace skill and the step that reads it, so
// no kernel runbook has to name one (C8).
func TestEachPolicyTemplateShowsHowToNameAUserspaceSkill(t *testing.T) {
	for _, role := range []string{"implementer", "code-reviewer", "coordinator"} {
		s := section(t, read(t, "skills/loop-init/policy/"+role+".md"), "Userspace skills")
		for _, want := range []string{"the `<name>` skill", "before `<step>`"} {
			if !strings.Contains(s, want) {
				t.Errorf("the %s template's userspace section does not hold %q: %s", role, want, s)
			}
		}
	}
}

// loop-init keeps the section only where the project declares a
// userspace skill (C8).
func TestLoopInitKeepsTheUserspaceSectionOnlyForADeclaredSkill(t *testing.T) {
	s := section(t, read(t, "skills/loop-init/SKILL.md"), "6. Policy")
	for _, want := range []string{
		"Keep the `## Userspace skills` section of each policy file when the project declares a userspace skill",
		"Delete the section otherwise",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("loop-init section 6 does not hold %q", want)
		}
	}
}
