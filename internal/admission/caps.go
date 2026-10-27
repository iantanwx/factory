package admission

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// intakeCounted are the epic states an agent moves on. An epic here holds
// an intake slot. An epic that waits on a person (a sign-off, an
// architecture epic, an escalation) or on a blocker (backlog) holds none,
// and neither does one past spec review. Ported from CAP_STATES in
// scripts/sdlc/intake-cap.ts (alpha).
var intakeCounted = []string{
	machine.PRDNeeded, machine.PRDReview, machine.PRDAmending, machine.SpecNeeded, machine.SpecDrafting, machine.SpecReview,
}

// liveRuns counts the runs of the stages that have not ended, on the whole
// instance, except the run named by except. Host config is instance scope,
// so its caps are too.
func liveRuns(q querier, stages []string, except string) (int, error) {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(stages)), ", ")
	args := []any{except}
	for _, s := range stages {
		args = append(args, s)
	}
	var n int
	err := q.QueryRow(`SELECT count(*) FROM runs WHERE ended_at IS NULL AND run_id <> ? AND stage IN (`+marks+`)`, args...).Scan(&n)
	return n, err
}

// capRefusal checks every limit of the stage. caller is the run that asks,
// which its own count leaves out: "" when no run asks, as in Admit before
// its reservation and in the scheduler's peek.
func capRefusal(q querier, stage string, limits []config.Limit, caller string) (*cli.Refusal, error) {
	for _, l := range limits {
		n, err := liveRuns(q, l.Stages, caller)
		if err != nil {
			return nil, err
		}
		if n >= l.Cap {
			return capFull(stage, l, n), nil
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

// countIntakeEpics counts a project's epics in counted states.
func countIntakeEpics(epics []machine.Item) int {
	n := 0
	for _, e := range epics {
		if slices.Contains(intakeCounted, e.Status) {
			n++
		}
	}
	return n
}

// intakeEpics counts the project's epics in counted states. It reads
// beads, so no caller holds a lock.
func (a *Admission) intakeEpics(ctx context.Context, project string) (int, *cli.Refusal) {
	all, ref := a.items.List(ctx, project, machine.ListFilter{Kind: machine.KindEpic})
	if ref != nil {
		return 0, ref
	}
	return countIntakeEpics(all), nil
}

// intakeFull refuses when the counted epics fill the intake cap.
func intakeFull(epics int, l config.Limit) *cli.Refusal {
	if epics < l.Cap {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeCapFull, Rule: "cap_full",
		Hint: fmt.Sprintf("%d epics in counted document states fill the cap of %d in host config throughput.%s", epics, l.Cap, l.Name)}
}

// IntakeCap refuses a new intake with CAP_FULL while the project's epics
// in counted document states fill the intake cap (KTD30). It reads host
// config at each call, so a changed cap applies with no restart. Intake
// runs in a room, so `room open --kind intake` asks it before the room
// opens. It checks the cap only: a room holds no run and no stage mutex.
func (a *Admission) IntakeCap(ctx context.Context, project string) *cli.Refusal {
	if a.items == nil {
		return cli.InternalRefusal(errors.New("admission: no machine attached"))
	}
	limits, ref := a.limitsFor(ctx, config.StageIntake)
	if ref != nil {
		return ref
	}
	epics, ref := a.intakeEpics(ctx, project)
	if ref != nil {
		return ref
	}
	return intakeFull(epics, limits[0])
}
