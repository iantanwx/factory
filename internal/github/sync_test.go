package github

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// syncCall is `branch sync` from a run that holds item.
func (e *env) syncCall(item string) cli.Result {
	e.t.Helper()
	v, ok := cli.Surface().Verb("branch sync")
	if !ok {
		e.t.Fatal("no branch sync verb")
	}
	call := &rpc.Call{Inv: cli.Invocation{Verb: v, Flags: map[string]string{}, Extra: map[string]string{}},
		Principal: rpc.Principal{Kind: cli.PrincipalRun, RunID: "run-1", Project: testProject, Item: item,
			Stage: machine.StageImplementer, Checkout: e.checkout}}
	return e.svc.branchSync(context.Background(), call)
}

// syncData is the answer of `branch sync` as a run reads it from JSON.
type syncData struct {
	Mirror string   `json:"mirror"`
	Base   string   `json:"base"`
	SHA    string   `json:"sha"`
	Ref    string   `json:"ref"`
	Fetch  []string `json:"fetch"`
	Item   struct {
		ID     string   `json:"id"`
		Branch string   `json:"branch"`
		SHA    string   `json:"sha"`
		Ref    string   `json:"ref"`
		Fetch  []string `json:"fetch"`
	} `json:"item"`
}

func decodeSync(t *testing.T, res cli.Result) syncData {
	t.Helper()
	if res.Refusal != nil {
		t.Fatalf("branch sync refused: %+v", *res.Refusal)
	}
	var d syncData
	raw, err := json.Marshal(res.Data)
	if err == nil {
		err = json.Unmarshal(raw, &d)
	}
	if err != nil {
		t.Fatalf("branch sync data: %v", err)
	}
	return d
}

// agentGit runs git in a checkout the way an agent does: no credential,
// no user config.
func agentGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0"}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// A run's checkout brings a PR head in from the mirror with no
// credential: `branch sync` names the mirror, the head sha and a ref the
// server keeps for the item, and a fetch by that sha works. The head here
// was pushed by someone else, so neither the mirror nor the checkout held
// it before the sync.
func TestBranchSyncBringsThePRHeadIntoAFreshCheckout(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	id := e.ticket(machine.InProgress, "")
	branch := BranchFor(id)

	// A second checkout cloned before the head existed: it holds the base
	// only, and it has no remote that reaches the head.
	fresh := filepath.Join(t.TempDir(), "fresh")
	gt.Git(t, filepath.Dir(fresh), "clone", "-q", e.git.RemoteDir(), fresh)
	gt.Git(t, fresh, "remote", "remove", "origin")

	head := commit(t, e.checkout, "work", map[string]string{"b.txt": "b\n"})
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), head+":refs/heads/"+branch)

	d := decodeSync(t, e.syncCall(id))
	if d.Mirror == "" || d.Base != "main" || d.SHA != e.base {
		t.Fatalf("base part of branch sync = %+v", d)
	}
	if strings.Contains(d.Ref, "refs/remotes/") || strings.Contains(d.Item.Ref, "refs/remotes/") {
		t.Fatalf("branch sync leaks the mirror's remote-tracking layout: %+v", d)
	}
	if d.Item.ID != id || d.Item.Branch != branch || d.Item.SHA != head || d.Item.Ref == "" {
		t.Fatalf("item part of branch sync = %+v, want head %s on %s", d.Item, head, branch)
	}

	// A fetch by sha: the contract the turn skill uses.
	if out, err := agentGit(t, fresh, "fetch", "-q", d.Mirror, d.Item.SHA); err != nil {
		t.Fatalf("git fetch <mirror> <sha>: %v\n%s", err, out)
	}
	if out, err := agentGit(t, fresh, "cat-file", "-e", head+"^{commit}"); err != nil {
		t.Fatalf("the head did not arrive: %s", out)
	}
	// The item's fetch command lands the server's ref in the checkout.
	if out, err := agentGit(t, fresh, d.Item.Fetch[1:]...); err != nil {
		t.Fatalf("%v: %v\n%s", d.Item.Fetch, err, out)
	}
	// The base's fetch command too.
	if out, err := agentGit(t, fresh, d.Fetch[1:]...); err != nil {
		t.Fatalf("%v: %v\n%s", d.Fetch, err, out)
	}
	if got, _ := agentGit(t, fresh, "rev-parse", "refs/remotes/origin/main"); got != e.base {
		t.Fatalf("origin/main in the checkout = %q, want %s", got, e.base)
	}
	// A head that is no longer a tip still fetches by its sha, under either
	// wire protocol: a run may hold the sha that `pr show` read before a
	// later push.
	next := commit(t, e.checkout, "more", map[string]string{"c.txt": "c\n"})
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), next+":refs/heads/"+branch)
	if d = decodeSync(t, e.syncCall(id)); d.Item.SHA != next {
		t.Fatalf("item head after a second push = %q, want %s", d.Item.SHA, next)
	}
	older := filepath.Join(t.TempDir(), "older")
	gt.Git(t, filepath.Dir(older), "init", "-q", older)
	if out, err := agentGit(t, older, "-c", "protocol.version=0", "fetch", "-q", d.Mirror, head); err != nil {
		t.Fatalf("fetch of a reachable sha that is no tip: %v\n%s", err, out)
	}
	// The mirror is the server's: the checkout's config gained nothing.
	if out, _ := agentGit(t, fresh, "config", "--get", "uploadpack.allowAnySHA1InWant"); out != "" {
		t.Fatalf("the checkout's config changed: %q", out)
	}
}

// An item with no branch on the remote answers an empty head and no fetch
// command, and the server drops a ref it kept for an earlier head.
func TestBranchSyncWithNoItemBranch(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	id := e.ticket(machine.InProgress, "")
	d := decodeSync(t, e.syncCall(id))
	if d.Item.ID != id || d.Item.SHA != "" || len(d.Item.Fetch) != 0 {
		t.Fatalf("item part with no branch = %+v", d.Item)
	}
	head := commit(t, e.checkout, "work", map[string]string{"b.txt": "b\n"})
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), head+":refs/heads/"+BranchFor(id))
	if d = decodeSync(t, e.syncCall(id)); d.Item.SHA != head {
		t.Fatalf("item head = %q, want %s", d.Item.SHA, head)
	}
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), ":refs/heads/"+BranchFor(id))
	if d = decodeSync(t, e.syncCall(id)); d.Item.SHA != "" {
		t.Fatalf("item head after the branch went = %q", d.Item.SHA)
	}
	if got := e.svc.Mirror(e.project).Resolve(context.Background(), itemSyncRef(id)); got != "" {
		t.Fatalf("the mirror kept %s at %s after the branch went", itemSyncRef(id), got)
	}
}
