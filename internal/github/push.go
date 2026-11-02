package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/captures"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/shape"
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
	// Caller is the principal kind of the pusher. Only a cli.PrincipalRun
	// push takes the settings check: a room acts with the operator.
	Caller string
	// Prototype is a design room's push of the prototype: Branch is then
	// PrototypeBranchFor(Item), and no other.
	Prototype bool
	// Stage is the stage of the pusher's token. A doc-reviewer run only
	// reads, and each of its readers shares its token. A simplify run's
	// push takes the scope check, and the server holds its commit.
	Stage string
	// Captures is the sha of the capture commit with no parent in the
	// checkout, or "". A ticket that cites states pushes one at each head.
	Captures string
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
	// Held is true when the server holds the commit in the mirror until
	// the simplify run's hand-off, and pushed nothing to GitHub.
	Held bool `json:"held"`
	// Captures is the capture branch that holds the head's captures, for
	// a ticket that cites states.
	Captures *CapturePush `json:"captures,omitempty"`
}

// CapturePush is the capture branch after a push: its tip, and the states
// whose captures it holds under the head.
type CapturePush struct {
	Branch string   `json:"branch"`
	Tip    string   `json:"tip"`
	States []string `json:"states"`
}

// incomingRef holds a commit fetched from a checkout before its push.
func incomingRef(branch string) string { return "refs/loop/incoming/" + branch }

// heldRef holds a simplify run's commit in the mirror until the run's
// hand-off pushes it.
func heldRef(run string) string { return "refs/loop/held/" + run }

// remoteRef is the mirror's copy of a remote branch.
func remoteRef(branch string) string { return "refs/remotes/origin/" + branch }

// errCrash lets a test stop a push at a kill point, as a crash would.
var errCrash = errors.New("github: stopped at a kill point")

