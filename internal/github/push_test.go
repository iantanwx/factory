package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

func refusalCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("the push landed; want a refusal")
	}
	return Refusal(err).Code
}

// With an insteadOf rewrite to SSH in the server's own HOME, and a global
// config file in its environment, the push still goes over HTTPS (here
// plain HTTP to the local server) with the App token.
func TestPushUsesTheExplicitURLAndTheAppToken(t *testing.T) {
	e := newEnv(t)
	home := t.TempDir()
	rewrite := "[url \"ssh://git@127.0.0.1:1/\"]\n\tinsteadOf = " + e.git.Base() + "/\n" +
		"[credential]\n\thelper = !echo leaked >" + filepath.Join(home, "captured") + "\n"
	writeFile(t, filepath.Join(home, ".gitconfig"), rewrite)
	writeFile(t, filepath.Join(home, ".config", "git", "config"), rewrite)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))

	head := commit(t, e.checkout, "work", map[string]string{"a.txt": "a\n"})
	res, err := e.push("tk-1", head)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !res.Pushed || res.Replayed {
		t.Fatalf("push result %+v", res)
	}
	if got := e.remoteSHA("loop/tk-1"); got != head {
		t.Fatalf("remote loop/tk-1 = %q, want %s", got, head)
	}
	seen := e.git.SeenTokens()
	if len(seen) == 0 || !slices.Contains(e.api.Minted(), seen[len(seen)-1]) {
		t.Fatalf("the git server saw no minted App token: %v", seen)
	}
	if _, err := os.Stat(filepath.Join(home, "captured")); err == nil {
		t.Fatal("a credential helper from the global config received the token")
	}
}

