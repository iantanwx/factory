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

// mutexStages have one holder for each project at a time. Two intake runs
// that overlap could create one epic twice (scripts/sdlc/intake-cap.ts,
// alpha). The stage_mutexes row replaces the mutex issue.
var mutexStages = []string{config.StageIntake}

func isMutexStage(stage string) bool { return slices.Contains(mutexStages, stage) }

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
