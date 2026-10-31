package escalation

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// C1: the live panel holds exactly two judges, fable-5 and opus-5, and
// neither runs codex.
func TestC1LivePanelHoldsFable5AndOpus5WithNoCodex(t *testing.T) {
	var names []string
	for _, j := range Live().Judges {
		c := j.(CommandJudge)
		names = append(names, c.JudgeName)
		if c.Bin == "codex" || slices.Contains(c.Args, "codex") {
			t.Fatalf("judge %s runs codex: %s %q", c.JudgeName, c.Bin, c.Args)
		}
	}
	if !slices.Equal(names, []string{"fable-5", "opus-5"}) {
		t.Fatalf("Live() judges = %q, want fable-5 and opus-5", names)
	}
}

// C2: JudgeBins names each binary of the live panel once, so a panel of
// two claude judges needs claude only.
func TestC2JudgeBinsNamesClaudeOnce(t *testing.T) {
	if got := JudgeBins(); !slices.Equal(got, []string{"claude"}) {
		t.Fatalf("JudgeBins() = %q, want [claude]", got)
	}
}

// C3: on a host with claude and no codex on the path, the live panel
// records both judges' answers, and Jev is asked.
func TestC3LivePanelWithNoCodexRecordsBothAnswersAndAsksJev(t *testing.T) {
	bin := t.TempDir()
	reply := `{"type":"result","result":"{\"level\":\"derived\",\"choice\":\"inside\",\"reason\":\"the ADR says so\"}"}`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat >/dev/null\necho '"+reply+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	jev := &fakeJev{reading: &Reading{Probabilities: map[string]float64{"2": 1}, Score: 2, Confidence: 0.9}}
	_, h := judgeWorld(t, jev, Live().Judges...)
	res := h(context.Background(), inv(map[string]string{"finding": "f", "rubric": "rubric.md"}))
	if res.Refusal != nil {
		t.Fatal(res.Refusal)
	}
	data := res.Data.(map[string]any)
	var names []string
	for _, a := range data["panel"].(Panel).Judges {
		names = append(names, a.Judge)
	}
	if !slices.Equal(names, []string{"fable-5", "opus-5"}) {
		t.Fatalf("panel judges = %q, notes = %q", names, data["notes"])
	}
	if len(jev.keys) != 1 {
		t.Fatalf("Jev was asked %d times, want once", len(jev.keys))
	}
}