// Push runs `branch push` (KTD14). It fetches the head from the checkout
// into the mirror with no token, scans every new commit, records a push
// intent, and pushes from the mirror over the explicit URL with the App
// token, one refspec and a lease on the remote head it read. It never
// pushes the base branch, and it refuses a remote branch that holds
// commits the head lacks. A simplify run's commit goes to heldRef, not to
// GitHub, once it passes scopeCheck. A ticket that cites states pushes
// the captures of the head to its capture branch first, so the remote
// holds them before CI reads the head (factorio-4lso).
func (s *Service) Push(ctx context.Context, req PushRequest) (PushResult, error) {
	res := PushResult{Branch: req.Branch, Head: req.Head}
	if req.Stage == config.StageDocReviewer {
		return res, &RefusedError{Code: cli.CodeCapabilityDenied, Rule: "reviewer_reads_only",
			Hint: "a doc-reviewer run reads the document and pushes nothing; submit the verdict with `loop review submit`"}
	}
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
	case !slices.Contains(pushBranches(req.Item, req.Prototype), req.Branch):
		return res, &RefusedError{Code: cli.CodeCapabilityDenied, Rule: "not_the_item_branch",
			Hint: fmt.Sprintf("%s pushes only to its branch %s", req.Item, pushBranches(req.Item, req.Prototype)[0])}
	case !shaRe.MatchString(req.Head):
		return res, &RefusedError{Code: cli.CodeUsage, Rule: "bad_head", Hint: "the checkout's HEAD is not a full sha"}
	case req.Checkout == "":
		return res, &RefusedError{Code: cli.CodeUsage, Rule: "no_checkout", Hint: "push from inside the run's checkout"}
	}
	epic, states, err := s.citedStates(ctx, req)
	if err != nil {
		return res, err
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
	design := DesignEpicOf(req.Branch) == req.Item
	fetch := []string{base, req.Branch}
	if design {
		fetch = append(fetch, PrototypeBranchFor(req.Item))
	}
	if states != nil {
		fetch = append(fetch, captures.Branch(req.Item))
	}
	if err := m.Fetch(ctx, readTok, fetch...); err != nil {
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
	if remote != req.Head {
		if res.Commits, err = s.checkHead(ctx, m, p, req, base, remote, design); err != nil {
			return res, err
		}
	}
	if states != nil {
		set, err := m.captureSet(ctx, req, states)
		if err != nil {
			return res, err
		}
		if res.Captures, err = s.pushCaptures(ctx, p, m, req, epic, states, set); err != nil {
			return res, err
		}
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
	if req.Stage == config.StageSimplify {
		return s.hold(ctx, m, req, remoteRef(base), remote, res)
	}

	if err := s.pushBranch(ctx, p, m, req.Item, req.Branch, remote, req.Head, req.RunID, "pushed"); err != nil {
		return res, err
	}
	res.Pushed = true
	return res, nil
}

// pushBranch records the push intent of sha to branch, pushes it with a
// lease on old, and settles the intent. A crash test stops it at the kill
// point kill, after the push.
func (s *Service) pushBranch(ctx context.Context, p config.Project, m *Mirror, item, branch, old, sha, run, kill string) error {
	tok, err := s.Token(ctx, p, ScopePush)
	if err != nil {
		return err
	}
	id, err := s.recordPush(ctx, p.ID, item, branch, old, sha, run)
	if err != nil {
		return err
	}
	if err := m.Push(ctx, tok, sha, branch, old); err != nil {
		var ge *GitError
		if errors.As(err, &ge) && (strings.Contains(ge.Stderr, "stale info") || strings.Contains(ge.Stderr, "rejected")) {
			if serr := s.settlePush(ctx, id, "abandoned"); serr != nil {
				return serr
			}
			return &RefusedError{Code: cli.CodeRemoteAhead, Rule: "lease_lost", AllowedNext: []string{verbBranchSync},
				Hint: fmt.Sprintf("%s moved on the remote during the push; run `loop branch sync` and push again", branch)}
		}
		// The result is unknown: the intent stays pending, and the next
		// push or the reconcile reads the remote to settle it.
		return &UnavailableError{Err: err}
	}
	if s.crash != nil && s.crash(kill) {
		return errCrash
	}
	if err := s.settlePush(ctx, id, "done"); err != nil {
		return err
	}
	_, _ = s.git.run(ctx, gitRun{dir: m.Dir, args: []string{"update-ref", remoteRef(branch), sha}})
	return nil
}

// checkHead runs every check of a head that the remote lacks: the remote
// holds no commit the head lacks, each new commit passes the scan, a
// reviser's edits stay in one commit, and a design branch stays in its
// directory. It answers the count of new commits.
func (s *Service) checkHead(ctx context.Context, m *Mirror, p config.Project, req PushRequest, base, remote string, design bool) (int, error) {
	if remote != "" {
		lacking, err := m.Lacking(ctx, req.Head, remote)
		if err != nil {
			return 0, err
		}
		if len(lacking) > 0 {
			return 0, &RefusedError{Code: cli.CodeRemoteAhead, Rule: "remote_holds_more", AllowedNext: []string{verbBranchSync},
				Hint: fmt.Sprintf("%s holds %d commit(s) that HEAD lacks; run `loop branch sync`, merge, and push again", req.Branch, len(lacking))}
		}
	}
	var pins []string
	if s.cfg.UserSkills != nil {
		var err error
		if pins, err = s.cfg.UserSkills(ctx, p.ID); err != nil {
			return 0, err
		}
	}
	commits, err := m.Scan(ctx, s.cfg.Redactor, req.Caller == cli.PrincipalRun, pins, req.Head, remoteRef(base), remote)
	if err != nil {
		return 0, err
	}
	if err := s.oneEditSet(ctx, m, req, remoteRef(base), commits); err != nil {
		return 0, err
	}
	if design {
		if err := m.CheckDesignScope(ctx, req.Item, req.Head, remoteRef(base), remoteRef(PrototypeBranchFor(req.Item))); err != nil {
			return 0, err
		}
	}
	return len(commits), nil
}

// citedStates reads the states that the ticket of a push cites, from its
// screens fact, and its epic. The states are nil when the push takes no
// captures. It refuses captures on any branch but a ticket's own, and on
// a ticket that cites no state.
func (s *Service) citedStates(ctx context.Context, req PushRequest) (epic string, states []string, err error) {
	it, ref := s.item(ctx, req.Project, req.Item)
	if ref != nil {
		return "", nil, &RefusedError{Code: ref.Code, Rule: ref.Rule, Hint: ref.Hint}
	}
	if it.Kind != machine.KindTicket || req.Prototype || req.Branch != BranchFor(req.Item) {
		if req.Captures != "" {
			return "", nil, &RefusedError{Code: cli.CodeUsage, Rule: "captures_ticket_only",
				Hint: fmt.Sprintf("%s is no ticket's branch; only a ticket that cites states pushes captures", req.Branch)}
		}
		return "", nil, nil
	}
	screens := it.Fact(machine.FactScreens)
	if screens == "" {
		if req.Captures != "" {
			return "", nil, &RefusedError{Code: cli.CodeShapeRefused, Rule: "captures_not_cited",
				Hint: fmt.Sprintf("%s cites no state, so it has no captures; push with no --captures", req.Item)}
		}
		return "", nil, nil
	}
	if req.Captures != "" && !shaRe.MatchString(req.Captures) {
		return "", nil, &RefusedError{Code: cli.CodeUsage, Rule: "bad_captures", Hint: "the capture commit is not a full sha"}
	}
	return it.Fact(machine.FactEpic), strings.Split(screens, ","), nil
}

// captureSet fetches the capture commit of a push from the checkout, and
// checks its files against the cited states. It answers the entries of
// its tree. A push with no capture commit holds no capture.
func (m *Mirror) captureSet(ctx context.Context, req PushRequest, states []string) (map[string]string, error) {
	entries := map[string]string{}
	if req.Captures != "" {
		in := incomingRef(captures.Branch(req.Item))
		if err := m.FetchCheckout(ctx, req.Checkout, req.Captures, in); err != nil {
			return nil, &RefusedError{Code: cli.CodeUsage, Rule: "captures_unreadable",
				Hint: "the server could not read the capture commit from the checkout; push with --captures again"}
		}
		var err error
		if entries, err = m.rawTree(ctx, req.Captures); err != nil {
			return nil, err
		}
	}
	files := map[string][]byte{}
	for name, meta := range entries {
		data, err := m.blob(ctx, meta)
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
	if f := captures.Check(states, files); f != nil {
		hint := map[string]string{
			captures.RuleMissing: "the captures lack %s; run `loop captures take --out <dir>` at the head, and push with --captures <dir>",
			captures.RuleUnknown: "%s names no state the ticket cites; push one <state id>.png for each cited state and nothing else",
			captures.RuleNotPNG:  "%s is not a PNG; capture the state again",
		}[f.Rule]
		return nil, &RefusedError{Code: cli.CodeShapeRefused, Rule: f.Rule, Hint: fmt.Sprintf(hint, strings.Join(f.Names, ", "))}
	}
	return entries, nil
}

// pushCaptures writes set under <head>/ on the next commit of the
// ticket's capture branch, with the manifest at its root, and pushes that
// branch with a lease on the tip it read. A second push at one head
// replaces that head's captures. A tree that already holds them pushes
// nothing.
func (s *Service) pushCaptures(ctx context.Context, p config.Project, m *Mirror, req PushRequest, epic string, states []string, set map[string]string) (*CapturePush, error) {
	branch := captures.Branch(req.Item)
	tip := m.Resolve(ctx, remoteRef(branch))
	out := &CapturePush{Branch: branch, Tip: tip, States: states}
	if err := s.settlePending(ctx, p.ID, branch, tip); err != nil {
		return nil, err
	}
	manifest, err := json.MarshalIndent(captures.ManifestOf(req.Item, epic, states), "", "  ")
	if err != nil {
		return nil, err
	}
	blob, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"hash-object", "-w", "--stdin"}, stdin: append(manifest, '\n')})
	if err != nil {
		return nil, err
	}
	head, err := m.mkTree(ctx, set)
	if err != nil {
		return nil, err
	}
	root, err := m.rawTree(ctx, tip)
	if err != nil {
		return nil, err
	}
	root[req.Head] = "040000 tree " + head
	root[captures.ManifestFile] = "100644 blob " + blob
	tree, err := m.mkTree(ctx, root)
	if err != nil {
		return nil, err
	}
	parent := ""
	if tip != "" {
		cur, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"rev-parse", tip + "^{tree}"}})
		if err != nil || cur == tree {
			return out, err
		}
		parent = "parent " + tip + "\n"
	}
	at := s.cfg.Now().Unix()
	obj := fmt.Sprintf("tree %s\n%sauthor %s %d +0000\ncommitter %s %d +0000\n\nCaptures of %s at %s\n",
		tree, parent, evidenceIdent, at, evidenceIdent, at, req.Item, req.Head)
	next, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"hash-object", "-t", "commit", "-w", "--stdin"}, stdin: []byte(obj)})
	if err != nil {
		return nil, err
	}
	if err := s.pushBranch(ctx, p, m, req.Item, branch, tip, next, req.RunID, "captures_pushed"); err != nil {
		return nil, err
	}
	out.Tip = next
	return out, nil
}

