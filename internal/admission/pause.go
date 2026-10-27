package admission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/store"
)

// StagePause holds one run stage or agent job in one project, or in every
// project (ADR 0004 D3). While it holds, Peek and PeekAs find nothing and
// Admit refuses. A run of the stage that already started continues.
type StagePause struct {
	Project string    `json:"project"` // "" pauses every project
	Stage   string    `json:"stage"`
	Reason  string    `json:"reason"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
}

var (
	ErrAlreadyPaused = errors.New("admission: the stage is already paused in this scope")
	ErrNotPaused     = errors.New("admission: no pause of the stage in this scope")
)

// Pause writes the pause and its stage_paused audit row in one write, and
// returns the pause as stored. When a pause of the same scope holds, it
// writes nothing, and returns that pause with ErrAlreadyPaused.
func Pause(ctx context.Context, st *store.Store, p StagePause) (StagePause, error) {
	var held StagePause
	err := st.Write(ctx, func(tx *store.Tx) error {
		h, found, err := pauseOf(tx, p.Project, p.Stage)
		if err != nil {
			return err
		}
		if found {
			held = h
			return ErrAlreadyPaused
		}
		ms := tx.Now().UnixMilli()
		if _, err := tx.Exec(`INSERT INTO stage_pauses (project, stage, reason, paused_by, paused_at) VALUES (?, ?, ?, ?, ?)`,
			p.Project, p.Stage, p.Reason, p.By, ms); err != nil {
			return err
		}
		p.At = time.UnixMilli(ms).UTC()
		held = p
		return pauseAudit(tx, "stage_paused", p)
	})
	return held, err
}

// Resume deletes the pause of its own scope only, and writes one
// stage_resumed audit row by by with the deleted pause's reason. It returns
// the deleted pause, or ErrNotPaused when the scope holds none.
func Resume(ctx context.Context, st *store.Store, project, stage, by string) (StagePause, error) {
	var p StagePause
	err := st.Write(ctx, func(tx *store.Tx) error {
		var found bool
		var err error
		if p, found, err = pauseOf(tx, project, stage); err != nil {
			return err
		}
		if !found {
			return ErrNotPaused
		}
		if _, err := tx.Exec(`DELETE FROM stage_pauses WHERE project = ? AND stage = ?`, project, stage); err != nil {
			return err
		}
		return pauseAudit(tx, "stage_resumed", StagePause{Project: project, Stage: stage, Reason: p.Reason, By: by})
	})
	return p, err
}

// Pauses lists every pause, oldest first.
func Pauses(ctx context.Context, tx *sql.Tx) ([]StagePause, error) {
	rows, err := tx.QueryContext(ctx, `SELECT project, stage, reason, paused_by, paused_at FROM stage_pauses
		ORDER BY paused_at, project, stage`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []StagePause
	for rows.Next() {
		var p StagePause
		var ms int64
		if err := rows.Scan(&p.Project, &p.Stage, &p.Reason, &p.By, &ms); err != nil {
			return nil, err
		}
		p.At = time.UnixMilli(ms).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// Paused reports whether a pause of project or of every project holds the
// stage.
func (a *Admission) Paused(ctx context.Context, project, stage string) (bool, error) {
	var n int
	err := a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM stage_pauses WHERE stage = ? AND project IN ('', ?))`,
			stage, project).Scan(&n)
	})
	return n == 1, err
}

// pausedRefusal is Admit's answer for a paused stage. CAP_FULL already
// means that the stage starts no run now.
func pausedRefusal(project, stage string) *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeCapFull, Rule: "stage_paused",
		Hint: fmt.Sprintf("%s is paused in %s; `loop stage resume %s` ends the pause", stage, project, stage)}
}

func pauseOf(q querier, project, stage string) (StagePause, bool, error) {
	p := StagePause{Project: project, Stage: stage}
	var ms int64
	err := q.QueryRow(`SELECT reason, paused_by, paused_at FROM stage_pauses WHERE project = ? AND stage = ?`,
		project, stage).Scan(&p.Reason, &p.By, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return StagePause{}, false, nil
	}
	p.At = time.UnixMilli(ms).UTC()
	return p, err == nil, err
}

func pauseAudit(tx *store.Tx, action string, p StagePause) error {
	_, err := tx.Exec(`INSERT INTO audit (at, actor, project, action, detail) VALUES (?, ?, ?, ?,
		json_object('project', ?, 'stage', ?, 'reason', ?))`, tx.Now().UnixMilli(), p.By, p.Project, action, p.Project, p.Stage, p.Reason)
	return err
}
