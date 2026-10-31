//go:build acceptance

package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/machine"
)

// C5 and C10, the done means of factorio-2mwq: the manifest gives Jev's key
// to the stage by its old name, stages: [fixer]. An escalation that waits
// for its panel starts a triage run. Its brief names the loop-triage
// runbook, its .env holds the key, and `loop escalation judge` runs from
// its checkout with the key from that .env.
func TestC5C10WaitingEscalationStartsATriageRunWithTheJudgeKey(t *testing.T) {
	w := newWorld(t, acceptance.Options{Files: map[string]string{
		".loop/secrets.yaml": "secrets:\n  - name: TYPESAFE_API_KEY\n    stages: [fixer]\n",
	}})
	w.Must(w.HostCLI(strings.NewReader("tk-fixture-not-a-key"), "secret", "set", "TYPESAFE_API_KEY", "--project", acceptance.Project))
	ticket := createTicket(w, "Add a", "Create `a.txt` holding the line `a`.")
	run := w.Dispatch(machine.StageImplementer)
	if run.Item != ticket {
		t.Fatalf("the implementer took %s, want %s", run.Item, ticket)
	}
	impl, err := os.ReadFile(filepath.Join(run.Checkout, ".env"))
	if err == nil && strings.Contains(string(impl), "TYPESAFE_API_KEY=") {
		t.Fatal("the implementer's .env holds Jev's key, which the manifest gives to triage only")
	}
	id := escalationID(t, w.Must(w.In(run.Checkout, "escalation", "raise", "--score", "owner-only",
		"--body", "Which of two layouts the owner prefers.")))

	tri := w.Dispatch("triage")
	raw, err := os.ReadFile(filepath.Join(tri.Checkout, ".loop", "brief.json"))
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Stage   string `json:"stage"`
		Runbook string `json:"runbook"`
	}
	if err := json.Unmarshal(raw, &b); err != nil || b.Stage != "triage" || b.Runbook != ".claude/skills/loop-triage/SKILL.md" {
		t.Fatalf("the triage brief = %+v, %v, want stage triage and runbook loop-triage", b, err)
	}
	env, err := os.ReadFile(filepath.Join(tri.Checkout, ".env"))
	if err != nil || !strings.Contains(string(env), "TYPESAFE_API_KEY=") {
		t.Fatal("the triage run's .env does not name TYPESAFE_API_KEY")
	}

	j := w.In(tri.Checkout, "escalation", "judge", id, "--finding", "Which of two layouts the owner prefers.")
	if j.Code != 0 || j.Str("key_from") != ".env" || strings.Contains(j.Raw, "tk-fixture") {
		t.Fatalf("escalation judge from the triage run = exit %d, key_from %q", j.Code, j.Str("key_from"))
	}
}
