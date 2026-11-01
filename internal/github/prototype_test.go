package github

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// roomCall is one verb from a room of stage that serves item, inside the
// env's checkout.
func (e *env) roomCall(path, item, stage string, flags map[string]string) cli.Result {
	e.t.Helper()
	v, ok := cli.Surface().Verb(path)
	if !ok {
		e.t.Fatalf("no verb %q", path)
	}
	head := gitT(e.t, e.checkout, "rev-parse", "HEAD")
	call := &rpc.Call{Inv: cli.Invocation{Verb: v, Flags: flags, Extra: map[string]string{cli.ExtraHead: head}, RequestID: "req-" + path},
		Principal: rpc.Principal{Kind: cli.PrincipalRoom, RunID: "room-1", Project: testProject, Item: item, Stage: stage,
			Checkout: e.checkout}}
	return e.svc.Handlers()[path](context.Background(), call)
}

// prototypePush is `loop branch push --prototype` from a room of stage.
func (e *env) prototypePush(item, stage string) cli.Result {
	e.t.Helper()
	return e.roomCall("branch push", item, stage, map[string]string{"prototype": "true"})
}

// designEpic is an epic in status with a merged PRD and a UI story.
func (e *env) designEpic(status string) string {
	e.t.Helper()
	return e.prdEpicIn(status, map[string]string{machine.FactPRDMerged: "m1", machine.FactUIStories: "US-01"})
}

func wantRule(t *testing.T, res cli.Result, code, rule string) {
	t.Helper()
	if res.Refusal == nil || res.Refusal.Code != code || res.Refusal.Rule != rule {
		t.Fatalf("result %+v, want %s with rule %s", res.Refusal, code, rule)
	}
}

// C19: a design room's `branch push --prototype` on an epic in design
// pushes the checkout's HEAD to loop/<epic>-prototype, and to no other
// branch.
func TestC19DesignRoomPushesThePrototypeToItsOwnBranch(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	ep := e.designEpic(machine.Design)
	head := commit(t, e.checkout, "prototype", map[string]string{"web/index.html": "<p>widgets</p>\n"})
	mustOK(t, e.prototypePush(ep, "design-room"))
	if got := e.remoteSHA("loop/" + ep + "-prototype"); got != head {
		t.Fatalf("remote loop/%s-prototype = %q, want %s", ep, got, head)
	}
	for _, b := range []string{"loop/" + ep, "loop/" + ep + "-design", "loop/" + ep + "-spec"} {
		if got := e.remoteSHA(b); got != "" {
			t.Errorf("remote %s = %q; want no branch", b, got)
		}
	}
}

// C19: the prototype push refuses a run, and a room of another kind, with
// rule prototype_room_only, and a design room on an epic in any state but
// design with rule not_in_design. Each refusal pushes nothing.
func TestC19PrototypePushRefusesAnyCallerButADesignRoomInDesign(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	ep := e.designEpic(machine.Design)
	commit(t, e.checkout, "prototype", map[string]string{"web/index.html": "<p>widgets</p>\n"})

	run := e.runCall("branch push", ep, machine.StagePlanner, map[string]string{"prototype": "true"},
		map[string]string{cli.ExtraHead: gitT(t, e.checkout, "rev-parse", "HEAD")})
	wantRule(t, run, cli.CodeCapabilityDenied, "prototype_room_only")
	wantRule(t, e.prototypePush(ep, "architect-room"), cli.CodeCapabilityDenied, "prototype_room_only")

	for _, status := range []string{machine.DesignReview, machine.DesignSignoff, machine.SpecNeeded, machine.Architecture} {
		other := e.designEpic(status)
		wantRule(t, e.prototypePush(other, "design-room"), cli.CodeCapabilityDenied, "not_in_design")
		if got := e.remoteSHA("loop/" + other + "-prototype"); got != "" {
			t.Errorf("an epic in %s: remote prototype branch = %q; want none", status, got)
		}
	}
	if got := e.remoteSHA("loop/" + ep + "-prototype"); got != "" {
		t.Fatalf("remote loop/%s-prototype = %q after the refusals; want none", ep, got)
	}
}

// designPush pushes HEAD of the checkout to the design branch of tk-1.
func (e *env) designPush() (PushResult, error) {
	head := gitT(e.t, e.checkout, "rev-parse", "HEAD")
	return e.svc.Push(context.Background(), PushRequest{Project: testProject, Item: "tk-1", Branch: "loop/tk-1-design",
		Checkout: e.checkout, Head: head, RunID: "room-1", Caller: cli.PrincipalRoom})
}

