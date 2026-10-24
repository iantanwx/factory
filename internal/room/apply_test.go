package room

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// A room applies only what a person decided: a decision file with no
// operator statement is refused with NO_OPERATOR_STATEMENT.
func TestDecisionFileNeedsAnOperatorStatement(t *testing.T) {
	for raw, code := range map[string]string{
		`{"decision": "left"}`:                          cli.CodeNoOperatorStatement,
		`{"decision": "left", "statement": "   "}`:      cli.CodeNoOperatorStatement,
		`{"statement": "I said left"}`:                  cli.CodeUsage,
		`{"decision": "left", "statement": "x", "z":1}`: cli.CodeUsage,
		`not json`: cli.CodeUsage,
	} {
		if _, ref := ParseDecisionFile(raw); ref == nil || ref.Code != code {
			t.Errorf("%s = %+v, want %s", raw, ref, code)
		}
	}
	f, ref := ParseDecisionFile(`{"decision": " left ", "statement": "Left. The right one hides the total.", "amend": true}`)
	if ref != nil || f.Decision != "left" || !f.Amend || !strings.HasPrefix(f.Statement, "Left.") {
		t.Fatalf("file = %+v %+v", f, ref)
	}
}

func TestRoomPromptNamesItsRunbookAndReason(t *testing.T) {
	r := &Room{ID: "room-1", Project: "alpha", Item: "tk-1", Kind: "intake", Reason: "the operator has a new idea"}
	p := Prompt(r, ".claude/skills/loop-intake-room/SKILL.md")
	for _, want := range []string{"loop intake room about work item tk-1", ".claude/skills/loop-intake-room/SKILL.md",
		"the operator has a new idea", "never makes the decision", "loop room apply room-1"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if Stage("decision") != "decision-room" {
		t.Fatal(Stage("decision"))
	}
}

// A manifest entry may name each room's stage, so a key reaches one kind
// of room only.
func TestManifestTakesEveryRoomStage(t *testing.T) {
	for _, k := range Kinds {
		if _, err := config.ParseManifest([]byte("secrets:\n  - name: K\n    stages: [" + Stage(k) + "]\n")); err != nil {
			t.Fatalf("%s: %v", Stage(k), err)
		}
	}
}
