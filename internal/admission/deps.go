package admission

import (
	"context"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// DepReader reports the open dependencies of one item: the items it
// depends on that are not terminal. The beads client of v1 exposes no
// dependency read, so the reader is optional. Admission calls it outside
// every lock.
type DepReader interface {
	OpenDependencies(ctx context.Context, project, item string) ([]string, error)
}

// dependencyRefusal refuses an item with an open dependency. A ticket in
// backlog holds one by the ticket table: create puts it there, and only
// unblock moves it on. A ticket whose `## Depends on` names an open pull
// request holds one too, by pullHolds.
func (a *Admission) dependencyRefusal(ctx context.Context, it machine.Item) (*cli.Refusal, error) {
	var open []string
	if it.Kind == machine.KindTicket && it.Status == machine.Backlog {
		open = append(open, "a dependency that has not landed")
	}
	if a.cfg.Deps != nil {
		ids, err := a.cfg.Deps.OpenDependencies(ctx, it.Project, it.ID)
		if err != nil {
			return nil, fmt.Errorf("admission: read the dependencies of %s: %w", it.ID, err)
		}
		open = append(open, ids...)
	}
	pulls, err := a.pullHolds(ctx, it)
	if err != nil {
		return nil, err
	}
	open = append(open, pulls...)
	if len(open) == 0 {
		return nil, nil
	}
	return &cli.Refusal{Code: cli.CodeDependencyOpen, Rule: "dependency_open", Item: it.ID, CurrentStatus: it.Status,
		Hint: fmt.Sprintf("%s waits on %s", it.ID, strings.Join(open, ", "))}, nil
}
