package github

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// PushRequest is one `branch push`: the commit a checkout holds goes to
// the item's assigned branch.
type PushRequest struct {
	Project  string
	Item     string
	Branch   string // the item's assigned branch: BranchFor(Item), or SpecBranchFor(Item) for an epic's spec
	Checkout string // the run's or the room's checkout, from its token
	Head     string // the full sha the checkout holds
	RunID    string
}

// PushResult is what `branch push` answers.
type PushResult struct {
	Branch  string `json:"branch"`
	Head    string `json:"head"`
	Old     string `json:"old"`
	Pushed  bool   `json:"pushed"`
	Commits int    `json:"commits"`
	// Replayed is true when the remote already holds the head from an
	// earlier push of the same intent.
	Replayed bool `json:"-"`
}

// incomingRef holds a commit fetched from a checkout before its push.
func incomingRef(branch string) string { return "refs/loop/incoming/" + branch }

// remoteRef is the mirror's copy of a remote branch.
func remoteRef(branch string) string { return "refs/remotes/origin/" + branch }

// errCrash lets a test stop a push at a kill point, as a crash would.
var errCrash = errors.New("github: stopped at a kill point")

// Push runs `branch push` (KTD14). It fetches the head from the checkout
// into the mirror with no token, scans every new commit, records a push
// intent, and pushes from the mirror over the explicit URL with the App
// token, one refspec and a lease on the remote head it read. It never
// pushes the base branch, and it refuses a remote branch that holds
// commits the head lacks.
func (s *Service) Push(ctx context.Context, req PushRequest) (PushResult, error) {
	res := PushResult{Branch: req.Branch, Head: req.Head}
	p, err := s.project(req.Project)
	if err != nil {
		return res, err
	}
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return res, err
	}
	switch {
	case req.Branch == base:
		return res, &RefusedError{Code: cli.CodeCapabilityDenied, Rule: "base_branch",
			Hint: fmt.Sprintf("the loop never pushes the base branch %s; push the item's branch", base)}
	case req.Branch != BranchFor(req.Item) && req.Branch != SpecBranchFor(req.Item):
		return res, &RefusedError{Code: cli.CodeCapabilityDenied, Rule: "not_the_item_branch",
			Hint: fmt.Sprintf("%s pushes only to its branch %s", req.Item, BranchFor(req.Item))}
	case !shaRe.MatchString(req.Head):
		return res, &RefusedError{Code: cli.CodeUsage, Rule: "bad_head", Hint: "the checkout's HEAD is not a full sha"}
	case req.Checkout == "":
		return res, &RefusedError{Code: cli.CodeUsage, Rule: "no_checkout", Hint: "push from inside the run's checkout"}
	}
	unlock := s.lock(p.ID + "\x00" + req.Branch)
	defer unlock()

	m := s.Mirror(p)
	if err := m.Init(ctx); err != nil {
		return res, err
	}
	readTok, err := s.Token(ctx, p, ScopeRead)
	if err != nil {
		return res, err
	}
	if err := m.Fetch(ctx, readTok, base, req.Branch); err != nil {
		return res, err
	}
	if err := m.FetchCheckout(ctx, req.Checkout, req.Head, incomingRef(req.Branch)); err != nil {
		return res, &RefusedError{Code: cli.CodeUsage, Rule: "head_unreadable",
			Hint: "the server could not read the checkout's HEAD commit; commit the work and push again"}
	}
	if got := m.Resolve(ctx, incomingRef(req.Branch)); got != req.Head {
		return res, &RefusedError{Code: cli.CodeUsage, Rule: "head_moved", Hint: "the checkout's HEAD moved during the push; push again"}
	}
	remote := m.Resolve(ctx, remoteRef(req.Branch))
	res.Old = remote
	if err := s.settlePending(ctx, p.ID, req.Branch, remote); err != nil {
		return res, err
	}
	if remote == req.Head {
		own, err := s.ownPush(ctx, p.ID, req.Branch, req.Head)
		if err != nil {
			return res, err
		}
		// The remote already holds the head. When an earlier push of the
		// same head recorded it, this call repeats that push.
		res.Replayed = own
		res.Pushed = own
		return res, nil
	}
	if remote != "" {
		lacking, err := m.Lacking(ctx, req.Head, remote)
		if err != nil {
			return res, err
		}
		if len(lacking) > 0 {
			return res, &RefusedError{Code: cli.CodeRemoteAhead, Rule: "remote_holds_more", AllowedNext: []string{verbBranchSync},
				Hint: fmt.Sprintf("%s holds %d commit(s) that HEAD lacks; run `loop branch sync`, merge, and push again", req.Branch, len(lacking))}
		}
	}
	commits, err := m.Scan(ctx, s.cfg.Redactor, req.Head, remoteRef(base), remote)
	if err != nil {
		return res, err
	}
	res.Commits = len(commits)

	pushTok, err := s.Token(ctx, p, ScopePush)
	if err != nil {
		return res, err
	}
	id, err := s.recordPush(ctx, p.ID, req.Item, req.Branch, remote, req.Head, req.RunID)
	if err != nil {
		return res, err
	}
	if err := m.Push(ctx, pushTok, req.Head, req.Branch, remote); err != nil {
		var ge *GitError
		if errors.As(err, &ge) && (strings.Contains(ge.Stderr, "stale info") || strings.Contains(ge.Stderr, "rejected")) {
			if serr := s.settlePush(ctx, id, "abandoned"); serr != nil {
				return res, serr
			}
			return res, &RefusedError{Code: cli.CodeRemoteAhead, Rule: "lease_lost", AllowedNext: []string{verbBranchSync},
				Hint: fmt.Sprintf("%s moved on the remote during the push; run `loop branch sync` and push again", req.Branch)}
		}
		// The result is unknown: the intent stays pending, and the next
		// push or the reconcile reads the remote to settle it.
		return res, &UnavailableError{Err: err}
	}
	if s.crash != nil && s.crash("pushed") {
		return res, errCrash
	}
	if err := s.settlePush(ctx, id, "done"); err != nil {
		return res, err
	}
	_, _ = s.git.run(ctx, gitRun{dir: m.Dir, args: []string{"update-ref", remoteRef(req.Branch), req.Head}})
	res.Pushed = true
	return res, nil
}

