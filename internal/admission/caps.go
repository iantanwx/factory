package admission

import (
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// stageIntake is the stage whose cap counts epics, not runs (KTD30).
const stageIntake = "intake"

// intakeCounted are the epic states an agent moves on. An epic here holds
// an intake slot. An epic that waits on a person (a sign-off, an
// architecture epic, an escalation) holds none, and neither does one past
// spec review. Ported from CAP_STATES in scripts/sdlc/intake-cap.ts (alpha).
var intakeCounted = []string{
	machine.PRDReview, machine.PRDAmending, machine.SpecNeeded, machine.SpecDrafting, machine.SpecReview,
}

// liveRuns counts the runs of the stages that have not ended, on the whole
// instance. Host config is instance scope, so its caps are too.
func liveRuns(q querier, stages []string) (int, error) {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(stages)), ", ")
	args := make([]any, len(stages))
	for i, s := range stages {
		args[i] = s
	}
	var n int
	err := q.QueryRow(`SELECT count(*) FROM runs WHERE ended_at IS NULL AND stage IN (`+marks+`)`, args...).Scan(&n)
	return n, err
}

// capRefusal checks every limit of the stage. extra is the run this call
// already reserved, which counts as live: 0 before a reservation, 1 after.
func capRefusal(q querier, stage string, limits []config.Limit, extra int) (*cli.Refusal, error) {
	for _, l := range limits {
		n, err := liveRuns(q, l.Stages)
		if err != nil {
			return nil, err
		}
		if n-extra >= l.Cap {
			return capFull(stage, l, n-extra), nil
		}
	}
	return nil, nil
}

func capFull(stage string, l config.Limit, n int) *cli.Refusal {
	what := fmt.Sprintf("%d live %s runs", n, stage)
	if l.Name != stage {
		what = fmt.Sprintf("%d live runs in the %s pool (%s)", n, l.Name, strings.Join(l.Stages, ", "))
	}
	return &cli.Refusal{Code: cli.CodeCapFull, Rule: "cap_full",
		Hint: fmt.Sprintf("%s fill the cap of %d in host config throughput.%s", what, l.Cap, l.Name)}
}

// countIntakeEpics counts a project's epics in counted states. It reads
// beads, so no caller holds a lock.
func countIntakeEpics(epics []machine.Item) int {
	n := 0
	for _, e := range epics {
		if slices.Contains(intakeCounted, e.Status) {
			n++
		}
	}
	return n
}