// The agent controls the checkout's config and hooks. None of them sees
// the token or redirects the push.
func TestCheckoutConfigNeverSeesTheToken(t *testing.T) {
	e := newEnv(t)
	trap := t.TempDir()
	gitT(t, e.checkout, "config", "credential.helper", "!f() { cat > "+filepath.Join(trap, "helper")+"; }; f")
	gitT(t, e.checkout, "config", "remote.origin.url", e.git.URL(testRepo))
	gitT(t, e.checkout, "config", "remote.origin.pushurl", "file://"+filepath.Join(trap, "elsewhere.git"))
	gitT(t, e.checkout, "config", "url.file://"+trap+"/.insteadOf", e.git.Base()+"/")
	hook := filepath.Join(e.checkout, ".git", "hooks", "pre-push")
	writeFile(t, hook, "#!/bin/sh\necho ran > "+filepath.Join(trap, "hook")+"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(e.checkout, ".git", "hooks", "post-checkout")
	writeFile(t, upload, "#!/bin/sh\necho ran > "+filepath.Join(trap, "hook2")+"\n")
	_ = os.Chmod(upload, 0o755)

	head := commit(t, e.checkout, "work", map[string]string{"a.txt": "a\n"})
	if _, err := e.push("tk-1", head); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := e.remoteSHA("loop/tk-1"); got != head {
		t.Fatalf("the push went elsewhere: remote loop/tk-1 = %q", got)
	}
	for _, f := range []string{"helper", "hook", "hook2", "elsewhere.git"} {
		if _, err := os.Stat(filepath.Join(trap, f)); err == nil {
			t.Errorf("%s ran or received data", f)
		}
	}
}

func TestPushOfTheBaseBranchIsRefused(t *testing.T) {
	e := newEnv(t)
	head := commit(t, e.checkout, "work", map[string]string{"a.txt": "a\n"})
	_, err := e.svc.Push(context.Background(), PushRequest{Project: testProject, Item: "tk-1", Branch: "main",
		Checkout: e.checkout, Head: head})
	if got := refusalCode(t, err); got != cli.CodeCapabilityDenied {
		t.Fatalf("code %s, want %s: %v", got, cli.CodeCapabilityDenied, err)
	}
	_, err = e.svc.Push(context.Background(), PushRequest{Project: testProject, Item: "tk-1", Branch: "loop/tk-2",
		Checkout: e.checkout, Head: head})
	if got := refusalCode(t, err); got != cli.CodeCapabilityDenied {
		t.Fatalf("another item's branch: code %s: %v", got, err)
	}
	if e.remoteSHA("main") != e.base {
		t.Fatal("the base branch moved")
	}
}

func TestPushRefusedWhenTheRemoteHoldsCommitsHeadLacks(t *testing.T) {
	e := newEnv(t)
	first := commit(t, e.checkout, "one", map[string]string{"a.txt": "a\n"})
	if _, err := e.push("tk-1", first); err != nil {
		t.Fatal(err)
	}
	// Someone else adds a commit to the branch on the remote.
	other := t.TempDir()
	gitT(t, other, "clone", "-q", "-b", "loop/tk-1", e.git.RemoteDir(), ".")
	commit(t, other, "foreign", map[string]string{"b.txt": "b\n"})
	gitT(t, other, "push", "-q", "origin", "loop/tk-1")
	foreign := e.remoteSHA("loop/tk-1")

	second := commit(t, e.checkout, "two", map[string]string{"c.txt": "c\n"})
	_, err := e.push("tk-1", second)
	if got := refusalCode(t, err); got != cli.CodeRemoteAhead {
		t.Fatalf("code %s, want %s: %v", got, cli.CodeRemoteAhead, err)
	}
	if next := Refusal(err).AllowedNext; len(next) != 1 || next[0] != "branch sync" {
		t.Fatalf("allowed_next = %v, want branch sync", next)
	}
	if e.remoteSHA("loop/tk-1") != foreign {
		t.Fatal("the push overwrote the foreign commit")
	}
}

func TestSecretAddedThenRemovedIsStillRefused(t *testing.T) {
	e := newEnv(t)
	commit(t, e.checkout, "add", map[string]string{"cfg.txt": "key=" + storedSecret + "\n"})
	head := commit(t, e.checkout, "remove", map[string]string{"cfg.txt": ""})
	_, err := e.push("tk-1", head)
	if got := refusalCode(t, err); got != cli.CodeSecretInDiff {
		t.Fatalf("code %s, want %s: %v", got, cli.CodeSecretInDiff, err)
	}
	if strings.Contains(err.Error(), storedSecret) {
		t.Fatal("the refusal quotes the secret")
	}
	if e.remoteSHA("loop/tk-1") != "" {
		t.Fatal("the branch reached the remote")
	}
}

func TestSecretInAnEncodedFormIsRefused(t *testing.T) {
	e := newEnv(t)
	// base64 of the stored value.
	head := commit(t, e.checkout, "add", map[string]string{"blob.txt": "c2stdGVzdC0wMTIzNDU2Nzg5YWJjZGVm\n"})
	if got := refusalCode(t, firstErr(e.push("tk-1", head))); got != cli.CodeSecretInDiff {
		t.Fatalf("code %s, want %s", got, cli.CodeSecretInDiff)
	}
}

func firstErr(_ PushResult, err error) error { return err }

// The scan refuses every file the server writes into a checkout (C-bug-1).
func TestInjectedPathIsRefused(t *testing.T) {
	for _, p := range []string{".loop/brief.json", ".loop/brief.md", ".loop/prompt.md", ".loop/token",
		".loop/pending.jsonl", ".loop/pending.lock", ".loop/env-files", ".env", ".claude/skills/loop-turn/SKILL.md", ".agents/skills/loop-x/a.md"} {
		t.Run(p, func(t *testing.T) {
			e := newEnv(t)
			head := commit(t, e.checkout, "inject", map[string]string{p: "x\n"})
			err := firstErr(e.push("tk-1", head))
			if got := refusalCode(t, err); got != cli.CodeSecretInDiff || Refusal(err).Rule != "injected_path" {
				t.Fatalf("refusal %+v", Refusal(err))
			}
		})
	}
}

// TestC16PushScanCoversEachPinDirectoryExactly proves C16: with the pin
// neon in the last good config, the scan refuses a commit under
// .claude/skills/neon/ or .agents/skills/neon/, and passes one under
// .claude/skills/neon-postgres/. With no last good config it passes neon
// and still refuses the loop- namespace.
func TestC16PushScanCoversEachPinDirectoryExactly(t *testing.T) {
	for _, tc := range []struct {
		pins    []string
		path    string
		refused bool
	}{
		{[]string{"neon"}, ".claude/skills/neon/SKILL.md", true},
		{[]string{"neon"}, ".agents/skills/neon/x.md", true},
		{[]string{"neon"}, ".claude/skills/neon-postgres/SKILL.md", false},
		{nil, ".claude/skills/neon/SKILL.md", false},
		{nil, ".claude/skills/loop-x/SKILL.md", true},
	} {
		t.Run(fmt.Sprintf("%v %s", tc.pins, tc.path), func(t *testing.T) {
			e := newEnv(t)
			e.userSkills = tc.pins
			e.restart()
			head := commit(t, e.checkout, "add a skill file", map[string]string{tc.path: "x\n"})
			_, err := e.push("tk-1", head)
			if !tc.refused {
				if err != nil {
					t.Fatalf("push refused: %+v", Refusal(err))
				}
				return
			}
			var se *ScanError
			if got := refusalCode(t, err); got != cli.CodeSecretInDiff || !errors.As(err, &se) || se.Rule != "injected_path" || se.Path != tc.path {
				t.Fatalf("refusal %+v", Refusal(err))
			}
			if e.remoteSHA("loop/tk-1") != "" {
				t.Fatal("the branch reached the remote")
			}
		})
	}
}

// C-bug-1: a run may change a .loop/ file the project's repository tracks,
// such as .loop/policy/<role>.md or .loop/config.yaml, and may add
// .loop/secrets.yaml. The scan refuses only the paths the server writes.
func TestTrackedLoopPolicyChangePushes(t *testing.T) {
	e := newEnv(t)
	tracked := commit(t, e.checkout, "policy", map[string]string{
		".loop/policy/coordinator.md": "# Coordinator\n\nold rule\n",
		".loop/config.yaml":           "gates: []\n",
	})
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), "work:main")
	if e.remoteSHA("main") != tracked {
		t.Fatal("the base does not track the policy")
	}
	head := commit(t, e.checkout, "edit policy", map[string]string{
		".loop/policy/coordinator.md": "# Coordinator\n\nnew rule\n",
		".loop/config.yaml":           "gates:\n  - name: lint\n    run: make lint\n",
		".loop/secrets.yaml":          "- name: STRIPE_KEY\n",
	})
	if _, err := e.push("tk-1", head); err != nil {
		t.Fatalf("push of a tracked policy change: %+v", Refusal(err))
	}
	if got := e.remoteSHA("loop/tk-1"); got != head {
		t.Fatalf("remote loop/tk-1 = %q, want %s", got, head)
	}
}

