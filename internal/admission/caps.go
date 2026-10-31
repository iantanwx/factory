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

// brainstormCounted are the epic states an agent moves on. An epic here holds
// a brainstorm slot. An epic that waits on a person (a sign-off, an
// architecture epic, an escalation) or on a blocker (backlog) holds none,
// and neither does one past spec review. Ported from CAP_STATES in
// scripts/sdlc/intake-cap.ts (alpha).
var brainstormCounted = []string{
	machine.PRDNeeded, machine.PRDDrafting, machine.PRDReview, machine.PRDAmending, machine.SpecNeeded, machine.SpecDrafting,
	machine.SpecReview,
}

// liveRuns counts the live runs of the limit's stages, except the run named
// by except. A limit that names a project counts that project's runs only.
// An agent job's limit names none, so it counts the whole instance.
func liveRuns(q querier, l config.Limit, except string) (int, error) {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(l.Stages)), ", ")
	query := `SELECT count(*) FROM runs WHERE ended_at IS NULL AND run_id <> ? AND stage IN (` + marks + `)`
	args := []any{except}
	for _, s := range l.Stages {
		args = append(args, s)
	}
	if l.Project != "" {
		query += ` AND project = ?`
		args = append(args, l.Project)
	}
	var n int
	err := q.QueryRow(query, args...).Scan(&n)
	return n, err
}

// capRefusal checks every limit of the stage. caller is the run that asks,
// which its own count leaves out: "" when no run asks, as in Admit before
// its reservation and in the scheduler's peek.
func capRefusal(q querier, stage string, limits []config.Limit, caller string) (*cli.Refusal, error) {
	for _, l := range limits {
		n, err := liveRuns(q, l, caller)
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
	if l.Project != "" {
		what += " of " + l.Project
	}
	return &cli.Refusal{Code: cli.CodeCapFull, Rule: "cap_full",
		Hint: fmt.Sprintf("%s fill the cap of %d in %s", what, l.Cap, fieldOf(l))}
}

// fieldOf names the config field that set the limit's cap, and the file
// that holds it.
func fieldOf(l config.Limit) string {
	if strings.HasPrefix(l.Field, "stages.") || strings.HasPrefix(l.Field, "pools.") {
		return l.Field + " in " + config.ProjectConfigPath
	}
	return l.Field + " in host config"
}

// countBrainstormEpics counts a project's epics in counted states.
func countBrainstormEpics(epics []machine.Item) int {
	n := 0
	for _, e := range epics {
		if slices.Contains(brainstormCounted, e.Status) {
			n++
		}
	}
	return n
}

// brainstormEpics counts the project's epics in the states the brainstorm cap
// counts. It reads beads, so no caller holds a lock.
func (a *Admission) brainstormEpics(ctx context.Context, project string) (int, *cli.Refusal) {
	all, ref := a.items.List(ctx, project, machine.ListFilter{Kind: machine.KindEpic})
	if ref != nil {
		return 0, ref
	}
	return countBrainstormEpics(all), nil
}

// brainstormFull refuses when the counted epics fill the brainstorm cap.
func brainstormFull(epics int, l config.Limit) *cli.Refusal {
	if epics < l.Cap {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeCapFull, Rule: "cap_full",
		Hint: fmt.Sprintf("%d epics in counted document states fill the cap of %d in %s", epics, l.Cap, fieldOf(l))}
}

// BrainstormCap refuses a new brainstorm with CAP_FULL while the project's epics
// in counted document states fill the brainstorm cap in force for the project
// (KTD30). It reads host config and the project's config at each call, so a
// changed cap applies with no restart. Brainstorm runs in a room, so `room open
// --kind brainstorm` asks it before the room opens. It checks the cap only: a
// room holds no run and no stage mutex.
func (a *Admission) BrainstormCap(ctx context.Context, project string) *cli.Refusal {
	if a.items == nil {
		return cli.InternalRefusal(errors.New("admission: no machine attached"))
	}
	limits, ref := a.limitsFor(ctx, project, config.StageBrainstorm)
	if ref != nil {
		return ref
	}
	epics, ref := a.brainstormEpics(ctx, project)
	if ref != nil {
		return ref
	}
	return brainstormFull(epics, limits[0])
}