// recordPush is the push intent of KTD14: the branch, the old sha and the
// new sha, recorded before the push.
func (s *Service) recordPush(ctx context.Context, project, item, branch, old, sha, run string) (int64, error) {
	var id int64
	err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		res, err := tx.Exec(`INSERT INTO push_intents (project, item, branch, old_sha, new_sha, run_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, project, item, branch, old, sha, run, tx.Now().UnixMilli())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

func (s *Service) settlePush(ctx context.Context, id int64, state string) error {
	return s.cfg.Store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE push_intents SET state = ?, settled_at = ? WHERE id = ? AND state = 'pending'`,
			state, tx.Now().UnixMilli(), id)
		return err
	})
}

// settlePending settles every pending push intent of one branch against
// the remote head: an intent whose new sha the remote holds is done, and
// any other is abandoned.
func (s *Service) settlePending(ctx context.Context, project, branch, remote string) error {
	type pending struct {
		id  int64
		sha string
	}
	var ps []pending
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, new_sha FROM push_intents WHERE project = ? AND branch = ? AND state = 'pending'`,
			project, branch)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.id, &p.sha); err != nil {
				return err
			}
			ps = append(ps, p)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, p := range ps {
		state := "abandoned"
		if p.sha == remote {
			state = "done"
		}
		if err := s.settlePush(ctx, p.id, state); err != nil {
			return err
		}
	}
	return nil
}

// ownPush reports whether the server pushed sha to the branch, or holds a
// pending push of it.
func (s *Service) ownPush(ctx context.Context, project, branch, sha string) (bool, error) {
	var n int
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM push_intents
			WHERE project = ? AND branch = ? AND new_sha = ? AND state IN ('pending', 'done')`, project, branch, sha).Scan(&n)
	})
	return n > 0, err
}