func TestCommitMessageWithASecretIsSecretInOutput(t *testing.T) {
	e := newEnv(t)
	head := commit(t, e.checkout, "fix: use "+storedSecret, map[string]string{"a.txt": "a\n"})
	if got := refusalCode(t, firstErr(e.push("tk-1", head))); got != cli.CodeSecretInOutput {
		t.Fatalf("code %s, want %s", got, cli.CodeSecretInOutput)
	}
}

func TestUnsafePathsAreRefused(t *testing.T) {
	cases := map[string]func(t *testing.T, e *env) string{
		"gitmodules": func(t *testing.T, e *env) string {
			return commit(t, e.checkout, "sub", map[string]string{".gitmodules": "[submodule \"x\"]\n\tpath = x\n\turl = ../x\n"})
		},
		"gitlink": func(t *testing.T, e *env) string {
			gitT(t, e.checkout, "update-index", "--add", "--cacheinfo", "160000,"+e.base+",vendor/lib")
			gitT(t, e.checkout, "commit", "-q", "-m", "gitlink")
			return gitT(t, e.checkout, "rev-parse", "HEAD")
		},
		"lfs": func(t *testing.T, e *env) string {
			return commit(t, e.checkout, "lfs", map[string]string{".gitattributes": "*.bin filter=lfs diff=lfs merge=lfs -text\n"})
		},
		"nested filter": func(t *testing.T, e *env) string {
			return commit(t, e.checkout, "filter", map[string]string{"sub/.gitattributes": "*.c filter=indent\n"})
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			head := mk(t, e)
			if got := refusalCode(t, firstErr(e.push("tk-1", head))); got != cli.CodeUnsafePath {
				t.Fatalf("code %s, want %s", got, cli.CodeUnsafePath)
			}
		})
	}
}

// A ticket branch never changes the merge queue's config: an edit can
// weaken a queue condition, and a broken config leaves every PR queued.
// A write, a delete and a rename away are each refused. Only the root
// paths that Mergify reads count.
func TestMergeConfigPathsAreRefused(t *testing.T) {
	cases := map[string]func(t *testing.T, e *env) string{
		"add .mergify.yml": func(t *testing.T, e *env) string {
			return commit(t, e.checkout, "queue", map[string]string{".mergify.yml": "queue_rules: []\n"})
		},
		"add .mergify/config.yml": func(t *testing.T, e *env) string {
			return commit(t, e.checkout, "queue", map[string]string{".mergify/config.yml": "queue_rules: []\n"})
		},
		"add .github/mergify.yml": func(t *testing.T, e *env) string {
			return commit(t, e.checkout, "queue", map[string]string{".github/mergify.yml": "queue_rules: []\n"})
		},
		"delete": func(t *testing.T, e *env) string {
			gitT(t, e.checkout, "rm", "-q", ".mergify.yml")
			gitT(t, e.checkout, "commit", "-q", "-m", "drop the queue")
			return gitT(t, e.checkout, "rev-parse", "HEAD")
		},
		"rename away": func(t *testing.T, e *env) string {
			gitT(t, e.checkout, "mv", ".mergify.yml", "queue.yml")
			gitT(t, e.checkout, "commit", "-q", "-m", "move the queue")
			return gitT(t, e.checkout, "rev-parse", "HEAD")
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if strings.HasPrefix(name, "delete") || strings.HasPrefix(name, "rename") {
				e.onBase(t, map[string]string{".mergify.yml": "queue_rules: []\n"})
			}
			head := mk(t, e)
			err := firstErr(e.push("tk-1", head))
			if got := refusalCode(t, err); got != cli.CodeUnsafePath {
				t.Fatalf("code %s, want %s", got, cli.CodeUnsafePath)
			}
			if r := Refusal(err); r.Rule != "merge_config" || !slices.Contains(r.AllowedNext, verbEscalationRaise) {
				t.Fatalf("refusal %+v; want rule merge_config and escalation raise next", r)
			}
		})
	}
}

