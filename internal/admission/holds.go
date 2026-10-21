package admission

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/iantanwx/loop/internal/cli"
)

// holdRefusal refuses new admission on an item that a room or an
// escalation holds (KTD23). U12 writes the holds table. A hold never
// revokes a running lease: the lease finishes first.
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

// stageFixer is the stage that routes escalations (U12).
const stageFixer = "fixer"

// panelsWaiting reports whether an escalation of the project waits for the
// fixer's panel. Peek admits no fixer run without one.
func panelsWaiting(q querier, project string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT EXISTS (SELECT 1 FROM escalations WHERE project = ? AND state = 'awaiting_panel')`, project).Scan(&n)
	return n == 1, err
}