// C18: a design branch push that changes only the epic's design directory
// lands. One that also names a path outside it gets SHAPE_REFUSED with
// rule design_scope, its hint names each such path, and the remote has no
// design branch.
func TestC18DesignBranchHoldsOnlyTheDesignDirectory(t *testing.T) {
	e := newEnv(t)
	commit(t, e.checkout, "record", map[string]string{"docs/design/tk-1/record.md": "# Record\n"})
	if _, err := e.designPush(); err != nil {
		t.Fatalf("push of the design directory: %+v", Refusal(err))
	}
	inside := e.remoteSHA("loop/tk-1-design")

	commit(t, e.checkout, "reach out", map[string]string{"docs/design/tk-1/s1.png": "png\n", "web/app.js": "app\n",
		"docs/design/tk-2/record.md": "# Other\n"})
	_, err := e.designPush()
	ref := Refusal(err)
	if ref.Code != cli.CodeShapeRefused || ref.Rule != "design_scope" {
		t.Fatalf("refusal %+v, want SHAPE_REFUSED with rule design_scope", ref)
	}
	for _, p := range []string{"web/app.js", "docs/design/tk-2/record.md"} {
		if !strings.Contains(ref.Hint, p) {
			t.Errorf("hint %q names no %s", ref.Hint, p)
		}
	}
	if strings.Contains(ref.Hint, "docs/design/tk-1/s1.png") {
		t.Errorf("hint %q names a path inside the design directory", ref.Hint)
	}
	if got := e.remoteSHA("loop/tk-1-design"); got != inside {
		t.Fatalf("remote loop/tk-1-design = %q, want %s", got, inside)
	}
}

// C18: a design branch push that holds a commit of the prototype's branch
// that the base lacks gets SHAPE_REFUSED with rule prototype_in_design,
// and its hint names the commit.
func TestC18PrototypeCommitInTheDesignBranchIsRefused(t *testing.T) {
	e := newEnv(t)
	gitT(t, e.checkout, "checkout", "-q", "-b", "prototype", e.base)
	proto := commit(t, e.checkout, "prototype", map[string]string{"docs/design/tk-1/proto.html": "<p>widgets</p>\n"})
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), "prototype:refs/heads/loop/tk-1-prototype")
	gitT(t, e.checkout, "checkout", "-q", "work")
	commit(t, e.checkout, "record", map[string]string{"docs/design/tk-1/record.md": "# Record\n"})
	gitT(t, e.checkout, "merge", "-q", "--no-ff", "-m", "bring the prototype", "prototype")

	_, err := e.designPush()
	ref := Refusal(err)
	if ref.Code != cli.CodeShapeRefused || ref.Rule != "prototype_in_design" {
		t.Fatalf("refusal %+v, want SHAPE_REFUSED with rule prototype_in_design", ref)
	}
	if !strings.Contains(ref.Hint, proto[:12]) {
		t.Errorf("hint %q names no commit %s", ref.Hint, proto[:12])
	}
	if got := e.remoteSHA("loop/tk-1-design"); got != "" {
		t.Fatalf("remote loop/tk-1-design = %q; want no branch", got)
	}
}

// C18: a merge of the base into the design branch brings in paths outside
// the design directory that the base changed, and the push lands.
func TestC18MergeOfTheBaseIntoTheDesignBranchLands(t *testing.T) {
	e := newEnv(t)
	commit(t, e.checkout, "record", map[string]string{"docs/design/tk-1/record.md": "# Record\n"})
	if _, err := e.designPush(); err != nil {
		t.Fatalf("push of the record: %+v", Refusal(err))
	}
	e.baseMoves(t, map[string]string{"web/app.js": "app\n", "docs/design/tk-2/record.md": "# Other\n"})
	gitT(t, e.checkout, "merge", "-q", "--no-ff", "-m", "merge the base", "main")
	head := gitT(t, e.checkout, "rev-parse", "HEAD")
	if _, err := e.designPush(); err != nil {
		t.Fatalf("push of the merge of the base: %+v", Refusal(err))
	}
	if got := e.remoteSHA("loop/tk-1-design"); got != head {
		t.Fatalf("remote loop/tk-1-design = %q, want %s", got, head)
	}
}

