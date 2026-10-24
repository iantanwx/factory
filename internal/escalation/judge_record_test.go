package escalation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// recordJudge answers the way a judge does when it reads the item: a
// finding that the operator's recorded decision answers is derived from
// the record, and one it cannot read is owner-only.
type recordJudge struct {
	name, decision string
	prompts        *[]string
}

func (r recordJudge) Name() string { return r.name }

func (r recordJudge) Ask(_ context.Context, prompt string) (string, error) {
	promptsMu.Lock()
	*r.prompts = append(*r.prompts, prompt)
	promptsMu.Unlock()
	if strings.Contains(prompt, r.decision) {
		return `{"level":"derived","choice":"inside","reason":"the operator's decision recorded on the item on 2026-10-24 answers F1"}`, nil
	}
	return `{"level":"owner-only","choice":"inside","reason":"only the operator can say which window the project keeps"}`, nil
}

// An escalation's item holds the operator's recorded answer to the
// finding. `loop escalation judge <escalation>` hands that decision to
// both judges, and the panel routes the finding to the loop and cites the
// decision (factorio-gf5, loop fault F-26).
func TestJudgeHandsTheJudgesTheOperatorsRecordedDecision(t *testing.T) {
	const decision = "Operator, 2026-10-24, on F1: keep the 14-day regression window, and do not ask again."
	v, ok := cli.Surface().Verb("escalation judge")
	if !ok {
		t.Fatal("no escalation judge verb")
	}
	if len(v.Args) != 1 || v.Args[0].Name != "escalation" {
		t.Fatalf("escalation judge takes args %+v; it must name the escalation, so it can read the item's recorded decisions", v.Args)
	}

	var prompts []string
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(JevKeyVar+"='jev-secret-key'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rubric.md"), []byte(testRubric), 0o600); err != nil {
		t.Fatal(err)
	}
	var asked []string
	j := Judging{
		Judges:       []Judge{recordJudge{"fable-5", decision, &prompts}, recordJudge{"sol-6", decision, &prompts}},
		Jev:          &fakeJev{reading: &Reading{Probabilities: map[string]float64{"0": 0, "1": 0, "2": 0.1, "3": 0.9}, Score: 2.9, Confidence: 0.95}},
		Getwd:        func() (string, error) { return dir, nil },
		FindCheckout: func(string) (string, error) { return dir, nil },
		// Recorded reads the operator's decisions recorded on the
		// escalation's item: notes that quote the operator, and earlier
		// inbox answers on it.
		Recorded: func(_ context.Context, escalation string) ([]string, error) {
			asked = append(asked, escalation)
			if escalation != "4" {
				return nil, fmt.Errorf("no escalation %q", escalation)
			}
			return []string{decision}, nil
		},
	}
	res := JudgeVerb(j)(context.Background(), cli.Invocation{Verb: v, Args: []string{"4"},
		Flags: map[string]string{"finding": "F1: which regression window does the project keep?", "rubric": "rubric.md", "out": filepath.Join(dir, "f1.json")}})
	if res.Refusal != nil {
		t.Fatalf("judge refused: %+v", res.Refusal)
	}
	if len(asked) != 1 || asked[0] != "4" {
		t.Fatalf("the verb read the record of %v, want escalation 4", asked)
	}
	if len(prompts) != 2 {
		t.Fatalf("%d judges were asked, want 2", len(prompts))
	}
	for _, p := range prompts {
		if !strings.Contains(p, decision) {
			t.Fatalf("a judge never read the operator's recorded decision; its prompt:\n%s", p)
		}
	}
	data := res.Data.(map[string]any)
	if d := data["decision"].(Decision); d.Route != RouteLoop {
		t.Fatalf("a finding the operator already answered routed to %q, want %q: %+v", d.Route, RouteLoop, d)
	}
	for _, a := range data["panel"].(Panel).Judges {
		if !strings.Contains(a.Reason, "decision recorded on the item") {
			t.Fatalf("judge %s routed without citing the decision: %q", a.Judge, a.Reason)
		}
	}
}

// `loop escalation judge` names its escalation. Without one it refuses
// with USAGE, before any judge is asked.
func TestJudgeWithoutAnEscalationIsRefused(t *testing.T) {
	var prompts []string
	_, h := judgeWorld(t, &fakeJev{}, fakeJudge{"a", "{}", &prompts}, fakeJudge{"b", "{}", &prompts})
	i := inv(map[string]string{"finding": "f", "rubric": "rubric.md"})
	i.Args = nil
	res := h(context.Background(), i)
	if res.Refusal == nil || res.Refusal.Code != cli.CodeUsage || res.Refusal.Rule != "no_escalation" || len(prompts) != 0 {
		t.Fatalf("res = %+v, prompts %d", res.Refusal, len(prompts))
	}
	if _, r := cli.Surface().Validate("escalation judge", nil, map[string]string{"finding": "f"}, "r"); r == nil || r.Refusal.Code != cli.CodeUsage {
		t.Fatalf("the surface took escalation judge with no escalation: %+v", r)
	}
}

