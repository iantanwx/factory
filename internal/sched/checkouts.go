package sched

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"

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
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
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