// `branch sync` for an epic in a design state names the prototype's branch
// with a fetch command that brings its head into the checkout as
// origin/loop/<epic>-prototype. An epic outside the design states gets no
// prototype part.
func TestBranchSyncNamesThePrototypeOfAnEpicInADesignState(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	ep := e.designEpic(machine.Design)
	head := commit(t, e.checkout, "prototype", map[string]string{"web/index.html": "<p>widgets</p>\n"})
	mustOK(t, e.prototypePush(ep, "design-room"))
	e.put(ep, machine.DesignReview, nil)

	res := e.roomCall("branch sync", ep, "design-room", map[string]string{})
	mustOK(t, res)
	got, ok := res.Data.(SyncResult)
	if !ok || got.Prototype == nil {
		t.Fatalf("branch sync data %+v holds no prototype part", res.Data)
	}
	if got.Prototype.Branch != "loop/"+ep+"-prototype" || got.Prototype.SHA != head || len(got.Prototype.Fetch) == 0 {
		t.Fatalf("prototype part %+v, want loop/%s-prototype at %s with a fetch", *got.Prototype, ep, head)
	}
	fresh := t.TempDir()
	gitT(t, fresh, "init", "-q")
	if out, err := agentGit(t, fresh, got.Prototype.Fetch[1:]...); err != nil {
		t.Fatalf("fetch %v: %v\n%s", got.Prototype.Fetch, err, out)
	}
	if sha := gitT(t, fresh, "rev-parse", "refs/remotes/origin/loop/"+ep+"-prototype"); sha != head {
		t.Fatalf("the checkout's origin/loop/%s-prototype = %s, want %s", ep, sha, head)
	}

	spec := e.designEpic(machine.SpecNeeded)
	res = e.roomCall("branch sync", spec, "architect-room", map[string]string{})
	mustOK(t, res)
	if got := res.Data.(SyncResult); got.Prototype != nil {
		t.Fatalf("an epic in spec_needed: prototype part %+v; want none", *got.Prototype)
	}
}

// refDeletes match a delete of a remote ref in Go source: a DELETE call to
// GitHub's refs or branches endpoint, and a git push that deletes, mirrors
// or prunes, or whose refspec has an empty source.
var refDeletes = []*regexp.Regexp{
	regexp.MustCompile(`MethodDelete[^}]*(git/refs|/branches/)`),
	regexp.MustCompile(`"DELETE"[^}]*(git/refs|/branches/)`),
	regexp.MustCompile(`"push"[^}\n]*"(--delete|-d|--mirror|--prune)"`),
	regexp.MustCompile(`"(--delete|--mirror)"`),
	regexp.MustCompile(`[,(\[{]\s*":refs/`),
}

func deletesARef(src string) bool {
	for _, re := range refDeletes {
		if re.MatchString(src) {
			return true
		}
	}
	return false
}

// C20: no package under internal/ deletes a remote ref, by GitHub's API or
// by a git push. The search finds each planted delete, so a pass is no
// blind spot. The prototype's branch also keeps every commit the loop
// pushed: a later push whose head lacks them is refused, and the remote
// still holds the first head.
func TestC20LoopNeverDeletesThePrototypeBranch(t *testing.T) {
	for _, planted := range []string{
		`s.repoDo(ctx, p, ScopePush, Call{Method: http.MethodDelete, Path: repoPath(p, "/git/refs/heads/"+b)}, nil)`,
		`Call{Method: "DELETE", Path: repoPath(p, "/branches/"+b+"/protection")}`,
		`args: []string{"push", "--quiet", "--delete", m.URL, ref}`,
		`args: []string{"push", m.URL, ":refs/heads/" + branch}`,
		`args: []string{"push", "-d", m.URL, ref}`,
	} {
		if !deletesARef(planted) {
			t.Errorf("the search misses a planted delete: %s", planted)
		}
	}
	root := ".." // internal/
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			strings.Contains(path, "githubtest") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if deletesARef(string(src)) {
			t.Errorf("%s deletes a remote ref", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)
	e.withWorld()
	ep := e.designEpic(machine.Design)
	first := commit(t, e.checkout, "prototype", map[string]string{"web/index.html": "<p>one</p>\n"})
	mustOK(t, e.prototypePush(ep, "design-room"))
	gitT(t, e.checkout, "checkout", "-q", "-b", "again", e.base)
	commit(t, e.checkout, "another prototype", map[string]string{"web/index.html": "<p>two</p>\n"})
	wantRule(t, e.prototypePush(ep, "design-room"), cli.CodeRemoteAhead, "remote_holds_more")
	if got := e.remoteSHA("loop/" + ep + "-prototype"); got != first {
		t.Fatalf("remote loop/%s-prototype = %q, want %s", ep, got, first)
	}
}