// hold runs scopeCheck on a simplify run's push over the item's
// simplify_from, or over remote when the item names none, and keeps the
// head at heldRef. GitHub stays at simplify_from until the run's hand-off
// (pushHeld), so a run that ends with no hand-off leaves no commit there.
func (s *Service) hold(ctx context.Context, m *Mirror, req PushRequest, base, remote string, res PushResult) (PushResult, error) {
	it, ref := s.item(ctx, req.Project, req.Item)
	if ref != nil {
		return res, &RefusedError{Code: ref.Code, Rule: ref.Rule, Hint: ref.Hint}
	}
	from := it.Fact(machine.FactSimplifyFrom)
	if from == "" {
		from = remote
	}
	if err := m.scopeCheck(ctx, from, base, req.Head); err != nil {
		return res, err
	}
	if err := m.Keep(ctx, heldRef(req.RunID), req.Head); err != nil {
		return res, err
	}
	res.Held = true
	return res, nil
}

// pushHeld pushes the commit that run holds at heldRef to branch, with a
// lease on from, records the push intent, and deletes the ref. It answers
// the pushed head. With no held ref it answers the run's newest push to
// branch, which a first call of the hand-off made, or "" when the run
// pushed nothing. A branch that moved from from refuses with
// REMOTE_AHEAD, and the ref stays until the run's settlement.
func (s *Service) pushHeld(ctx context.Context, project, item, branch, run, from string) (string, error) {
	p, err := s.project(project)
	if err != nil {
		return "", err
	}
	unlock := s.lock(p.ID + "\x00" + branch)
	defer unlock()
	m := s.Mirror(p)
	if err := m.Init(ctx); err != nil {
		return "", err
	}
	held := m.Resolve(ctx, heldRef(run))
	if held == "" {
		return s.lastPush(ctx, p.ID, branch, run)
	}
	readTok, err := s.Token(ctx, p, ScopeRead)
	if err != nil {
		return "", err
	}
	if err := m.Fetch(ctx, readTok, branch); err != nil {
		return "", err
	}
	remote := m.Resolve(ctx, remoteRef(branch))
	if err := s.settlePending(ctx, p.ID, branch, remote); err != nil {
		return "", err
	}
	moved := &RefusedError{Code: cli.CodeRemoteAhead, Rule: "lease_lost",
		Hint: fmt.Sprintf("%s moved from %s on the remote, so the simplify commit cannot land; finish the run failed", branch, short(from))}
	switch remote {
	case held:
	case from:
		pushTok, err := s.Token(ctx, p, ScopePush)
		if err != nil {
			return "", err
		}
		id, err := s.recordPush(ctx, p.ID, item, branch, from, held, run)
		if err != nil {
			return "", err
		}
		if err := m.Push(ctx, pushTok, held, branch, from); err != nil {
			var ge *GitError
			if errors.As(err, &ge) && (strings.Contains(ge.Stderr, "stale info") || strings.Contains(ge.Stderr, "rejected")) {
				return "", errors.Join(moved, s.settlePush(ctx, id, "abandoned"))
			}
			return "", &UnavailableError{Err: err}
		}
		if s.crash != nil && s.crash("held_pushed") {
			return "", errCrash
		}
		if err := s.settlePush(ctx, id, "done"); err != nil {
			return "", err
		}
		_, _ = s.git.run(ctx, gitRun{dir: m.Dir, args: []string{"update-ref", remoteRef(branch), held}})
	default:
		return "", moved
	}
	return held, m.Keep(ctx, heldRef(run), "")
}

