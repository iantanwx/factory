package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/sched"
)

// checkoutPrefix starts the name of every checkout the loop makes:
// loop-<project>-<stage>-<run id> for a run (internal/sched/dispatch.go),
// loop-<room id> for a room (internal/room/room.go). The part reads no
// other directory, because the checkout root also holds the operator's own
// worktrees.
const checkoutPrefix = agenthost.CheckoutPrefix

// coordinatorCheckoutName is the name of the coordinator's checkout
// (internal/sched/coordinator.go). It is never work: no checkouts row names
// it, and the coordinator keeps it.
const coordinatorCheckoutName = "loop-coordinator"

// worktreesPart holds work for each leaked checkout (C16): a checkouts row
// whose run ended longer ago than the agent host's quiet period plus the
// cleanup interval, a loop-* directory that no row names, and a row whose
// path does not exist. A worktree of a repository that no project
// registers belongs to another loop server, and is no work (F-50).
func worktreesPart() Part {
	return Part{Name: "worktrees", Read: func(ctx context.Context, w World) (Reading, error) {
		host, err := w.Host(ctx)
		if err != nil {
			return Reading{}, fmt.Errorf("read host config: %w", err)
		}
		quiet := host.QuietPeriod
		if quiet == 0 {
			quiet = agenthost.DefaultQuietPeriod
		}
		rows, err := readCheckouts(ctx, w)
		if err != nil {
			return Reading{}, err
		}
		dirs, err := w.CheckoutDirs(ctx)
		if err != nil {
			return Reading{}, fmt.Errorf("list the checkout directories: %w", err)
		}
		projects, err := w.Projects(ctx)
		if err != nil {
			return Reading{}, fmt.Errorf("read the projects: %w", err)
		}
		dirs = slices.DeleteFunc(dirs, func(d string) bool {
			// A directory git cannot read, such as a plain one, stays work.
			common, project, err := sched.WorktreeOf(ctx, d, projects)
			return err == nil && common != "" && project == ""
		})
		return classifyWorktrees(w.Now(), quiet+w.Intervals().Cleanup, rows, dirs, w.Coordinator().Checkout), nil
	}}
}

// checkoutRow is one checkouts row that is not deleted, and when its run
// ended or its room closed.
type checkoutRow struct {
	path, run, state string
	ended            time.Time // zero while the run or the room lives
	exists           bool
}

// readCheckouts reads every checkouts row that is not deleted, and
// whether its path exists. A row whose name does not start loop- is left
// out.
func readCheckouts(ctx context.Context, w World) ([]checkoutRow, error) {
	var rows []checkoutRow
	err := w.Store().Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT c.path, coalesce(c.run_id, ''), c.state,
			coalesce((SELECT r.ended_at FROM runs r WHERE r.run_id = c.run_id),
				(SELECT max(m.closed_at) FROM rooms m WHERE m.checkout_id = c.id))
			FROM checkouts c WHERE c.state <> 'deleted' ORDER BY c.path`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var (
				r     checkoutRow
				ended sql.NullInt64
			)
			if err := rs.Scan(&r.path, &r.run, &r.state, &ended); err != nil {
				return err
			}
			if ended.Valid {
				r.ended = time.UnixMilli(ended.Int64).UTC()
			}
			rows = append(rows, r)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read the checkouts: %w", err)
	}
	out := rows[:0]
	for _, r := range rows {
		if !strings.HasPrefix(filepath.Base(r.path), checkoutPrefix) {
			continue
		}
		_, err := os.Stat(r.path)
		switch {
		case err == nil:
			r.exists = true
		case !errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("read the checkout %s: %w", r.path, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// classifyWorktrees is the worktrees part's rule. span is the agent host's
// quiet period plus the cleanup interval: a checkout whose run ended
// inside it is still the cleanup's to delete.
func classifyWorktrees(now time.Time, span time.Duration, rows []checkoutRow, dirs []string, coordinator string) Reading {
	isCoordinator := func(path string) bool {
		return filepath.Base(path) == coordinatorCheckoutName || coordinator != "" && filepath.Clean(path) == filepath.Clean(coordinator)
	}
	var r Reading
	seen := map[string]bool{}
	add := func(name, line string) {
		key := Key("worktrees", name)
		if seen[key] {
			return
		}
		seen[key] = true
		r.Findings = append(r.Findings, Finding{Key: key, Line: fmt.Sprintf("%s (%s)", line, key)})
	}
	named := map[string]bool{}
	for _, c := range rows {
		named[filepath.Clean(c.path)] = true
		if isCoordinator(c.path) {
			continue
		}
		name := filepath.Base(c.path)
		switch {
		case !c.exists:
			add(name, fmt.Sprintf("checkout %s of %s: its row is %s, and its path %s does not exist", name, c.run, c.state, c.path))
		case !c.ended.IsZero() && now.Sub(c.ended) > span:
			add(name, fmt.Sprintf("checkout %s of %s: its row is %s, and its run ended at %s, more than %s ago",
				name, c.run, c.state, c.ended.Format(time.RFC3339), span))
		}
	}
	for _, d := range dirs {
		name := filepath.Base(d)
		if !strings.HasPrefix(name, checkoutPrefix) || isCoordinator(d) || named[filepath.Clean(d)] {
			continue
		}
		add(name, fmt.Sprintf("directory %s: no checkout row names it", d))
	}
	r.Notes = append(r.Notes, fmt.Sprintf("checkout rows read: %d; loop directories read: %d", len(rows), len(dirs)))
	return r
}