// With no decision recorded on the item, a judge's prompt holds no
// decisions section, and reads as it did before.
func TestJudgePromptWithNoRecordedDecisionHasNoDecisionsSection(t *testing.T) {
	const want = "You are one of two independent judges. Another judge answers the same question separately,\n" +
		"and you will not see its answer. Score the finding below against the rubric that follows.\n\n" +
		"Answer the rubric's two questions about this one finding, and answer nothing else. Do not say\n" +
		"what the answer to the finding is. Say what kind of answer it needs.\n\n" +
		"Reply with one JSON object and no other text:\n" +
		`{"level":"<one level name>","choice":"inside|outside","reason":"<one sentence>"}` + "\n\n" +
		"Name the command, the rule or the arithmetic in your reason. A reason that names no evidence\n" +
		"is the reason the other judge cannot be compared against.\n\n" +
		"## The finding\n\nF1\n\n## The rubric\n\nRUBRIC"
	if got := JudgePrompt("RUBRIC", "F1", nil); got != want {
		t.Fatalf("prompt with no decision:\n%s", got)
	}
	got := JudgePrompt("RUBRIC", "F1", []string{"keep 14 days"})
	if !strings.Contains(got, "## Decisions the operator recorded on the item") || !strings.Contains(got, "### Decision 1\n\nkeep 14 days\n\n## The rubric") {
		t.Fatalf("prompt with one decision:\n%s", got)
	}
}

// A recorded decision that holds the Jev key reaches no judge with the
// key, and neither the panel file nor the output holds a decision.
func TestJudgeKeepsTheKeyAndTheDecisionsOutOfThePanel(t *testing.T) {
	var prompts []string
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(JevKeyVar+"='jev-secret-key'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rubric.md"), []byte(testRubric), 0o600); err != nil {
		t.Fatal(err)
	}
	const decision = "Operator: keep the window; the key was jev-secret-key"
	j := Judging{
		Judges: []Judge{fakeJudge{"a", `{"level":"derived","choice":"inside","reason":"the decision recorded on the item"}`, &prompts},
			fakeJudge{"b", `{"level":"derived","choice":"inside","reason":"the decision recorded on the item"}`, &prompts}},
		Jev:          &fakeJev{reading: &Reading{Probabilities: map[string]float64{"0": 0, "1": 0, "2": 0.1, "3": 0.9}, Score: 2.9, Confidence: 0.95}},
		Getwd:        func() (string, error) { return dir, nil },
		FindCheckout: func(string) (string, error) { return dir, nil },
		Recorded:     func(context.Context, string) ([]string, error) { return []string{decision}, nil },
	}
	out := filepath.Join(dir, "panel.json")
	res := JudgeVerb(j)(context.Background(), inv(map[string]string{"finding": "F1", "rubric": "rubric.md", "out": out}))
	if res.Refusal != nil {
		t.Fatal(res.Refusal)
	}
	for _, p := range prompts {
		if strings.Contains(p, "jev-secret-key") || !strings.Contains(p, "Operator: keep the window; the key was [REDACTED]") {
			t.Fatalf("judge prompt = %q", p)
		}
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(raw), fmtAny(res.Data)} {
		if strings.Contains(text, "jev-secret-key") || strings.Contains(text, "keep the window") {
			t.Fatalf("the panel or the output holds the key or a decision: %s", text)
		}
	}
}

// A refusal from the server's read of the decisions is the verb's answer,
// so a server that is not ready exits 4 and no judge is asked.
func TestJudgeAnswersTheServersRefusalOfTheRecord(t *testing.T) {
	var prompts []string
	dir, _ := judgeWorld(t, &fakeJev{})
	wait := cli.Reject(cli.CodeServerUnavailable, "server_unavailable", "the server is not ready")
	j := Judging{Judges: []Judge{fakeJudge{"a", "{}", &prompts}, fakeJudge{"b", "{}", &prompts}}, Jev: &fakeJev{},
		Getwd: func() (string, error) { return dir, nil }, FindCheckout: func(string) (string, error) { return dir, nil },
		Recorded: ServerRecorded(cli.Surface(), func(context.Context, cli.Invocation) cli.Result { return wait })}
	res := JudgeVerb(j)(context.Background(), inv(map[string]string{"finding": "F1", "rubric": "rubric.md"}))
	if res.Refusal == nil || res.Refusal.Code != cli.CodeServerUnavailable || len(prompts) != 0 {
		t.Fatalf("res = %+v, prompts %d", res.Refusal, len(prompts))
	}
}
