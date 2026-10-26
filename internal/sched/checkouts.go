package sched

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/store"
)

// Checkout states. The checkouts row of a run decides whether its
// checkout may go.
const (
	CheckoutActive  = "active"  // a run is starting or running in it
	CheckoutSettled = "settled" // its run finished; cleanup may delete it
	CheckoutKept    = "kept"    // its run ended with no finish, or its room closed; cleanup judges its content
	CheckoutDeleted = "deleted"
)

func (s *Scheduler) setCheckoutState(ctx context.Context, run, state string) error {
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE checkouts SET state = ?, updated_at = ? WHERE run_id = ? AND state <> ?`,
			state, tx.Now().UnixMilli(), run, CheckoutDeleted)
		return err
	})
}

// CleanReport is what one cleanup pass did. Kept maps each checkout a
// content rule keeps to its reason.
type CleanReport struct {
	Deleted []string          `json:"deleted"`
	Kept    map[string]string `json:"kept"`
	Errors  map[string]string `json:"errors"`
}

// CleanCheckouts judges each settled or kept checkout whose run ended or
// whose room closed, whose run holds no lease, and that no open room uses.
// One that is clean and fully pushed goes, and its alert closes. One that
// a content rule keeps stays, and an alert for a person names its path and
// the reason (F-9). One whose path is gone, because a person removed it,
// settles as deleted (F-34). A checkout it cannot read is reported, and
// the others still go. Then it judges each loop directory that no row
// names (cleanStrays).
func (s *Scheduler) CleanCheckouts(ctx context.Context) CleanReport {
	rep := CleanReport{Kept: map[string]string{}, Errors: map[string]string{}}
	type row struct{ id, run, project, path string }
	var rows []row
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT c.id, coalesce(c.run_id, ''), c.project, c.path FROM checkouts c
			WHERE c.state IN (?, ?)
			AND (EXISTS (SELECT 1 FROM runs r WHERE r.run_id = c.run_id AND r.ended_at IS NOT NULL)
				OR EXISTS (SELECT 1 FROM rooms m WHERE m.checkout_id = c.id AND m.closed_at IS NOT NULL))
			AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.owner_run_id = c.run_id)
			AND NOT EXISTS (SELECT 1 FROM rooms m WHERE m.checkout_id = c.id AND m.closed_at IS NULL)
			ORDER BY c.created_at, c.id`, CheckoutSettled, CheckoutKept)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.id, &r.run, &r.project, &r.path); err != nil {
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
		if _, err := os.Stat(r.path); errors.Is(err, fs.ErrNotExist) {
			if err := s.settleDeleted(ctx, r.id, r.path); err != nil {
				rep.Errors[r.path] = err.Error()
			}
			continue
		}
		why, err := s.keepReason(ctx, r.project, r.path)
		if err != nil {
			rep.Errors[r.path] = err.Error()
			s.log.WarnContext(ctx, "read a checkout", "path", r.path, "err", err)
			continue
		}
		if why != "" {
			rep.Kept[r.path] = why
			s.alertKept(ctx, r.project, r.id, r.run, r.path, why)
			continue
		}
		if err := s.cfg.Checkouts(r.project).RemoveCheckout(ctx, r.path); err != nil {
			rep.Errors[r.path] = err.Error()
			continue
		}
		if err := s.settleDeleted(ctx, r.id, r.path); err != nil {
			rep.Errors[r.path] = err.Error()
			continue
		}
		rep.Deleted = append(rep.Deleted, r.path)
	}
	s.cleanStrays(ctx, &rep)
	return rep
}

// settleDeleted records the checkout's row as deleted and closes its
// alert.
func (s *Scheduler) settleDeleted(ctx context.Context, id, path string) error {
	if err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE checkouts SET state = ?, updated_at = ? WHERE id = ?`, CheckoutDeleted, tx.Now().UnixMilli(), id)
		return err
	}); err != nil {
		return err
	}
	s.clearKept(ctx, id, path)
	return nil
}

func (s *Scheduler) clearKept(ctx context.Context, id, path string) {
	if err := s.cfg.Alerts.Clear(ctx, keptKey(id)); err != nil {
		s.log.ErrorContext(ctx, "close the kept checkout's alert", "path", path, "err", err)
	}
}

// cleanStrays judges each loop-* directory one level under a repository
// directory of agent_host.checkout_root that no checkouts row names, by
// the content rules of a row's checkout (F-34). A clean, fully pushed one
// goes. One that a rule keeps stays, and an alert names it. The
// coordinator's checkout and the operator's own worktrees are never read.
// It holds checkoutMu, so a run's new checkout is never a stray: dispatch
// records the row before it lets go.
func (s *Scheduler) cleanStrays(ctx context.Context, rep *CleanReport) {
	hc, err := s.cfg.Host.Load(ctx)
	if err != nil {
		rep.Errors[""] = err.Error()
		return
	}
	root := hc.AgentHost.CheckoutRoot
	if root == "" {
		return
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	dirs, err := agenthost.ListCheckoutDirs(root)
	if err != nil {
		rep.Errors[root] = err.Error()
		return
	}
	named := map[string]bool{}
	err = s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT path FROM checkouts WHERE state <> ?`, CheckoutDeleted)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var path string
			if err := rs.Scan(&path); err != nil {
				return err
			}
			named[filepath.Clean(path)] = true
		}
		return rs.Err()
	})
	if err != nil {
		rep.Errors[""] = err.Error()
		return
	}
	projects, err := s.cfg.Projects()
	if err != nil {
		rep.Errors[""] = err.Error()
		return
	}
	for _, dir := range dirs {
		if named[dir] || filepath.Base(dir) == coordinatorCheckout {
			continue
		}
		gitDirs, err := checkoutGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
		if err != nil {
			rep.Errors[dir] = err.Error()
			continue
		}
		gitDir, common, _ := strings.Cut(gitDirs, "\n")
		if gitDir == common {
			continue // a clone, not a checkout
		}
		// git names the common dir with each link resolved.
		project := ""
		for _, p := range projects {
			if real, err := filepath.EvalSymlinks(p.Path); err == nil && filepath.Join(real, ".git") == common {
				project = p.ID
				break
			}
		}
		why, err := s.keepReason(ctx, project, dir)
		if err != nil {
			rep.Errors[dir] = err.Error()
			continue
		}
		if why != "" {
			rep.Kept[dir] = why
			s.alertKept(ctx, project, dir, "a run that no checkouts row names", dir, why)
			continue
		}
		if project != "" {
			err = s.cfg.Checkouts(project).RemoveCheckout(ctx, dir)
		} else {
			_, err = gitx.Run(ctx, common, "worktree", "remove", dir)
		}
		if err != nil {
			rep.Errors[dir] = err.Error()
			continue
		}
		s.clearKept(ctx, dir, dir)
		rep.Deleted = append(rep.Deleted, dir)
	}
}

