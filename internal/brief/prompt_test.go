package brief_test

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/room"
	"github.com/iantanwx/loop/internal/sched"
)

// mannered is Anthropic's definition of mannered prose, word for word
// from the writing-density guidance for Claude Fable 5.1 (factorio-kv8f).
const mannered = `Mannered prose substitutes metaphor and flourish for direct statement. Instead of "a parameter worth varying," the mannered writer produces "a dial worth turning." Instead of "this point still matters," they write "this point earns its keep." The phrases exist to display the writer, not to convey the idea, and readers can tell. That is why mannered prose irritates: it makes the reader work harder so the writer can perform. It is also imprecise. Metaphors drag in connotations the writer did not choose and cannot control. The fix is to say what you mean. When a literal phrase is available, use it.`

// TestEveryPromptHoldsTheManneredProseDefinition proves that the first
// user message of every run, room and the coordinator holds the
// definition word for word.
func TestEveryPromptHoldsTheManneredProseDefinition(t *testing.T) {
	roles := append(config.RunStages(), sched.CoordinatorStage)
	for _, k := range room.Kinds {
		roles = append(roles, room.Stage(k))
	}
	for _, role := range roles {
		p := brief.Prompt(brief.Brief{Role: role, Runbook: "runbook.md"})
		if !strings.Contains(p, mannered) {
			t.Errorf("the %s prompt lacks the mannered-prose definition:\n%s", role, p)
		}
	}
}
