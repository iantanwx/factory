package github

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// simplifyRun is the run id of the simplify run that runCall acts as.
const simplifyRun = "run-" + config.StageSimplify

// prFiles are the paths the implementer's commit changes, so the paths of
// the pull request in the simplify tests.
var prFiles = map[string]string{
	"app/x.go":          "package app\n\nfunc X() int { return 1 }\n",
	"app/x_test.go":     "package app\n\nfunc TestX(t *testing.T) {}\n\nfunc TestY(t *testing.T) {}\n",
	".loop/config.yaml": "# the project's gates\n" + configBase,
}

// configBase is the project config the base holds. The pull request
// changes no setting of it.
const configBase = "merge:\n  mechanism: direct\n"

// implementerPushed makes a ticket that the implementer run holds, with
// one pushed commit that changes prFiles. It returns the ticket and that
// head.
func (e *env) implementerPushed() (string, string) {
	e.t.Helper()
	e.withWorld()
	e.onBase(e.t, map[string]string{".loop/config.yaml": configBase})
	id := e.ticket(machine.InProgress, "")
	head := commit(e.t, e.checkout, "work", prFiles)
	if _, err := e.push(id, head); err != nil {
		e.t.Fatal(err)
	}
	return id, head
}

// simplifyHolds makes a ticket in in_simplify that the simplify run holds,
// with its pull request at simplify_from. It returns the ticket and
// simplify_from.
func (e *env) simplifyHolds() (string, string) {
	e.t.Helper()
	id, from := e.implementerPushed()
	e.api.World.Add(&gt.PR{Head: BranchFor(id), HeadSHA: from, Title: "t", Body: "the implementer's red and green runs"})
	mustOK(e.t, e.runCall("pr open", id, machine.StageImplementer, map[string]string{"title": "t"}, nil))
	if _, res := e.m.Claim(context.Background(), machine.ClaimRequest{Project: testProject, Item: id,
		Stage: config.StageSimplify, RunID: simplifyRun}); res.Refusal != nil {
		e.t.Fatalf("simplify claim: %+v", *res.Refusal)
	}
	if it := e.status(id); it.Status != machine.InSimplify || it.Fact(machine.FactSimplifyFrom) != from {
		e.t.Fatalf("not waiting for the simplify run: %s %v", it.Status, it.Facts)
	}
	return id, from
}

// branchPush runs `branch push` of the checkout's HEAD as a run of stage.
func (e *env) branchPush(id, stage string) cli.Result {
	e.t.Helper()
	head := gitT(e.t, e.checkout, "rev-parse", "HEAD")
	return e.runCall("branch push", id, stage, nil, map[string]string{cli.ExtraHead: head})
}

// held is the commit the mirror holds for run, or "".
func (e *env) held(run string) string {
	e.t.Helper()
	return e.svc.Mirror(e.project).Resolve(context.Background(), heldRef(run))
}

// wantScope fails the test unless res refuses with SIMPLIFY_SCOPE and rule.
func wantScope(t *testing.T, res cli.Result, rule string) {
	t.Helper()
	if res.Refusal == nil || res.Refusal.Code != "SIMPLIFY_SCOPE" || res.Refusal.Rule != rule {
		t.Fatalf("push: refusal %+v; want SIMPLIFY_SCOPE, %s", res.Refusal, rule)
	}
}

// pushResult reads the PushResult of a push that landed.
func pushResult(t *testing.T, res cli.Result) PushResult {
	t.Helper()
	mustOK(t, res)
	out, ok := res.Data.(PushResult)
	if !ok {
		t.Fatalf("push data %T, want PushResult", res.Data)
	}
	return out
}

// outOfBounds are the three pushes that break a simplify run's bounds
// other than a test path. Each one changes the checkout over the
// implementer's head.
var outOfBounds = []struct {
	name, rule string
	make       func(e *env)
}{
	{"two commits", "two_commits", func(e *env) {
		commit(e.t, e.checkout, "one", map[string]string{"app/x.go": "package app\n\nfunc X() int { return 2 }\n"})
		commit(e.t, e.checkout, "two", map[string]string{"app/x.go": "package app\n\nfunc X() int { return 3 }\n"})
	}},
	{"merge commit", "merge_commit", func(e *env) {
		e.baseMoves(e.t, map[string]string{"other.txt": "o\n"})
		gitT(e.t, e.checkout, "merge", "-q", "--no-ff", "-m", "Merge main", "main")
	}},
	{"path outside the pull request", "path_outside_pr", func(e *env) {
		commit(e.t, e.checkout, "outside", map[string]string{"README.md": "hello, simpler\n"})
	}},
}