// lastPush is the sha of the newest push the server made to the branch,
// or "" when it made none.
func (s *Service) lastPush(ctx context.Context, project, branch string) (string, error) {
	var sha string
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT new_sha FROM push_intents
			WHERE project = ? AND branch = ? AND state = 'done' ORDER BY id DESC LIMIT 1`, project, branch).Scan(&sha)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return sha, err
}

// ReconcilePushes settles every pending push intent of one project
// against the remote. A start-up runs it before the reactor reads any
// head, so the server's own push never reads as foreign.
func (s *Service) ReconcilePushes(ctx context.Context, p config.Project) error {
	var branches []string
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT branch FROM push_intents WHERE project = ? AND state = 'pending'`, p.ID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var b string
			if err := rows.Scan(&b); err != nil {
				return err
			}
			branches = append(branches, b)
		}
		return rows.Err()
	})
	if err != nil || len(branches) == 0 {
		return err
	}
	tok, err := s.Token(ctx, p, ScopeRead)
	if err != nil {
		return err
	}
	m := s.Mirror(p)
	if err := m.Init(ctx); err != nil {
		return err
	}
	for _, b := range branches {
		unlock := s.lock(p.ID + "\x00" + b)
		remote, err := m.RemoteHead(ctx, tok, b)
		if err == nil {
			err = s.settlePending(ctx, p.ID, b, remote)
		}
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// baseSyncRef is the ref the mirror keeps for the base head that the last
// `branch sync` read. A checkout fetches it from the mirror path.
const baseSyncRef = "refs/loop/base"

// itemSyncRef is the ref the mirror keeps for the head of an item's
// branch that the last `branch sync` read.
func itemSyncRef(item string) string { return "refs/loop/items/" + item }

// SyncResult is what `branch sync` answers: a contract a checkout uses
// with no credential and no knowledge of the mirror's layout. Mirror is a
// path, and a fetch from it needs no token. The server keeps Ref for the
// base and Item.Ref for the item, and a fetch by SHA or by Item.SHA works
// too.
type SyncResult struct {
	Mirror string `json:"mirror"`
	Base   string `json:"base"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	// Fetch brings the base into the checkout as origin/<base>.
	Fetch []string `json:"fetch"`
	Item  SyncItem `json:"item"`
}

// SyncItem is the head of the item's branch. SHA is "" and Fetch is
// empty while the remote holds no such branch.
type SyncItem struct {
	ID     string `json:"id"`
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	// Fetch brings the head into the checkout as origin/<branch>.
	Fetch []string `json:"fetch"`
}

// Sync runs `branch sync` (KTD14). It fetches the base and the item's
// branch into the mirror with the token, then points the refs the server
// keeps at the heads it read. It lets a fetch from the mirror path ask
// for any object by sha, so a checkout can fetch a head by its sha. The
// checkout itself is never touched.
func (s *Service) Sync(ctx context.Context, p config.Project, item, branch string) (SyncResult, error) {
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return SyncResult{}, err
	}
	unlock := s.lock(p.ID + "\x00" + branch)
	defer unlock()
	m := s.Mirror(p)
	if err := m.Init(ctx); err != nil {
		return SyncResult{}, err
	}
	tok, err := s.Token(ctx, p, ScopeRead)
	if err != nil {
		return SyncResult{}, err
	}
	if err := m.Fetch(ctx, tok, base, branch); err != nil {
		return SyncResult{}, err
	}
	if err := m.ServeLocalFetch(ctx); err != nil {
		return SyncResult{}, err
	}
	res := SyncResult{Mirror: m.Dir, Base: base, Ref: baseSyncRef, Item: SyncItem{ID: item, Branch: branch, Ref: itemSyncRef(item), Fetch: []string{}}}
	res.SHA = m.Resolve(ctx, remoteRef(base))
	if err := m.Keep(ctx, baseSyncRef, res.SHA); err != nil {
		return SyncResult{}, err
	}
	res.Fetch = []string{"git", "fetch", m.Dir, "+" + baseSyncRef + ":refs/remotes/origin/" + base}
	res.Item.SHA = m.Resolve(ctx, remoteRef(branch))
	if err := m.Keep(ctx, res.Item.Ref, res.Item.SHA); err != nil {
		return SyncResult{}, err
	}
	if res.Item.SHA != "" {
		res.Item.Fetch = []string{"git", "fetch", m.Dir, "+" + res.Item.Ref + ":refs/remotes/origin/" + branch}
	}
	return res, nil
}

// FetchBase reads the head the remote holds for the project's base branch
// into the mirror with the token, and copies that commit into the
// project's clone as refs/remotes/origin/<base>. It returns the head. A
// new checkout starts from it, never from the clone's local branch, which
// only a person moves (C-bug-1). The clone's local branches stay as they
// were.
func (s *Service) FetchBase(ctx context.Context, project string) (string, error) {
	p, err := s.project(project)
	if err != nil {
		return "", err
	}
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return "", err
	}
	unlock := s.lock(p.ID + "\x00" + base)
	defer unlock()
	m := s.Mirror(p)
	if err := m.Init(ctx); err != nil {
		return "", err
	}
	tok, err := s.Token(ctx, p, ScopeRead)
	if err != nil {
		return "", err
	}
	if err := m.Fetch(ctx, tok, base); err != nil {
		return "", err
	}
	if err := m.ServeLocalFetch(ctx); err != nil {
		return "", err
	}
	sha := m.Resolve(ctx, remoteRef(base))
	if sha == "" {
		return "", fmt.Errorf("github: the remote holds no branch %s", base)
	}
	if _, err := s.git.run(ctx, gitRun{dir: p.Path, args: []string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules",
		"--no-write-fetch-head", m.Dir, "+" + sha + ":" + remoteRef(base)}}); err != nil {
		return "", err
	}
	return sha, nil
}