// A merge of the base brings in the operator's queue config change. The
// branch does not change the config, so the push lands, as a PR's net file
// list against the base would not hold the path either.
func TestMergeOfTheBaseCarriesTheQueueConfig(t *testing.T) {
	e := newEnv(t)
	work := commit(t, e.checkout, "work", map[string]string{"a.txt": "a\n"})
	if _, err := e.push("tk-1", work); err != nil {
		t.Fatalf("push of the work: %+v", Refusal(err))
	}
	e.baseMoves(t, map[string]string{".mergify.yml": "queue_rules: []\n"})
	gitT(t, e.checkout, "merge", "-q", "--no-ff", "-m", "merge the base", "main")
	head := gitT(t, e.checkout, "rev-parse", "HEAD")
	if _, err := e.push("tk-1", head); err != nil {
		t.Fatalf("push of the merge of the base: %+v", Refusal(err))
	}
	if got := e.remoteSHA("loop/tk-1"); got != head {
		t.Fatalf("remote loop/tk-1 = %q, want %s", got, head)
	}
}

// A merge whose resolution changes the queue config against the base side
// changes the config itself, so it is refused: a config that neither
// parent holds, and a drop of the base's config, which matches the work
// side that forked before the base added it.
func TestMergeThatResolvesTheQueueConfigIsRefused(t *testing.T) {
	cases := map[string]func(t *testing.T, e *env){
		"writes its own": func(t *testing.T, e *env) {
			writeFile(t, filepath.Join(e.checkout, ".mergify.yml"), "queue_rules:\n  - name: weak\n")
			gitT(t, e.checkout, "add", ".mergify.yml")
		},
		"drops the base's": func(t *testing.T, e *env) {
			gitT(t, e.checkout, "rm", "-q", "-f", ".mergify.yml")
		},
	}
	for name, resolve := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			work := commit(t, e.checkout, "work", map[string]string{"a.txt": "a\n"})
			if _, err := e.push("tk-1", work); err != nil {
				t.Fatalf("push of the work: %+v", Refusal(err))
			}
			e.baseMoves(t, map[string]string{".mergify.yml": "queue_rules: []\n"})
			gitT(t, e.checkout, "merge", "-q", "--no-ff", "--no-commit", "main")
			resolve(t, e)
			gitT(t, e.checkout, "commit", "-q", "-m", "merge the base")
			head := gitT(t, e.checkout, "rev-parse", "HEAD")
			err := firstErr(e.push("tk-1", head))
			if got := refusalCode(t, err); got != cli.CodeUnsafePath {
				t.Fatalf("code %s, want %s", got, cli.CodeUnsafePath)
			}
			if r := Refusal(err); r.Rule != "merge_config" {
				t.Fatalf("refusal %+v; want rule merge_config", r)
			}
		})
	}
}

// A file that only shares a name with a queue config is no config.
func TestNestedMergifyFileIsNoMergeConfig(t *testing.T) {
	e := newEnv(t)
	head := commit(t, e.checkout, "fixture", map[string]string{"testdata/.mergify.yml": "queue_rules: []\n"})
	if _, err := e.push("tk-1", head); err != nil {
		t.Fatalf("push: %+v", Refusal(err))
	}
}

// A plain .gitattributes line and a workflow change are allowed: the
// operator grants the App the workflows permission.
func TestSafeAttributesAndWorkflowsPush(t *testing.T) {
	e := newEnv(t)
	head := commit(t, e.checkout, "ok", map[string]string{
		".gitattributes":           "*.txt text eol=lf\n",
		".github/workflows/ci.yml": "on: push\n",
	})
	if _, err := e.push("tk-1", head); err != nil {
		t.Fatalf("push: %v", err)
	}
	mint := e.api.Mints()[len(e.api.Mints())-1]
	if perms, _ := mint["permissions"].(map[string]any); perms["workflows"] != "write" {
		t.Fatalf("the push token lacks workflows write: %v", mint)
	}
}

