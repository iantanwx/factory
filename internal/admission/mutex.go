package admission

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

// mutexStages have one holder for each project at a time, whatever cap
// host config holds. Two intake runs that overlap could create one epic
// twice (scripts/sdlc/intake-cap.ts, alpha). The stage_mutexes row
// replaces the mutex issue. The spec author and the reproducer keep the
// one-run guard of scripts/sdlc/stage-claims.ts (alpha): two spec authors
// would each take an epic and break the stage's cadence, and a second
// reproducer would take the next report from a stale queue read while the
// first still reads it (timer-reproducer-row.ts, alpha). The unattended
// brainstorm keeps the same guard for the spec author's reason: two runs would each
// take an epic and break the stage's cadence (ADR 0001, spec factorio-4n0
// Decisions).
var mutexStages = []string{config.StageIntake, config.StageSpecAuthor, config.StageReproducer, config.StageUnattendedBrainstorm}

// MutexStage reports whether stage is a mutex stage.
func MutexStage(stage string) bool { return slices.Contains(mutexStages, stage) }

// mutexHolder returns the run that holds the project's stage mutex, or "".
func mutexHolder(q querier, project, stage string) (string, error) {
	var holder string
	err := q.QueryRow(`SELECT holder_run_id FROM stage_mutexes WHERE project = ? AND stage = ?`, project, stage).Scan(&holder)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return holder, err
}

func mutexHeld(project, stage, holder string) *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeMutexHeld, Rule: "mutex_held",
		Hint: fmt.Sprintf("run %s holds the %s mutex of project %s until it settles", holder, stage, project)}
}

func takeMutex(tx *store.Tx, project, stage, run string) error {
	_, err := tx.Exec(`INSERT INTO stage_mutexes (project, stage, holder_run_id, acquired_at) VALUES (?, ?, ?, ?)`,
		project, stage, run, tx.Now().UnixMilli())
	return err
}

// releaseMutexes frees every stage mutex the run holds.
func releaseMutexes(tx *store.Tx, run string) error {
	_, err := tx.Exec(`DELETE FROM stage_mutexes WHERE holder_run_id = ?`, run)
	return err
}
