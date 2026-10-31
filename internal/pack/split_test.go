package pack_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/sched"
)

const splitSkill = "skills/loop-sdlc-split/SKILL.md"

// section returns the text of the level-2 section whose heading starts
// with prefix, up to the next level-2 heading, with each run of white
// space as one space, so a wrapped line still matches.
func section(t *testing.T, text, prefix string) string {
	t.Helper()
	start := strings.Index(text, "\n## "+prefix)
	if start < 0 {
		t.Fatalf("the text holds no section %q", prefix)
	}
	rest := text[start+1:]
	if end := strings.Index(rest[3:], "\n## "); end >= 0 {
		rest = rest[:end+3]
	}
	return strings.Join(strings.Fields(rest), " ")
}

// The split procedure keeps the epic it started with: the room revises
// that epic's PRD in place, and the coordinator retitles it, in place of
// a create and a cancel (tickertown sdlc-split section 2).
func TestSplitSkillReusesTheEpicItStartedWith(t *testing.T) {
	s := section(t, read(t, splitSkill), "5. Reuse")
	for _, want := range []string{"loop branch push", "loop work edit <epic> --title", "Never move a PRD"} {
		if !strings.Contains(s, want) {
			t.Errorf("the reuse step does not name %q", want)
		}
	}
}

// Each document passes its gates before any command creates an epic, so
// a crash leaves a spare file and never an epic with a failing PRD
// (tickertown sdlc-split section 3).
func TestSplitSkillGatesEachDocumentBeforeItCreatesAnEpic(t *testing.T) {
	text := read(t, splitSkill)
	gate := strings.Index(text, "loop gates --docs")
	if gate < 0 {
		t.Fatal("the split skill never runs loop gates --docs")
	}
	submit := strings.Index(text, "loop prd submit --path")
	if submit < 0 {
		t.Fatal("the split skill never names loop prd submit --path")
	}
	if submit < gate {
		t.Fatal("the split skill submits a PRD before it runs the document gates")
	}
}

// One brainstorm room creates one epic: prdSubmit keys the epic to the room.
// So the procedure opens one room for each sibling, and says that the
// loop records no edge between two epics (tickertown sdlc-split
// section 4).
func TestSplitSkillCreatesEachSiblingInARoomOfItsOwn(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "pipeline", "prd.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `machine.FactCreateKey: "intake/" + room`) {
		t.Fatal("prdSubmit no longer keys the epic to the room; the split skill's sibling step is stale")
	}
	s := section(t, read(t, splitSkill), "7. Create")
	for _, want := range []string{"loop room open --kind brainstorm", "One brainstorm room creates one epic", "no edge between two epics"} {
		if !strings.Contains(s, want) {
			t.Errorf("the sibling step does not say %q", want)
		}
	}
}

// A submit that exits 0 is no proof. The procedure reads each epic back
// and checks its status, its title and its PRD path (tickertown
// sdlc-split section 5).
func TestSplitSkillReadsEachWriteBack(t *testing.T) {
	s := section(t, read(t, splitSkill), "8. Read")
	for _, want := range []string{"loop work show <epic>", "`prd_review`", "`prd_path`", "loop work list --kind epic --status prd_review"} {
		if !strings.Contains(s, want) {
			t.Errorf("the read-back step does not name %q", want)
		}
	}
}

// Each refusal of prd submit that a split meets has its recovery in the
// skill, and each rule the skill names is one the server still sends
// (tickertown sdlc-split section 6).
func TestSplitSkillNamesTheRecoveryOfEachSubmitRefusal(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "pipeline", "prd.go"))
	if err != nil {
		t.Fatal(err)
	}
	s := section(t, read(t, splitSkill), "9. If it goes wrong")
	for _, rule := range []string{"room_holds_item", "prd_moved", "prd_past_review"} {
		if !strings.Contains(string(src), `"`+rule+`"`) {
			t.Errorf("prd submit no longer sends the rule %s", rule)
		}
		if !strings.Contains(s, "`"+rule+"`") {
			t.Errorf("the recovery step does not name the rule %s", rule)
		}
	}
	for _, want := range []string{"the same `loop prd submit` again", "`CAP_FULL`"} {
		if !strings.Contains(s, want) {
			t.Errorf("the recovery step does not say %q", want)
		}
	}
}

// The coordinator does the split's steps that a room cannot: it retitles
// the reused epic and opens a room for each sibling. So it reads the
// skill.
func TestCoordinatorReadsTheSplitSkill(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Runbooks[sched.CoordinatorStage].Skills, "loop-sdlc-split") {
		t.Fatal("the coordinator's runbook does not load loop-sdlc-split")
	}
	if !strings.Contains(read(t, "runbooks/coordinator.md"), "../loop-sdlc-split/SKILL.md") {
		t.Fatal("the coordinator's runbook never points at the split procedure")
	}
}