// C12: from a simplify run, two commits over simplify_from, a merge
// commit and a commit outside the pull request's paths each refuse with
// SIMPLIFY_SCOPE and name the rule. The branch stays at simplify_from,
// and the mirror holds nothing.
func TestC12SimplifyPushOutsideItsBoundsIsRefused(t *testing.T) {
	for _, c := range outOfBounds {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			id, from := e.simplifyHolds()
			c.make(e)
			wantScope(t, e.branchPush(id, config.StageSimplify), c.rule)
			if got := e.remoteSHA(BranchFor(id)); got != from {
				t.Fatalf("remote %s = %q after the refusal, want %s", BranchFor(id), got, from)
			}
			if got := e.held(simplifyRun); got != "" {
				t.Fatalf("the mirror holds %s after the refusal", got)
			}
		})
	}
}

// C12: the same three pushes from an implementer run pass the check and
// land on the branch.
func TestC12ImplementerPushOfTheSameCommitsLands(t *testing.T) {
	for _, c := range outOfBounds {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			id, _ := e.implementerPushed()
			c.make(e)
			out := pushResult(t, e.branchPush(id, machine.StageImplementer))
			if !out.Pushed || out.Held {
				t.Fatalf("push result %+v, want pushed and not held", out)
			}
			if got, want := e.remoteSHA(BranchFor(id)), gitT(t, e.checkout, "rev-parse", "HEAD"); got != want {
				t.Fatalf("remote %s = %q, want %s", BranchFor(id), got, want)
			}
		})
	}
}

// C12, C18: one commit on the pull request's paths that changes no test
// path passes. The server holds it in the mirror and answers held: true,
// and the GitHub branch stays at simplify_from.
func TestC12SimplifyPushOfOneCommitIsHeld(t *testing.T) {
	e := newEnv(t)
	id, from := e.simplifyHolds()
	head := commit(t, e.checkout, "simplify", map[string]string{"app/x.go": "package app\n\nfunc X() int { return 1 } // simpler\n"})
	out := pushResult(t, e.branchPush(id, config.StageSimplify))
	if !out.Held || out.Pushed || out.Head != head || out.Old != from {
		t.Fatalf("push result %+v, want held %s over %s", out, head, from)
	}
	if got := e.remoteSHA(BranchFor(id)); got != from {
		t.Fatalf("remote %s = %q, want %s: a held commit reached GitHub", BranchFor(id), got, from)
	}
	if got := e.held(simplifyRun); got != head {
		t.Fatalf("held ref = %q, want %s", got, head)
	}
}

// C17: isTestPath holds for one example of each pattern of the rule, and
// for no source or document path.
func TestC17IsTestPath(t *testing.T) {
	cases := map[string]bool{
		"internal/x/x_test.go":         true,
		"src/a.test.ts":                true,
		"src/a.spec.js":                true,
		"pkg/test_a.py":                true,
		"pkg/conftest.py":              true,
		"test/helper.go":               true,
		"a/tests/b.py":                 true,
		"src/__tests__/a.ts":           true,
		"internal/x/testdata/in.json":  true,
		"spec/models/a.rb":             true,
		"web/e2e/login.ts":             true,
		"vitest.config.ts":             true,
		"web/jest.config.js":           true,
		"playwright.config.ts":         true,
		"pytest.ini":                   true,
		"tox.ini":                      true,
		".loop/config.yaml":            true,
		".loop/policy/implementer.md":  true,
		".github/workflows/ci.yml":     true,
		"internal/x/x.go":              false,
		"src/a.ts":                     false,
		"docs/plans/x-spec.md":         false,
		"contest.py":                   false,
		"internal/testing/x.go":        false,
		".github/dependabot.yml":       false,
		"src/latest/a.ts":              false,
		"spec.md":                      false,
		"internal/x/x_test_helpers.go": false,
	}
	for p, want := range cases {
		if got := isTestPath(p); got != want {
			t.Errorf("isTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// testChanges are the commits on the pull request's paths that change a
// test path, and the path each refusal names.
var testChanges = []struct {
	name, path string
	files      map[string]string
}{
	{"deletes a test file", "app/x_test.go", map[string]string{"app/x_test.go": ""}},
	{"deletes a test function", "app/x_test.go", map[string]string{"app/x_test.go": "package app\n\nfunc TestX(t *testing.T) {}\n"}},
	{"adds a skip", "app/x_test.go", map[string]string{
		"app/x_test.go": "package app\n\nfunc TestX(t *testing.T) { t.Skip(\"flaky\") }\n\nfunc TestY(t *testing.T) {}\n"}},
	{"renames a test file", "app/x_test.go", map[string]string{"app/x_test.go": "", "app/y_test.go": prFiles["app/x_test.go"]}},
	{"changes the project config", ".loop/config.yaml", map[string]string{".loop/config.yaml": "# the gates\n" + configBase}},
}

// C17: from a simplify run, each commit that changes a test path refuses
// with SIMPLIFY_SCOPE and rule test_path, names the path, and holds
// nothing.
func TestC17SimplifyCommitThatChangesATestPathIsRefused(t *testing.T) {
	for _, c := range testChanges {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			id, from := e.simplifyHolds()
			commit(t, e.checkout, c.name, c.files)
			res := e.branchPush(id, config.StageSimplify)
			wantScope(t, res, "test_path")
			if !strings.Contains(res.Refusal.Hint, c.path) {
				t.Fatalf("hint %q does not name %s", res.Refusal.Hint, c.path)
			}
			if e.remoteSHA(BranchFor(id)) != from || e.held(simplifyRun) != "" {
				t.Fatalf("the refusal moved the branch or held the commit")
			}
		})
	}
}

// C17: the same commits from an implementer run land.
func TestC17ImplementerCommitThatChangesATestPathLands(t *testing.T) {
	for _, c := range testChanges {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			id, _ := e.implementerPushed()
			head := commit(t, e.checkout, c.name, c.files)
			if out := pushResult(t, e.branchPush(id, machine.StageImplementer)); !out.Pushed || e.remoteSHA(BranchFor(id)) != head {
				t.Fatalf("push result %+v, remote %s", out, e.remoteSHA(BranchFor(id)))
			}
		})
	}
}

