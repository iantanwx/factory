package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/gitx/gittest"
)

// C10: `branch push --captures <dir>` makes one commit with no parent in
// the checkout, whose tree holds each file of dir with its bytes, and
// keeps it at refs/loop/captures/<head>. The same files give the same
// commit. The content of the push holds that commit, so a push of other
// captures at the same head is a new write.
func TestC10CapturesPushMakesAParentlessCommitOfTheFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := gittest.Command(append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "work")
	head := git("rev-parse", "HEAD")
	out := t.TempDir()
	files := map[string]string{"S1.1.png": "\x89PNG one", "S1.2.png": "\x89PNG two"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(out, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	sha, err := CaptureCommit(ctx, repo, out, head)
	if err != nil {
		t.Fatal(err)
	}
	if parents := git("rev-list", "--parents", "-n", "1", sha); parents != sha {
		t.Fatalf("the capture commit has parents: %q", parents)
	}
	if got := git("rev-parse", "refs/loop/captures/"+head); got != sha {
		t.Fatalf("refs/loop/captures/%s = %q, want %s", head, got, sha)
	}
	if names := git("ls-tree", "--name-only", sha); names != "S1.1.png\nS1.2.png" {
		t.Fatalf("the capture tree holds %q", names)
	}
	for name, body := range files {
		if got := git("cat-file", "blob", sha+":"+name); got != body {
			t.Errorf("%s holds %q, want %q", name, got, body)
		}
	}
	if again, err := CaptureCommit(ctx, repo, out, head); err != nil || again != sha {
		t.Fatalf("the same files make %s %v, want %s", again, err, sha)
	}

	push, _ := New().Verb("branch push")
	inv := Invocation{Verb: push, Flags: map[string]string{"captures": out}}
	before, err := ContentHash(ctx, inv, repo, repo)
	if err != nil || before != PushContentHash(head, sha) || before == HeadContentHash(head) {
		t.Fatalf("content %s %v, want the hash of %s with its captures %s", before, err, head, sha)
	}
	if err := os.WriteFile(filepath.Join(out, "S1.2.png"), []byte("\x89PNG three"), 0o600); err != nil {
		t.Fatal(err)
	}
	if after, err := ContentHash(ctx, inv, repo, repo); err != nil || after == before {
		t.Fatalf("other captures at the same head hash %s %v, the same as before", after, err)
	}
}

// A directory inside the captures directory is no capture, and the CLI
// sends nothing.
func TestCapturesPushRefusesADirectoryInTheSet(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	if out, err := gittest.Command("-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	out := t.TempDir()
	if err := os.Mkdir(filepath.Join(out, "S1.1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureCommit(context.Background(), repo, out, strings.Repeat("a", 40)); err == nil {
		t.Fatal("a directory in the set made a capture commit")
	}
}
