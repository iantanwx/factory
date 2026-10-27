package escalation

import (
	"slices"
	"testing"
)

// C21: JudgeBins names the binary of each judge of the live panel, in
// panel order, so a release that changes a judge changes the doctor line.
func TestC21JudgeBinsFollowTheLivePanel(t *testing.T) {
	var want []string
	for _, j := range Live().Judges {
		want = append(want, j.(CommandJudge).Bin)
	}
	if got := JudgeBins(); !slices.Equal(got, want) || !slices.Equal(got, []string{"claude", "codex"}) {
		t.Fatalf("JudgeBins() = %q, the live panel runs %q", got, want)
	}
}
