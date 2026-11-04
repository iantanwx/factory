package escalation

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// The server raises the escalation of a PRD reject cap, so no run scores
// it. It stands at the lowest level that reaches a person, as AE7 sent
// every cap to one, until the panel judges its findings.
var capTriage = Triage{Level: Levels[3], Choice: Inside}

// OpenCap is the machine's Applied hook for the PRD reject cap. The first
// cap of an epic waits for the triage panel: it opens an escalation whose
// finding is the reject, and holds the epic as raise does. Every other
// move writes nothing. Ported from fixer-author-gap.ts (alpha, TIC-263).
func OpenCap(tx *store.Tx, ev machine.AppliedEvent) error {
	if ev.Kind != machine.KindEpic || ev.Verb != "review_reject" || ev.When != machine.WhenPRDCap {
		return nil
	}
	review := strings.TrimSpace(ev.Fact(machine.FactReview))
	if review == "" {
		review = "The reject holds no text. `loop review rounds " + ev.Item + "` lists each verdict."
	}
	finding := "The doc-reviewer rejected the PRD a second time, which is its reject cap. " +
		"The findings are the numbered findings of the reject:\n\n" + review
	_, err := insert(tx, ev.Project, ev.Item, ev.RunID, machine.StageDocReviewer, capTriage, finding, tx.Now().UnixMilli())
	return err
}

// isCap reports whether rec is the escalation that OpenCap opened, and its
// epic still waits in the cap: the item's last change of status is the
// doc-reviewer run's reject that opened it.
func isCap(q Querier, rec Record) (bool, error) {
	var verb, to, run string
	err := q.QueryRow(`SELECT verb, to_status, run_id FROM transitions WHERE project = ? AND item = ? AND from_status <> to_status ORDER BY seq DESC LIMIT 1`,
		rec.Project, rec.Item).Scan(&verb, &to, &run)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return rec.Stage == machine.StageDocReviewer && verb == "review_reject" && to == machine.Escalated && run == rec.RunID, nil
}
