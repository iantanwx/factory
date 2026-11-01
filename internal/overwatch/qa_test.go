package overwatch

import (
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// C32: one sweep over three fixture projects. A project with the qa stage
// on and no verify skill in its tracked tree is one finding,
// overwatch:qa:<project>, whose line names loop-verify-create. A project
// with the stage off, or with one verify skill, gives no line.
func TestC32TheSweepReportsAProjectWithNoVerifySkill(t *testing.T) {
	on, off := true, false
	cases := []struct {
		project string
		stage   *bool
		tree    memTree
		finding bool
	}{
		{"alpha", &on, memTree{"go.mod": "module a\n", ".claude/skills/loop-init/SKILL.md": "---\n"}, true},
		{"beta", &off, memTree{"go.mod": "module b\n"}, false},
		{"gamma", nil, memTree{"go.mod": "module c\n", ".claude/skills/verify-web/SKILL.md": "---\nname: verify-web\n---\n"}, false},
	}
	w := newUIWorld(t, "alpha", "beta", "gamma")
	for _, c := range cases {
		w.configs[c.project] = &config.ProjectSnapshot{Config: &config.ProjectConfig{
			Stages: map[string]config.StageConfig{config.StageQA: {Enabled: c.stage}}}}
		w.trees[c.project] = c.tree
	}
	pr := sweepOne(t, w, partNamed(t, "qa"))
	expect(t, pr, Work, "overwatch:qa:alpha", "loop-verify-create")
	for _, c := range cases {
		want := 0
		if c.finding {
			want = 1
		}
		if got := unfiled(pr, "overwatch:qa:"+c.project); got != want {
			t.Errorf("%s: %d unfiled lines quote overwatch:qa:%s, want %d: %q", c.project, got, c.project, want, pr.Lines)
		}
		if !c.finding && slices.ContainsFunc(pr.Lines, func(l string) bool { return strings.Contains(l, c.project) }) {
			t.Errorf("%s: a line names the project, which owes no verify skill: %q", c.project, pr.Lines)
		}
	}
}
