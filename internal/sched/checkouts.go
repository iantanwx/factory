package sched

import (
	"context"
	"database/sql"
	"os"
	"strings"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/store"
)

// Checkout states. The checkouts row of a run decides whether its
// checkout may go.
const (
	CheckoutActive  = "active"  // a run is starting or running in it
	CheckoutSettled = "settled" // its run finished; cleanup may delete it
	CheckoutKept    = "kept"    // its run ended with no finish; kept for a person
	CheckoutDeleted = "deleted"
)

func (s *Scheduler) setCheckoutState(ctx context.Context, run, state string) error {
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE checkouts SET state = ?, updated_at = ? WHERE run_id = ? AND state <> ?`,
			state, tx.Now().UnixMilli(), run, CheckoutDeleted)
		return err
	})
}

// CleanReport is what one cleanup pass did.
type CleanReport struct {
	Deleted []string          `json:"deleted"`
	Kept    []string          `json:"kept"`
	Errors  map[string]string `json:"errors"`
}

// CleanCheckouts deletes each settled checkout whose run ended, whose run
// holds no lease, that no open room uses, and that is clean and fully
// pushed. A checkout it cannot read is reported, and the others still go.
func (s *Scheduler) CleanCheckouts(ctx context.Context) CleanReport {
	rep := CleanReport{Errors: map[string]string{}}
	type row struct{ run, project, path string }
	var rows []row
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT c.run_id, c.project, c.path FROM checkouts c
			JOIN runs r ON r.run_id = c.run_id
			WHERE c.state = ? AND r.ended_at IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.owner_run_id = c.run_id)
			AND NOT EXISTS (SELECT 1 FROM rooms m WHERE m.checkout_id = c.id AND m.closed_at IS NULL)
			ORDER BY c.created_at, c.id`, CheckoutSettled)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.run, &r.project, &r.path); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return rs.Err()
	})
	if err != nil {
		rep.Errors[""] = err.Error()
		return rep
	}
	for _, r := range rows {
		ok, err := s.deletable(ctx, r.project, r.path)
		if err != nil {
			rep.Errors[r.path] = err.Error()
			s.log.WarnContext(ctx, "read a checkout", "path", r.path, "err", err)
			continue
		}
		if !ok {
			rep.Kept = append(rep.Kept, r.path)
			continue
		}
		if err := s.cfg.Checkouts(r.project).RemoveCheckout(ctx, r.path); err != nil {
			rep.Errors[r.path] = err.Error()
			continue
		}
		if err := s.setCheckoutState(ctx, r.run, CheckoutDeleted); err != nil {
			rep.Errors[r.path] = err.Error()
			continue
		}
		rep.Deleted = append(rep.Deleted, r.path)
	}
	return rep
}

func checkoutGit(ctx context.Context, dir string, args ...string) (string, error) {
	if _, err := os.Stat(dir); err != nil {
		return "", err
	}
	out, err := gitx.Run(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// deletable reports whether the checkout is clean and fully pushed. Its
// head is pushed when a remote-tracking ref holds it, or when the server
// pushed it itself: the server pushes from its mirror (KTD14), so the
// checkout's own remote refs may lag.
func (s *Scheduler) deletable(ctx context.Context, project, path string) (bool, error) {
	status, err := checkoutGit(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	if status != "" {
		return false, nil
	}
	ahead, err := checkoutGit(ctx, path, "rev-list", "HEAD", "--not", "--remotes")
	if err != nil {
		return false, err
	}
	if ahead == "" {
		return true, nil
	}
	head, err := checkoutGit(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	var n int
	err = s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM push_intents WHERE project = ? AND new_sha = ? AND state = 'done'`,
			project, head).Scan(&n)
	})
	return n > 0, err
}

// keptWork lists the kept checkouts of the item's earlier runs of the
// stage that still hold uncommitted work, oldest first. A run that ended
// with no finish leaves its work there, and the next run's brief names it.
// A checkout it cannot read is left out.
func (s *Scheduler) keptWork(ctx context.Context, ad admission.Admitted) []brief.KeptCheckout {
	var kept []brief.KeptCheckout
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT c.run_id, c.path, c.branch FROM checkouts c
			JOIN runs r ON r.run_id = c.run_id
			WHERE c.project = ? AND c.item = ? AND c.state = ? AND r.stage = ? AND c.run_id <> ?
			ORDER BY c.created_at, c.id`, ad.Project, ad.Item, CheckoutKept, ad.Stage, ad.RunID)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var k brief.KeptCheckout
			if err := rs.Scan(&k.RunID, &k.Path, &k.Branch); err != nil {
				return err
			}
			kept = append(kept, k)
		}
		return rs.Err()
	})
	if err != nil {
		s.log.WarnContext(ctx, "brief: read the kept checkouts", "item", ad.Item, "err", err)
		return nil
	}
	var out []brief.KeptCheckout
	for _, k := range kept {
		dirty, err := dirtyPaths(ctx, k.Path)
		if err != nil {
			s.log.WarnContext(ctx, "brief: read a kept checkout", "path", k.Path, "err", err)
			continue
		}
		if len(dirty) == 0 {
			continue
		}
		k.Dirty = dirty[:min(len(dirty), brief.MaxDirty)]
		k.More = len(dirty) - len(k.Dirty)
		out = append(out, k)
	}
	return out
}

// dirtyPaths lists each modified, added, deleted or untracked path in the
// checkout at dir, as git status names it. A rename names its new path.
func dirtyPaths(ctx context.Context, dir string) ([]string, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	out, err := gitx.Run(ctx, dir, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
		// A rename or a copy, in the index or the worktree column, is
		// followed by the path it came from.
		if strings.ContainsAny(e[:2], "RC") {
			i++
		}
	}
	return paths, nil
}
