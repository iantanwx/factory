package retro

import (
	"context"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/store"
)

// StateFailed is a retro that admission.FailureLimit runs ended with no
// route. No run takes it again.
const StateFailed = "failed"

// AlertFailed is the kind of the alert that a failed retro raises.
const AlertFailed = "retro_failed"

// Release ends runID's hold on its retro when the run ended with no route.
// The retro counts one more attempt, and goes back to pending, or to failed
// at admission.FailureLimit. It returns the retro as it left it, and ok is
// false when the run held no running retro, as after a route.
func Release(ctx context.Context, st *store.Store, runID string) (r Retro, ok bool, err error) {
	err = st.Write(ctx, func(tx *store.Tx) error {
		r, ok, err = scanRetro(tx.QueryRow(`SELECT `+retroColumns+` FROM retros WHERE run_id = ? AND state = ?`, runID, StateRunning))
		if err != nil || !ok {
			return err
		}
		r.Attempts++
		r.State, r.RunID = StatePending, ""
		var finished any
		if r.Attempts >= admission.FailureLimit {
			r.State, finished = StateFailed, tx.Now().UnixMilli()
		}
		_, err := tx.Exec(`UPDATE retros SET state = ?, run_id = ?, attempts = ?, finished_at = ? WHERE project = ? AND item = ?`,
			r.State, r.RunID, r.Attempts, finished, r.Project, r.Item)
		return err
	})
	return r, ok, err
}