// simplified holds one simplify commit for the simplify run, and returns
// it.
func (e *env) simplified(id string) string {
	e.t.Helper()
	head := commit(e.t, e.checkout, "simplify", map[string]string{"app/x.go": "package app\n\nfunc X() int { return 1 } // simpler\n"})
	if out := pushResult(e.t, e.branchPush(id, config.StageSimplify)); !out.Held {
		e.t.Fatalf("push result %+v, want held", out)
	}
	return head
}

// pushIntent reads the push intent of run to branch: its old and new sha
// and its state.
func (e *env) pushIntent(branch, run string) (old, sha, state string) {
	e.t.Helper()
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT old_sha, new_sha, state FROM push_intents
			WHERE branch = ? AND run_id = ? ORDER BY id DESC LIMIT 1`, branch, run).Scan(&old, &sha, &state)
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		e.t.Fatal(err)
	}
	return old, sha, state
}

// C6: the hand-off pushes the held commit with a lease on simplify_from,
// and moves the ticket to in_review with head at that push, while GitHub
// still reports the older head. The held ref is gone, and the pull
// request keeps its body.
func TestC6HandOffPushesTheHeldCommitAndRecordsItsHead(t *testing.T) {
	e := newEnv(t)
	id, from := e.simplifyHolds()
	head := e.simplified(id)

	mustOK(t, e.runCall("pr open", id, config.StageSimplify, nil, nil))
	if got := e.remoteSHA(BranchFor(id)); got != head {
		t.Fatalf("remote %s = %q after the hand-off, want %s", BranchFor(id), got, head)
	}
	if old, sha, state := e.pushIntent(BranchFor(id), simplifyRun); old != from || sha != head || state != "done" {
		t.Fatalf("push intent %s..%s %s, want %s..%s done", old, sha, state, from, head)
	}
	it := e.status(id)
	if it.Status != machine.InReview || it.Fact(machine.FactHead) != head || it.Owner != "" {
		t.Fatalf("after the hand-off: %s owner %q %v", it.Status, it.Owner, it.Facts)
	}
	if got := e.held(simplifyRun); got != "" {
		t.Fatalf("held ref = %s after the hand-off", got)
	}
	if got := e.api.World.PR(1); got.HeadSHA != from || got.Body != "the implementer's red and green runs" {
		t.Fatalf("PR #1 head %s body %q", got.HeadSHA, got.Body)
	}
}

// C6: a hand-off that holds no commit pushes nothing, and review starts
// at simplify_from.
func TestC6HandOffWithNoHeldCommitPushesNothing(t *testing.T) {
	e := newEnv(t)
	id, from := e.simplifyHolds()
	mustOK(t, e.runCall("pr open", id, config.StageSimplify, nil, nil))
	if got := e.remoteSHA(BranchFor(id)); got != from {
		t.Fatalf("remote %s = %q, want %s", BranchFor(id), got, from)
	}
	if _, sha, _ := e.pushIntent(BranchFor(id), simplifyRun); sha != "" {
		t.Fatalf("the hand-off recorded a push of %s", sha)
	}
	if it := e.status(id); it.Status != machine.InReview || it.Fact(machine.FactHead) != from {
		t.Fatalf("after the hand-off: %s %v", it.Status, it.Facts)
	}
}

// C6: a hand-off whose lease fails, because the branch moved on GitHub,
// refuses with REMOTE_AHEAD, applies no row, and leaves the foreign
// commit.
func TestC6HandOffOnAMovedBranchIsRefused(t *testing.T) {
	e := newEnv(t)
	id, _ := e.simplifyHolds()
	e.simplified(id)
	other := t.TempDir()
	gitT(t, other, "clone", "-q", "-b", BranchFor(id), e.git.RemoteDir(), ".")
	commit(t, other, "foreign", map[string]string{"b.txt": "b\n"})
	gitT(t, other, "push", "-q", "origin", BranchFor(id))
	foreign := e.remoteSHA(BranchFor(id))

	res := e.runCall("pr open", id, config.StageSimplify, nil, nil)
	if res.Refusal == nil || res.Refusal.Code != cli.CodeRemoteAhead {
		t.Fatalf("pr open: refusal %+v, want REMOTE_AHEAD", res.Refusal)
	}
	if it := e.status(id); it.Status != machine.InSimplify || it.Owner != simplifyRun {
		t.Fatalf("after the refusal: %s owner %q", it.Status, it.Owner)
	}
	if got := e.remoteSHA(BranchFor(id)); got != foreign {
		t.Fatalf("remote %s = %q, want the foreign %s", BranchFor(id), got, foreign)
	}
}

// C6: the server stops after the hand-off's push lands and before the
// row. The repeat of pr open finds the commit on GitHub and records it as
// the head.
func TestC6RepeatAfterACrashAtTheHandOffRecordsThePushedHead(t *testing.T) {
	e := newEnv(t)
	id, _ := e.simplifyHolds()
	head := e.simplified(id)
	e.svc.crash = func(p string) bool { return p == "held_pushed" }
	if res := e.runCall("pr open", id, config.StageSimplify, nil, nil); res.Refusal == nil {
		t.Fatal("pr open passed the kill point")
	}
	if it := e.status(id); it.Status != machine.InSimplify {
		t.Fatalf("after the crash: %s", it.Status)
	}
	e.restart()
	mustOK(t, e.runCall("pr open", id, config.StageSimplify, nil, nil))
	if it := e.status(id); it.Status != machine.InReview || it.Fact(machine.FactHead) != head {
		t.Fatalf("after the repeat: %s %v", it.Status, it.Facts)
	}
}

// C6: a repeat of pr open that finds no held ref, because the first call
// pushed the commit and deleted the ref, reads the head from the run's
// push, not from simplify_from.
func TestC6RepeatWithNoHeldRefReadsTheRunsPush(t *testing.T) {
	e := newEnv(t)
	id, from := e.simplifyHolds()
	head := e.simplified(id)
	pushed, err := e.svc.pushHeld(context.Background(), testProject, id, BranchFor(id), simplifyRun, from)
	if err != nil || pushed != head {
		t.Fatalf("pushHeld = %q, %v; want %s", pushed, err, head)
	}
	if e.held(simplifyRun) != "" {
		t.Fatal("the held ref is still there after its push")
	}
	mustOK(t, e.runCall("pr open", id, config.StageSimplify, nil, nil))
	if it := e.status(id); it.Status != machine.InReview || it.Fact(machine.FactHead) != head {
		t.Fatalf("after the repeat: %s %v", it.Status, it.Facts)
	}
}

// C18: a run that holds one commit and ends with no hand-off leaves the
// GitHub branch at simplify_from, and its settlement deletes the held
// ref.
func TestC18HeldCommitNeverReachesGitHubWithoutTheHandOff(t *testing.T) {
	e := newEnv(t)
	id, from := e.simplifyHolds()
	e.simplified(id)
	if got := e.remoteSHA(BranchFor(id)); got != from {
		t.Fatalf("remote %s = %q, want %s", BranchFor(id), got, from)
	}
	if err := e.svc.DropHeld(context.Background(), testProject, simplifyRun); err != nil {
		t.Fatal(err)
	}
	if got := e.held(simplifyRun); got != "" {
		t.Fatalf("held ref = %s after DropHeld", got)
	}
	if err := e.svc.DropHeld(context.Background(), testProject, simplifyRun); err != nil {
		t.Fatalf("a second DropHeld: %v", err)
	}
	if got := e.remoteSHA(BranchFor(id)); got != from {
		t.Fatalf("remote %s = %q after the settlement, want %s", BranchFor(id), got, from)
	}
}
