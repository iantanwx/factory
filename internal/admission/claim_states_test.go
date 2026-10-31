package admission

import (
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// ClaimStates is the exported view of each stage's claim states, so a
// reader outside admission, such as overwatch's stalls part, lists no
// state of its own (docs/plans/factorio-a7r-overwatch-sweep-spec.md,
// Work entry 4).
func TestClaimStatesIsTheViewOfEachStagesClaimStates(t *testing.T) {
	want := map[string][]ClaimState{
		config.StageImplementer:  {{machine.KindTicket, machine.Todo}, {machine.KindTicket, machine.Repair}},
		config.StageCodeReviewer: {{machine.KindTicket, machine.InReview}},
		config.StageReproducer:   {{machine.KindBug, machine.Reported}},
		config.StageTriage:       nil,
		config.StageIntake:       nil,
	}
	for stage, w := range want {
		if got := ClaimStates(stage); !slices.Equal(got, w) {
			t.Errorf("ClaimStates(%s) = %v, want %v", stage, got, w)
		}
	}
	for _, stage := range config.RunStages() {
		var inner []ClaimState
		for _, s := range claimStates(stage) {
			inner = append(inner, ClaimState{Kind: s.kind, Status: s.status})
		}
		if got := ClaimStates(stage); !slices.Equal(got, inner) {
			t.Errorf("ClaimStates(%s) = %v, want claimStates' %v", stage, got, inner)
		}
	}
}
