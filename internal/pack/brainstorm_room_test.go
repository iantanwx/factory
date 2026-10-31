package pack_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
	packsrc "github.com/iantanwx/loop/pack"
)

const brainstormRoomRunbook = "runbooks/brainstorm-room.md"

// C10: the manifest names the brainstorm-room runbook, attended, and holds
// no intake-room key. The runbook lays out as loop-brainstorm-room and
// names itself so.
func TestC10BrainstormRoomRunbookHasItsNewName(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if _, old := m.Runbooks["intake-room"]; old {
		t.Error("the manifest still names an intake-room runbook")
	}
	rb, ok := m.Runbooks["brainstorm-room"]
	if !ok {
		t.Fatal("the manifest names no brainstorm-room runbook")
	}
	if !rb.Attended {
		t.Error("a room is attended, and the brainstorm-room runbook says it is not")
	}
	if got := pack.RunbookSource("brainstorm-room"); got != brainstormRoomRunbook {
		t.Errorf("RunbookSource(brainstorm-room) = %q, want %q", got, brainstormRoomRunbook)
	}
	if !strings.Contains(read(t, brainstormRoomRunbook), "\nname: loop-brainstorm-room\n") {
		t.Error("the brainstorm-room runbook does not name itself loop-brainstorm-room")
	}
}

// C11: the pack holds one PRD template, at skills/loop-brainstorm/prd.md,
// and both runbooks that write a PRD send the writer to the brainstorm
// skill and that template.
func TestC11OnePRDTemplateAndTwoRunbooksThatPointToIt(t *testing.T) {
	var templates []string
	err := fs.WalkDir(packsrc.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "prd.md" {
			templates = append(templates, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 1 || templates[0] != "skills/loop-brainstorm/prd.md" {
		t.Fatalf("PRD templates = %v, want skills/loop-brainstorm/prd.md only", templates)
	}
	for _, rb := range []string{brainstormRunbook, brainstormRoomRunbook} {
		text := read(t, rb)
		for _, ptr := range []string{"`../loop-brainstorm/SKILL.md`", "`../loop-brainstorm/prd.md`"} {
			if !strings.Contains(text, ptr) {
				t.Errorf("%s does not name %s", rb, ptr)
			}
		}
	}
}