func TestATagIsNeverPushed(t *testing.T) {
	e := newEnv(t)
	head := commit(t, e.checkout, "work", map[string]string{"a.txt": "a\n"})
	gitT(t, e.checkout, "tag", "-a", "v9", "-m", "tag")
	gitT(t, e.checkout, "tag", "light")
	if _, err := e.push("tk-1", head); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "for-each-ref", "refs/tags")
	cmd.Dir = e.git.RemoteDir()
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("the remote holds tags: %s", out)
	}
}

// The server crashes after a push and before it records the push as done.
// After a restart no head reads as foreign, and the retried push is a
// replay.
func TestCrashAfterPushReplays(t *testing.T) {
	e := newEnv(t)
	head := commit(t, e.checkout, "work", map[string]string{"a.txt": "a\n"})
	e.svc.crash = func(p string) bool { return p == "pushed" }
	if _, err := e.push("tk-1", head); !errors.Is(err, errCrash) {
		t.Fatalf("push: %v, want the crash", err)
	}
	e.restart()
	if err := e.svc.ReconcilePushes(context.Background(), e.project); err != nil {
		t.Fatal(err)
	}
	own, err := e.svc.ownPush(context.Background(), testProject, "loop/tk-1", head)
	if err != nil || !own {
		t.Fatalf("the reconciled push is not the server's own: %v %v", own, err)
	}
	res, err := e.push("tk-1", head)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replayed {
		t.Fatalf("the retried push is not a replay: %+v", res)
	}
}

// Two pushes in one repair run with different local HEADs both land, and
// neither is a replay.
func TestTwoPushesInOneRunBothLand(t *testing.T) {
	e := newEnv(t)
	for i, f := range []string{"a.txt", "b.txt"} {
		head := commit(t, e.checkout, f, map[string]string{f: f})
		res, err := e.push("tk-1", head)
		if err != nil {
			t.Fatalf("push %d: %v", i+1, err)
		}
		if !res.Pushed || res.Replayed || e.remoteSHA("loop/tk-1") != head {
			t.Fatalf("push %d: %+v", i+1, res)
		}
	}
}

// A rebased branch whose commits the head holds by patch id pushes with
// the lease.
func TestRebasedBranchPushes(t *testing.T) {
	e := newEnv(t)
	first := commit(t, e.checkout, "one", map[string]string{"a.txt": "a\n"})
	if _, err := e.push("tk-1", first); err != nil {
		t.Fatal(err)
	}
	gitT(t, e.checkout, "commit", "-q", "--amend", "-m", "one, reworded")
	head := gitT(t, e.checkout, "rev-parse", "HEAD")
	if _, err := e.push("tk-1", head); err != nil {
		t.Fatalf("push of the rebased head: %v", err)
	}
	if e.remoteSHA("loop/tk-1") != head {
		t.Fatal("the rebased head did not land")
	}
}

// Each refusal that a run recovers from with another verb names that verb,
// and the CLI registers it.
func TestRefusalsNameTheirRecoveryVerb(t *testing.T) {
	reg := cli.Surface()
	cases := map[string]struct {
		err  error
		code string
		next string
	}{
		"remote ahead": {&RefusedError{Code: cli.CodeRemoteAhead, Rule: "remote_holds_more", AllowedNext: []string{verbBranchSync}},
			cli.CodeRemoteAhead, "branch sync"},
		"unsafe path":    {&ScanError{Code: cli.CodeUnsafePath, Rule: "gitmodules"}, cli.CodeUnsafePath, "escalation raise"},
		"github refused": {&APIError{Method: "POST", Path: "/x", Status: 422}, cli.CodeGitHubRefused, "escalation raise"},
	}
	for name, c := range cases {
		ref := Refusal(c.err)
		if ref.Code != c.code || len(ref.AllowedNext) != 1 || ref.AllowedNext[0] != c.next {
			t.Errorf("%s: %s %v, want %s [%s]", name, ref.Code, ref.AllowedNext, c.code, c.next)
		}
		if _, ok := reg.Verb(c.next); !ok {
			t.Errorf("%s: the CLI registers no verb %q", name, c.next)
		}
	}
	if ref := Refusal(&APIError{Status: 500}); ref.Code != cli.CodeGitHubUnavailable {
		t.Errorf("a 500 is %s, want %s", ref.Code, cli.CodeGitHubUnavailable)
	}
}

// onBase adds one commit to the base branch on the remote, and moves the
// checkout's work branch onto it.
func (e *env) onBase(t *testing.T, files map[string]string) {
	t.Helper()
	e.baseMoves(t, files)
	gitT(t, e.checkout, "merge", "-q", "--ff-only", "main")
}

