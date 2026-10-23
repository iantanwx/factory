package merge

import (
	"context"
	"database/sql"

	"github.com/iantanwx/loop/internal/store"
)

// Tracked is one PR the hand-off job tracks, as `loop merge status`
// prints it. Times are unix milliseconds.
type Tracked struct {
	Project   string `json:"project"`
	PR        int    `json:"pr"`
	Item      string `json:"item"`
	Head      string `json:"head"`
	Mechanism string `json:"mechanism"`
	State     string `json:"state"`
	// Since is when the PR entered State. A poll that reads pending
	// writes nothing, so a PR that waits keeps its first time.
	Since      int64 `json:"since"`
	EnqueuedAt int64 `json:"enqueued_at,omitempty"` // the last hand-off
	// Attempts counts each hand-off and each re-queue of the PR, at any
	// head.
	Attempts int    `json:"attempts"`
	Reason   string `json:"reason,omitempty"` // the reason of the last dequeue
}

// Status lists the PRs in the merge pipeline, the one in its state
// longest first, so a PR that waits too long leads the list. A mechanism
// holds each PR in state handed or requeued. all adds every PR that left
// the pipeline: merged, dequeued, withdrawn, closed or escalated. A
// project that is not empty narrows the list to that project.
func Status(ctx context.Context, st *store.Store, project string, all bool) ([]Tracked, error) {
	q := `SELECT project, pr_number, item, head_sha, mechanism, state, updated_at, coalesce(enqueued_at, 0), attempts, reason
		FROM merge_tracking WHERE (? = '' OR project = ?) AND (? OR state IN (?, ?)) ORDER BY updated_at, project, pr_number`
	out := []Tracked{}
	err := st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, project, project, all, stateHanded, stateRequeued)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var t Tracked
			if err := rows.Scan(&t.Project, &t.PR, &t.Item, &t.Head, &t.Mechanism, &t.State, &t.Since, &t.EnqueuedAt,
				&t.Attempts, &t.Reason); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}
