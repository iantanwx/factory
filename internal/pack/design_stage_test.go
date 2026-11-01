package pack_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/shape"
)

// TestC11PRDWritersSetTheUILine proves C11 of spec factorio-a4yq. The PRD
// template holds a UI line under its story, where the prd gate reads it.
// The brainstorm skill and the brainstorm room set the line of each story
// with the operator, in a project whose config holds a ui key. The
// unattended brainstorm writes each line from the request. Each file says
// so in one paragraph, so words scattered over the file do not pass.
func TestC11PRDWritersSetTheUILine(t *testing.T) {
	stories := shape.Stories(read(t, "skills/loop-brainstorm/prd.md"))
	if len(stories) != 1 || stories[0].UI == shape.UIMissing {
		t.Errorf("the PRD template's stories = %+v, want one story with a **UI:** line under it", stories)
	}
	for file, words := range map[string][]string{
		"skills/loop-brainstorm/SKILL.md":   {"`**UI:** yes`", "`**UI:** no`", "`ui` key", "operator"},
		"runbooks/brainstorm-room.md":       {"`**UI:**`", "`ui` key", "operator"},
		"runbooks/unattended-brainstorm.md": {"`**UI:**`", "`ui` key", "`request` fact"},
	} {
		if !slices.ContainsFunc(strings.Split(read(t, file), "\n\n"), func(p string) bool {
			return !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(p, w) })
		}) {
			t.Errorf("%s holds no paragraph that names each of %q", file, words)
		}
	}
}