// baseMoves adds one commit to the base branch on the remote, and leaves
// the checkout on its work branch, behind the base.
func (e *env) baseMoves(t *testing.T, files map[string]string) {
	t.Helper()
	gitT(t, e.checkout, "checkout", "-q", "main")
	e.base = commit(t, e.checkout, "base change", files)
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), "main")
	gitT(t, e.checkout, "checkout", "-q", "work")
}

// settingsBase is the project config the base holds in the settings tests.
const settingsBase = `merge:
  mechanism: github
stages:
  implementer:
    model: claude-opus-5-5
gates:
  - name: lint
    run: make lint
`

// A stage run's commit that changes stages, pools or merge of the project
// config against its merge base is refused with SETTINGS_CHANGE, and so is
// a file that does not parse. The hint names loop settings set and the
// commit, and the remote branch does not move (C12).
func TestC12RunPushThatChangesASettingIsRefused(t *testing.T) {
	cases := map[string]string{
		"stages":          strings.Replace(settingsBase, "    model: claude-opus-5-5\n", "    model: claude-opus-5-5\n    cap: 3\n", 1),
		"pools":           settingsBase + "pools:\n  reviewers:\n    cap: 2\n",
		"merge":           strings.Replace(settingsBase, "  mechanism: github\n", "  mechanism: github\n  required_checks: [ci]\n", 1),
		"unparsable file": settingsBase + "stages: [\n",
	}
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.onBase(t, map[string]string{".loop/config.yaml": settingsBase})
			head := commit(t, e.checkout, "change a setting", map[string]string{".loop/config.yaml": file})
			err := firstErr(e.pushAs(cli.PrincipalRun, "tk-1", head))
			if got := refusalCode(t, err); got != cli.CodeSettingsChange {
				t.Fatalf("code %s, want %s: %v", got, cli.CodeSettingsChange, err)
			}
			hint := Refusal(err).Hint
			if !strings.Contains(hint, "loop settings set") || !strings.Contains(hint, head[:12]) {
				t.Fatalf("hint %q names no loop settings set or no commit %s", hint, head[:12])
			}
			if got := e.remoteSHA("loop/tk-1"); got != "" {
				t.Fatalf("remote loop/tk-1 = %q; want no branch", got)
			}
		})
	}
}

// A stage run's push that changes only fields outside the settings lands
// (C13).
func TestC13RunPushThatChangesNoSettingLands(t *testing.T) {
	cases := map[string]string{
		"gates":        strings.Replace(settingsBase, "make lint", "make vet", 1),
		"setup":        settingsBase + "setup: make deps\n",
		"policy_hooks": settingsBase + "policy_hooks:\n  - name: hook\n    run: make hook\n",
		"signoff":      settingsBase + "signoff:\n  readers: [octocat]\n",
		"tracker":      settingsBase + "tracker:\n  adapter: beads\n",
		"manifest":     settingsBase + "manifest: .loop/secrets.yaml\n",
		"health":       settingsBase + "health:\n  ci_budget: 2h\n",
	}
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.onBase(t, map[string]string{".loop/config.yaml": settingsBase})
			head := commit(t, e.checkout, "change "+name, map[string]string{".loop/config.yaml": file})
			if _, err := e.pushAs(cli.PrincipalRun, "tk-1", head); err != nil {
				t.Fatalf("push: %+v", Refusal(err))
			}
			if got := e.remoteSHA("loop/tk-1"); got != head {
				t.Fatalf("remote loop/tk-1 = %q, want %s", got, head)
			}
		})
	}
}

// uiBase is a project config that loads, as the base holds it in the ui
// test.
const uiBase = `merge:
  mechanism: mergify
stages:
  implementer:
    model: claude-opus-5-5
`