// DropHeld deletes the commit that run holds at heldRef, if any. The
// settlement of a simplify run calls it, so a commit that no hand-off
// pushed never reaches GitHub.
func (s *Service) DropHeld(ctx context.Context, project, run string) error {
	p, err := s.project(project)
	if err != nil {
		return err
	}
	m := s.Mirror(p)
	if m.Resolve(ctx, heldRef(run)) == "" {
		return nil
	}
	return m.Keep(ctx, heldRef(run), "")
}

// revisedDocument names the document of an epic that a reviser run
// revises, by the epic's state: the PRD in prd_amending, and the spec in
// spec_amending. It is nil in any other state. An epic with no PRD path
// on record names each PRD.
func revisedDocument(it machine.Item) func(path string) bool {
	switch {
	case it.Kind != machine.KindEpic:
		return nil
	case it.Status == machine.PRDAmending:
		if prd := it.Fact(machine.FactPRDPath); prd != "" {
			return func(p string) bool { return p == prd }
		}
		return func(p string) bool { return strings.HasPrefix(p, "docs/prd/") }
	case it.Status == machine.SpecAmending:
		return func(p string) bool {
			_, n := shape.SpecFile(it.ID, []shape.ChangedFile{{Path: p}})
			return n == 1
		}
	}
	return nil
}

