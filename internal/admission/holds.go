package admission

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// holdRefusal refuses new admission on an item that a room or an
// escalation holds (KTD23). Rooms and escalations write the holds table.
// A hold never
// revokes a running lease: the lease finishes first. A blocked finish
// holds an item at one stage only, by blockedRefusal.
func holdRefusal(q querier, project, item string) (*cli.Refusal, error) {
	var holder, kind string
	err := q.QueryRow(`SELECT holder, kind FROM holds WHERE project = ? AND item = ? ORDER BY created_at, holder LIMIT 1`,
		project, item).Scan(&holder, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cli.Refusal{Code: cli.CodeMutexHeld, Rule: "item_held", Item: item,
		Hint: fmt.Sprintf("%s %s holds %s; no stage takes it until the %s lets it go", kind, holder, item, kind)}, nil
}

// stageFixer is the stage that routes escalations.
const stageFixer = "fixer"

// waiting lists each stage that claims no item and has work only while a
// row of its project waits for it. Peek admits no run of such a stage
// without one.
var waiting = map[string]func(q querier, project string) (bool, error){
	stageFixer:        panelsWaiting,
	config.StageRetro: retrosWaiting,
}

// panelsWaiting reports whether an escalation of the project waits for the
// fixer's panel.
func panelsWaiting(q querier, project string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT EXISTS (SELECT 1 FROM escalations WHERE project = ? AND state = 'awaiting_panel')`, project).Scan(&n)
	return n == 1, err
}

// retrosWaiting reports whether a retro of the project waits for a run.
func retrosWaiting(q querier, project string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT EXISTS (SELECT 1 FROM retros WHERE project = ? AND state = 'pending')`, project).Scan(&n)
	return n == 1, err
}

// AlertBlocked is the kind of the alert that the scheduler raises when a
// run on an item finishes blocked. The open alert is also the hold: the
// run asked for something outside it, and another run at the same inputs
// and in the same status meets the same wall.
const AlertBlocked = "run_blocked"

// BlockedKey is the dedupe key of the blocked alert of one item and stage
// at these inputs. Changed inputs make another key, so they lift the hold.
func BlockedKey(project, item, stage, inputs string) string {
	return AlertBlocked + ":" + project + "/" + item + "/" + stage + "/" + inputs
}

// blockedRefusal refuses an item at a stage whose last run there finished
// blocked at these inputs. The hold lasts while its alert is open and the
// item has kept its status since the alert was last raised. So changed
// inputs, a move of the item, or an answer to the alert lets the stage
// take the item again.
func blockedRefusal(q querier, project, item, stage, inputs string) (*cli.Refusal, error) {
	var id int64
	err := q.QueryRow(`SELECT a.id FROM alerts a WHERE a.dedupe_key = ? AND a.state = 'open'
		AND NOT EXISTS (SELECT 1 FROM transitions t WHERE t.project = ? AND t.item = ? AND t.at >= a.last_at
			AND t.from_status <> t.to_status)`,
		BlockedKey(project, item, stage, inputs), project, item).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cli.Refusal{Code: cli.CodeMutexHeld, Rule: "blocked_hold", Item: item,
		Hint: fmt.Sprintf("the last %s run on %s finished blocked, and its inputs and status have not changed since; an answer to alert/%d lifts the hold",
			stage, item, id)}, nil
}

// mayBeBlocked reports whether a blocked alert of the item and stage is
// open at any inputs. Only then is the item's identity worth an outside
// read.
func mayBeBlocked(q querier, project, item, stage string) (bool, error) {
	prefix := BlockedKey(project, item, stage, "")
	var n int
	err := q.QueryRow(`SELECT EXISTS (SELECT 1 FROM alerts WHERE kind = ? AND state = 'open' AND substr(dedupe_key, 1, ?) = ?)`,
		AlertBlocked, len(prefix), prefix).Scan(&n)
	return n == 1, err
}