// TestC6RunPushThatAddsUIIsNoSettingsChange proves C6: a stage run's push
// whose only change adds a ui block that loads lands, and the same push
// that also changes stages is refused with SETTINGS_CHANGE.
func TestC6RunPushThatAddsUIIsNoSettingsChange(t *testing.T) {
	withUI := uiBase + "ui:\n  kind: web\n  paths: [web/**]\n"
	cfg, err := config.ParseProjectConfig([]byte(withUI))
	if err != nil || cfg.UI == nil || cfg.UI.Kind != config.UIWeb {
		t.Fatalf("pushed config: ui = %+v, %v; want a ui block of kind web", cfg, err)
	}
	e := newEnv(t)
	e.onBase(t, map[string]string{".loop/config.yaml": uiBase})
	head := commit(t, e.checkout, "declare the ui", map[string]string{".loop/config.yaml": withUI})
	if _, err := e.pushAs(cli.PrincipalRun, "tk-1", head); err != nil {
		t.Fatalf("push of the ui block: %+v", Refusal(err))
	}
	if got := e.remoteSHA("loop/tk-1"); got != head {
		t.Fatalf("remote loop/tk-1 = %q, want %s", got, head)
	}

	e = newEnv(t)
	e.onBase(t, map[string]string{".loop/config.yaml": uiBase})
	both := strings.Replace(withUI, "    model: claude-opus-5-5\n", "    model: claude-opus-5-5\n    cap: 3\n", 1)
	head = commit(t, e.checkout, "declare the ui and set a cap", map[string]string{".loop/config.yaml": both})
	if got := refusalCode(t, firstErr(e.pushAs(cli.PrincipalRun, "tk-1", head))); got != cli.CodeSettingsChange {
		t.Fatalf("push of the ui block and a cap: code %s, want %s", got, cli.CodeSettingsChange)
	}
	if got := e.remoteSHA("loop/tk-1"); got != "" {
		t.Fatalf("remote loop/tk-1 = %q; want no branch", got)
	}
}

// A stage run's merge of the base brings in a settings change that the
// base made since the fork. The branch changes no setting, so the push
// lands (C13).
func TestC13RunMergeOfABaseSettingsChangeLands(t *testing.T) {
	e := newEnv(t)
	e.onBase(t, map[string]string{".loop/config.yaml": settingsBase})
	work := commit(t, e.checkout, "work", map[string]string{".loop/config.yaml": settingsBase + "setup: make deps\n"})
	if _, err := e.pushAs(cli.PrincipalRun, "tk-1", work); err != nil {
		t.Fatalf("push of the work: %+v", Refusal(err))
	}
	e.baseMoves(t, map[string]string{".loop/config.yaml": strings.Replace(settingsBase, "    model: claude-opus-5-5\n",
		"    model: claude-opus-5-5\n    cap: 3\n", 1)})
	gitT(t, e.checkout, "merge", "-q", "--no-ff", "-m", "merge the base", "main")
	head := gitT(t, e.checkout, "rev-parse", "HEAD")
	if _, err := e.pushAs(cli.PrincipalRun, "tk-1", head); err != nil {
		t.Fatalf("push of the merge of the base: %+v", Refusal(err))
	}
	if got := e.remoteSHA("loop/tk-1"); got != head {
		t.Fatalf("remote loop/tk-1 = %q, want %s", got, head)
	}
}

// The commit that C12 refuses from a stage run lands from a room (C14).
func TestC14RoomPushThatChangesASettingLands(t *testing.T) {
	e := newEnv(t)
	e.onBase(t, map[string]string{".loop/config.yaml": settingsBase})
	head := commit(t, e.checkout, "set the implementer cap", map[string]string{".loop/config.yaml": strings.Replace(settingsBase,
		"    model: claude-opus-5-5\n", "    model: claude-opus-5-5\n    cap: 3\n", 1)})
	if got := refusalCode(t, firstErr(e.pushAs(cli.PrincipalRun, "tk-1", head))); got != cli.CodeSettingsChange {
		t.Fatalf("run push: code %s, want %s", got, cli.CodeSettingsChange)
	}
	if _, err := e.pushAs(cli.PrincipalRoom, "tk-1", head); err != nil {
		t.Fatalf("room push: %+v", Refusal(err))
	}
	if got := e.remoteSHA("loop/tk-1"); got != head {
		t.Fatalf("remote loop/tk-1 = %q, want %s", got, head)
	}
}

