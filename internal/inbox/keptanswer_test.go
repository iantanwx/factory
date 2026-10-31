package inbox

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// keptAnswer files the checkouts row r-kept at path, settled, with the
// open checkout_kept alert 154 that names it. It answers the alert and
// returns the row's state after the answer.
func keptAnswer(t *testing.T, path string) string {
	t.Helper()
	w := newArchWorld(t)
	if err := w.st.Write(w.ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(`INSERT INTO checkouts (id, project, item, path, run_id, state, created_at, updated_at)
			VALUES ('r-kept', ?, 'tk-1', ?, 'r-kept', 'settled', 0, 0)`, prj, path); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO alerts (id, project, kind, subject, dedupe_key, body, first_at, last_at)
			VALUES (154, ?, 'checkout_kept', 'checkout stays', 'checkout_kept:r-kept', 'remove it by hand', 0, 0)`, prj)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inv, bad := cli.Surface().Validate("inbox answer", []string{"alert/154"}, map[string]string{"body": "removed the checkout by hand"}, "r")
	if bad != nil {
		t.Fatal(bad)
	}
	if res := w.in.answer(w.ctx, &rpc.Call{Inv: inv}); res.Refusal != nil {
		t.Fatalf("answer = %+v", res.Refusal)
	}
	var state string
	if err := w.st.Read(w.ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(w.ctx, `SELECT state FROM checkouts WHERE id = 'r-kept'`).Scan(&state)
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

// F-62: a checkout_kept alert asks a person to remove the checkout by hand
// and to answer the entry. Once the path is gone, the answer settles the
// checkout's row as deleted, so the overwatch sweep reports no row whose
// path does not exist.
func TestAnAnswerToAKeptAlertSettlesTheRowOfAPathRemovedByHand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loop-factorio-prd-author-r-kept") // removed by hand: it does not exist
	if state := keptAnswer(t, path); state != "deleted" {
		t.Fatalf("the row of the removed checkout is %q after the answer, want deleted", state)
	}
}

// C-bug-1: an answer while the checkout's path still exists leaves its row
// as it is, and the cleanup pass judges the checkout.
func TestAnAnswerToAKeptAlertKeepsTheRowOfAPathThatStands(t *testing.T) {
	if state := keptAnswer(t, t.TempDir()); state != "settled" {
		t.Fatalf("the row of the standing checkout is %q after the answer, want settled", state)
	}
}