func keptKey(checkout string) string { return AlertCheckoutKept + ":" + checkout }

// alertKept raises the alert of a kept checkout once for each reason, so a
// pass that finds the same reason rings nobody again, and an answered alert
// stays answered until the reason changes.
func (s *Scheduler) alertKept(ctx context.Context, project, id, owner, path, why string) {
	body := fmt.Sprintf("The cleanup pass keeps the checkout at %s of %s, because %s. "+
		"Push the work, or remove it with `orca worktree rm --worktree path:%s --force`. "+
		"The next pass deletes a clean, fully pushed checkout, and this entry closes with it. "+
		"Answer this entry when you removed the checkout by hand.", path, owner, why, path)
	var n int
	if err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM alerts WHERE dedupe_key = ? AND body = ?`, keptKey(id), body).Scan(&n)
	}); err != nil {
		s.log.ErrorContext(ctx, "read alerts", "err", err)
		return
	}
	if n == 0 {
		s.raise(ctx, config.Alert{Project: project, Kind: AlertCheckoutKept, DedupeKey: keptKey(id),
			Subject: "checkout " + path + " stays: " + why, Body: body})
	}
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

// keepReason says why a content rule keeps the checkout, or "" when it is
// clean and fully pushed (tickertown reclaim rule 6). A commit is held
// when a remote-tracking ref holds it, when the base branch holds its
// patch (`git cherry`), since a rebase gives each commit a new id, or when
// the server pushed the head itself: the server pushes from its mirror
// (KTD14), so the checkout's own remote refs may lag.
func (s *Scheduler) keepReason(ctx context.Context, project, path string) (string, error) {
	status, err := checkoutGit(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "it holds uncommitted work", nil
	}
	n, err := unheld(ctx, path)
	if err != nil || n == 0 {
		return "", err
	}
	head, err := checkoutGit(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	var pushed int
	if err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM push_intents WHERE project = ? AND new_sha = ? AND state = 'done'`,
			project, head).Scan(&pushed)
	}); err != nil || pushed > 0 {
		return "", err
	}
	if n == 1 {
		return "it holds 1 commit that no remote and no base holds", nil
	}
	return fmt.Sprintf("it holds %d commits that no remote and no base hold", n), nil
}

// unheld counts the commits on HEAD that no remote-tracking ref and no
// base ref holds, less each one whose patch the base, or the branch's own
// published ref, holds under another id.
func unheld(ctx context.Context, path string) (int, error) {
	refs, err := checkoutGit(ctx, path, "for-each-ref", "--format=%(refname)",
		"refs/heads/"+DefaultBaseBranch, "refs/remotes/*/"+DefaultBaseBranch)
	if err != nil {
		return 0, err
	}
	bases := strings.Fields(refs)
	if branch, err := checkoutGit(ctx, path, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && branch != DefaultBaseBranch {
		published, err := checkoutGit(ctx, path, "for-each-ref", "--format=%(refname)", "refs/remotes/*/"+branch)
		if err != nil {
			return 0, err
		}
		bases = append(bases, strings.Fields(published)...)
	}
	out, err := checkoutGit(ctx, path, append([]string{"rev-list", "HEAD", "--not", "--remotes"}, bases...)...)
	if err != nil || out == "" {
		return 0, err
	}
	held := map[string]bool{}
	for _, ref := range bases {
		marks, err := checkoutGit(ctx, path, "cherry", ref, "HEAD")
		if err != nil {
			return 0, err
		}
		for line := range strings.Lines(marks) {
			if id, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok {
				held[id] = true
			}
		}
	}
	n := 0
	for _, id := range strings.Fields(out) {
		if !held[id] {
			n++
		}
	}
	return n, nil
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