// reviserHolds makes an epic in prd_amending that a reviser run holds.
// An earlier run pushed its PRD at prdPath to the epic's branch.
func (e *env) reviserHolds() string {
	e.t.Helper()
	e.withWorld()
	ctx := context.Background()
	id, res := e.m.Create(ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindEpic, Title: "e",
		Verb: "prd_submit", Params: map[string]string{"pr": "1"}, Facts: map[string]string{machine.FactPRDPath: prdPath},
		Actor: machine.Server("test")})
	mustOK(e.t, res)
	e.apply(id, "review_reject", nil)
	_, res = e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageReviser,
		RunID: "run-" + machine.StageReviser})
	mustOK(e.t, res)
	prd := commit(e.t, e.checkout, "prd", map[string]string{prdPath: prdText})
	if _, err := e.svc.Push(ctx, PushRequest{Project: testProject, Item: id, Branch: BranchFor(id), Checkout: e.checkout,
		Head: prd, RunID: "run-brainstorm", Caller: cli.PrincipalRun}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// reviserPush runs `branch push` of the checkout's HEAD as the reviser run.
func (e *env) reviserPush(id string) cli.Result {
	e.t.Helper()
	head := gitT(e.t, e.checkout, "rev-parse", "HEAD")
	return e.runCall("branch push", id, machine.StageReviser, nil, map[string]string{cli.ExtraHead: head})
}

func wantEditSetSplit(t *testing.T, res cli.Result) {
	t.Helper()
	if res.Refusal == nil || res.Refusal.Code != "EDIT_SET_SPLIT" || res.Refusal.Rule != "one_edit_set" {
		t.Fatalf("push: refusal %+v; want EDIT_SET_SPLIT, one_edit_set", res.Refusal)
	}
}

// C15: a reviser run lands one commit that edits its PRD, with a commit
// outside the document, and a merge of the base in a second push.
func TestC15ReviserPushOfOneEditSetLands(t *testing.T) {
	e := newEnv(t)
	id := e.reviserHolds()
	commit(t, e.checkout, "revise", map[string]string{prdPath: "A\nb\nc\nd\ne\n"})
	commit(t, e.checkout, "notes", map[string]string{"notes.txt": "n\n"})
	mustOK(t, e.reviserPush(id))
	e.baseMoves(t, map[string]string{"other.txt": "o\n"})
	gitT(t, e.checkout, "merge", "-q", "--no-ff", "-m", "Merge main", "main")
	mustOK(t, e.reviserPush(id))
	if got, want := e.remoteSHA(BranchFor(id)), gitT(t, e.checkout, "rev-parse", "HEAD"); got != want {
		t.Fatalf("remote %s = %q, want %s", BranchFor(id), got, want)
	}
}

// C15: two commits that edit the PRD in one push split the edit set, and
// the refusal pushes nothing.
func TestC15ReviserPushOfTwoEditsIsRefused(t *testing.T) {
	e := newEnv(t)
	id := e.reviserHolds()
	before := e.remoteSHA(BranchFor(id))
	commit(t, e.checkout, "revise", map[string]string{prdPath: "two\n"})
	commit(t, e.checkout, "revise again", map[string]string{prdPath: "three\n"})
	wantEditSetSplit(t, e.reviserPush(id))
	if got := e.remoteSHA(BranchFor(id)); got != before {
		t.Fatalf("remote %s = %q after the refusal, want %s", BranchFor(id), got, before)
	}
}

// C15: one commit that edits the PRD in each of two pushes of one run
// splits the edit set too.
func TestC15ReviserSecondPushOfAnEditIsRefused(t *testing.T) {
	e := newEnv(t)
	id := e.reviserHolds()
	first := commit(t, e.checkout, "revise", map[string]string{prdPath: "two\n"})
	mustOK(t, e.reviserPush(id))
	commit(t, e.checkout, "revise again", map[string]string{prdPath: "three\n"})
	wantEditSetSplit(t, e.reviserPush(id))
	if got := e.remoteSHA(BranchFor(id)); got != first {
		t.Fatalf("remote %s = %q after the refusal, want %s", BranchFor(id), got, first)
	}
}

// C15: the epic's state names the document that the count reads.
func TestC15TheEpicStateNamesTheRevisedDocument(t *testing.T) {
	epic := func(status string, facts map[string]string) machine.Item {
		return machine.Item{ID: "ep-1", Kind: machine.KindEpic, Status: status, Facts: facts}
	}
	cases := []struct {
		it    machine.Item
		path  string
		edits bool
	}{
		{epic(machine.PRDAmending, map[string]string{machine.FactPRDPath: prdPath}), prdPath, true},
		{epic(machine.PRDAmending, map[string]string{machine.FactPRDPath: prdPath}), "docs/prd/other.md", false},
		{epic(machine.PRDAmending, nil), "docs/prd/other.md", true},
		{epic(machine.SpecAmending, nil), "docs/specs/ep-1-cap-spec.md", true},
		{epic(machine.SpecAmending, nil), "docs/plans/ep-1-cap-spec.md", true},
		{epic(machine.SpecAmending, nil), "docs/specs/ep-2-cap-spec.md", false},
		{epic(machine.SpecAmending, nil), prdPath, false},
	}
	for _, tc := range cases {
		doc := revisedDocument(tc.it)
		if doc == nil || doc(tc.path) != tc.edits {
			t.Errorf("%s: %s edits the document = %v, want %v", tc.it.Status, tc.path, doc != nil && doc(tc.path), tc.edits)
		}
	}
	for _, it := range []machine.Item{epic(machine.PRDReview, nil), {ID: "tk-1", Kind: machine.KindTicket, Status: machine.InProgress}} {
		if revisedDocument(it) != nil {
			t.Errorf("%s %s names a revised document", it.Kind, it.Status)
		}
	}
}
