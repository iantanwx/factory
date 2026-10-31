package escalation

import (
	"context"
	"strings"
	"testing"
)

// C-bug-1: a judge harness that exits non-zero leaves its last stderr line
// in the panel note, so the note says why the judge failed.
func TestCBug1FailedJudgeNoteCarriesItsLastStderrLine(t *testing.T) {
	const why = "ERROR: Your access token could not be refreshed because your refresh token was already used. Please log out and sign in again."
	sol := CommandJudge{JudgeName: "sol-6", Bin: "sh",
		Args: []string{"-c", "cat >/dev/null; echo 'reading prompt from stdin' >&2; echo '" + why + "' >&2; exit 1"}}
	_, h := judgeWorld(t, &fakeJev{}, fakeJudge{"fable-5", `{"level":"derived","choice":"inside","reason":"the ADR says so"}`, nil}, sol)
	res := h(context.Background(), inv(map[string]string{"finding": "f", "rubric": "rubric.md"}))
	if res.Refusal != nil {
		t.Fatal(res.Refusal)
	}
	notes := res.Data.(map[string]any)["notes"].([]string)
	for _, n := range notes {
		if strings.HasPrefix(n, "judge: sol-6") && strings.Contains(n, why) {
			return
		}
	}
	t.Fatalf("notes = %q, want a judge: sol-6 note that holds %q", notes, why)
}

// C-bug-1: the stderr line a failed judge leaves in its note never carries
// Jev's key.
func TestCBug1FailedJudgeNoteHidesTheJevKey(t *testing.T) {
	sol := CommandJudge{JudgeName: "sol-6", Bin: "sh",
		Args: []string{"-c", "cat >/dev/null; echo 'bad key jev-secret-key' >&2; echo >&2; exit 1"}}
	_, h := judgeWorld(t, &fakeJev{}, fakeJudge{"fable-5", `{"level":"derived","choice":"inside","reason":"the ADR says so"}`, nil}, sol)
	res := h(context.Background(), inv(map[string]string{"finding": "f", "rubric": "rubric.md"}))
	if res.Refusal != nil {
		t.Fatal(res.Refusal)
	}
	notes := res.Data.(map[string]any)["notes"].([]string)
	want := "judge: sol-6: exit status 1: bad key [REDACTED]"
	if len(notes) == 0 || notes[0] != want {
		t.Fatalf("notes = %q, want first note %q", notes, want)
	}
}