// oneEditSet refuses a reviser run's push when the run's pushes, this one
// and each earlier one, add a second commit that changes its document
// (rule one_edit_set). Every change the reviser agrees with lands in one
// commit, and a merge of the base is no such commit.
func (s *Service) oneEditSet(ctx context.Context, m *Mirror, req PushRequest, base string, commits []string) error {
	if req.Caller != cli.PrincipalRun || req.RunID == "" {
		return nil
	}
	it, ref := s.item(ctx, req.Project, req.Item)
	if ref != nil {
		return &RefusedError{Code: ref.Code, Rule: ref.Rule, Hint: ref.Hint}
	}
	doc := revisedDocument(it)
	if doc == nil {
		return nil
	}
	type span struct{ old, new string }
	var earlier []span
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT old_sha, new_sha FROM push_intents
			WHERE project = ? AND branch = ? AND run_id = ? AND state = 'done'`, req.Project, req.Branch, req.RunID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var sp span
			if err := rows.Scan(&sp.old, &sp.new); err != nil {
				return err
			}
			earlier = append(earlier, sp)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	// This push excludes the remote, which holds each earlier push, so no
	// commit is read twice.
	for _, sp := range earlier {
		pushed, err := m.newCommits(ctx, sp.new, base, sp.old)
		if err != nil {
			return err
		}
		commits = append(pushed, commits...)
	}
	edits, err := m.EditCommits(ctx, doc, base, commits)
	if err != nil || len(edits) < 2 {
		return err
	}
	return &ScanError{Code: cli.CodeEditSetSplit, Rule: "one_edit_set", Commit: edits[1],
		Reason: fmt.Sprintf("the run's pushes hold %d commits that change its document, and every agreed change lands in one commit; "+
			"run `git reset --soft` to the item head that `loop branch sync` names, and commit once", len(edits))}
}

// pushBranches are the branches a push of item may write: its own, its
// spec's and its design record's, or for a prototype push the prototype's
// branch alone.
func pushBranches(item string, prototype bool) []string {
	if prototype {
		return []string{PrototypeBranchFor(item)}
	}
	return []string{BranchFor(item), SpecBranchFor(item), DesignBranchFor(item)}
}

// baseMergeRun names a push intent of MergeBase in place of a run.
const baseMergeRun = "server:merge-base"

// ErrHeadMoved means that the branch held another commit than the head
// MergeBase expected, so the merge commit holds that commit too.
var ErrHeadMoved = errors.New("github: the branch moved from the expected head")

// MergeBase merges the tip of base into the item's branch through
// GitHub's merges API, with a merge commit, and returns that commit. The
// branch must hold head: a merge of any other commit answers
// ErrHeadMoved. A branch that already holds the tip answers head. A
// conflict is the *APIError that GitHub answers, 409. The push intent
// comes before the call, so the reactor reads the new head as the
// server's own (KTD14). An unknown answer abandons it: a merge that
// GitHub made all the same reads as a foreign head, and the ticket
// returns to review. The branch lock keeps a reconcile from settling the
// intent while GitHub has not answered.
func (s *Service) MergeBase(ctx context.Context, p config.Project, item, base, head string) (string, error) {
	branch := BranchFor(item)
	unlock := s.lock(p.ID + "\x00" + branch)
	defer unlock()
	id, err := s.recordPush(ctx, p.ID, item, branch, head, "", baseMergeRun)
	if err != nil {
		return "", err
	}
	var out struct {
		SHA     string `json:"sha"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	_, err = s.repoDo(ctx, p, ScopePush, Call{Method: http.MethodPost, Path: repoPath(p, "/merges"),
		Body: map[string]string{"base": branch, "head": base, "commit_message": fmt.Sprintf("Merge %s into %s", base, branch)}}, &out)
	switch {
	case err != nil:
		return "", errors.Join(err, s.settlePush(ctx, id, "abandoned"))
	case out.SHA == "":
		return head, s.settlePush(ctx, id, "abandoned") // 204: nothing to merge
	case !shaRe.MatchString(out.SHA) || len(out.Parents) == 0:
		return "", errors.Join(&UnavailableError{Err: fmt.Errorf("the merge of %s into %s names no merge commit", base, branch)},
			s.settlePush(ctx, id, "abandoned"))
	}
	from := out.Parents[0].SHA
	err = s.cfg.Store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE push_intents SET old_sha = ?, new_sha = ?, state = 'done', settled_at = ? WHERE id = ?`,
			from, out.SHA, tx.Now().UnixMilli(), id)
		return err
	})
	if err == nil && from != head {
		err = ErrHeadMoved
	}
	return out.SHA, err
}

// BaseMerge reads the server's newest merge of the base into branch at
// from: the merge commit, or pending while GitHub has not answered the
// merge yet. sha is empty when no such merge stands.
func (s *Service) BaseMerge(ctx context.Context, project, branch, from string) (sha string, pending bool, err error) {
	var state string
	err = s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT new_sha, state FROM push_intents
			WHERE project = ? AND branch = ? AND old_sha = ? AND run_id = ? AND state IN ('pending', 'done')
			ORDER BY id DESC LIMIT 1`, project, branch, from, baseMergeRun).Scan(&sha, &state)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return sha, state == "pending", err
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
// or "" when it made none. A run other than "" limits it to the pushes of
// that run.
func (s *Service) lastPush(ctx context.Context, project, branch, run string) (string, error) {
	var sha string
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT new_sha FROM push_intents
			WHERE project = ? AND branch = ? AND state = 'done' AND (? = '' OR run_id = ?)
			ORDER BY id DESC LIMIT 1`, project, branch, run, run).Scan(&sha)
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

// prototypeSyncRef is the ref the mirror keeps for the head of an epic's
// prototype branch that the last `branch sync` read.
func prototypeSyncRef(epic string) string { return "refs/loop/prototypes/" + epic }

// capturesSyncRef is the ref the mirror keeps for the tip of a ticket's
// capture branch that the last `branch sync` read.
func capturesSyncRef(ticket string) string { return "refs/loop/captures/" + ticket }

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
	// Prototype is the head of the prototype's branch, for an epic in a
	// design state. Nil for any other item.
	Prototype *SyncItem `json:"prototype,omitempty"`
	// Captures is the tip of the capture branch, for a ticket that cites
	// states. Nil for any other item.
	Captures *SyncItem `json:"captures,omitempty"`
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
// keeps at the heads it read. With prototype, the branch of an epic's
// prototype, or capture, the capture branch of a ticket, it does the same
// for that branch. It lets a fetch from the
// mirror path ask for any object by sha, so a checkout can fetch a head
// by its sha. The checkout itself is never touched.
func (s *Service) Sync(ctx context.Context, p config.Project, item, branch, prototype, capture string) (SyncResult, error) {
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
	fetch := []string{base, branch}
	for _, b := range []string{prototype, capture} {
		if b != "" {
			fetch = append(fetch, b)
		}
	}
	if err := m.Fetch(ctx, tok, fetch...); err != nil {
		return SyncResult{}, err
	}
	if err := m.ServeLocalFetch(ctx); err != nil {
		return SyncResult{}, err
	}
	res := SyncResult{Mirror: m.Dir, Base: base, Ref: baseSyncRef, Item: SyncItem{ID: item, Branch: branch, Ref: itemSyncRef(item)}}
	res.SHA = m.Resolve(ctx, remoteRef(base))
	if err := m.Keep(ctx, baseSyncRef, res.SHA); err != nil {
		return SyncResult{}, err
	}
	res.Fetch = []string{"git", "fetch", m.Dir, "+" + baseSyncRef + ":refs/remotes/origin/" + base}
	if err := m.keepHead(ctx, &res.Item); err != nil {
		return SyncResult{}, err
	}
	if prototype != "" {
		res.Prototype = &SyncItem{ID: item, Branch: prototype, Ref: prototypeSyncRef(item)}
		if err := m.keepHead(ctx, res.Prototype); err != nil {
			return SyncResult{}, err
		}
	}
	if capture != "" {
		res.Captures = &SyncItem{ID: item, Branch: capture, Ref: capturesSyncRef(item)}
		if err := m.keepHead(ctx, res.Captures); err != nil {
			return SyncResult{}, err
		}
	}
	return res, nil
}

// keepHead points part.Ref at the head the mirror holds for part.Branch,
// and fills part's SHA and the fetch that brings that head into a
// checkout as origin/<branch>.
func (m *Mirror) keepHead(ctx context.Context, part *SyncItem) error {
	part.SHA = m.Resolve(ctx, remoteRef(part.Branch))
	part.Fetch = []string{}
	if err := m.Keep(ctx, part.Ref, part.SHA); err != nil {
		return err
	}
	if part.SHA != "" {
		part.Fetch = []string{"git", "fetch", m.Dir, "+" + part.Ref + ":refs/remotes/origin/" + part.Branch}
	}
	return nil
}

// FetchBase reads the head the remote holds for base, the project's base
// branch, into the mirror with the token, and copies that commit into the
// project's clone as refs/remotes/origin/<base>. It returns the head. A
// new checkout starts from it, never from the clone's local branch, which
// only a person moves (C-bug-1). The clone's local branches stay as they
// were.
func (s *Service) FetchBase(ctx context.Context, project, base string) (string, error) {
	p, err := s.project(project)
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
