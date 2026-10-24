package github

import (
	"context"
	"path/filepath"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// C-bug-1 (F-14): the server reads the head the remote holds for the base
// branch through the mirror with the token, and brings that commit into
// the project clone, so a new checkout starts from it. The clone's local
// main stays where a person left it.
func TestFetchBaseBringsTheRemoteBaseHeadIntoTheClone(t *testing.T) {
	e := newEnv(t)
	stale := gt.Git(t, e.checkout, "rev-parse", "main")

	// Another clone merges a change, so the remote base moves on.
	other := filepath.Join(t.TempDir(), "other")
	gt.Git(t, filepath.Dir(other), "clone", "-q", e.git.RemoteDir(), other)
	head := commit(t, other, "merged", map[string]string{"merged.txt": "merged\n"})
	gt.Git(t, other, "push", "-q", "origin", "HEAD:main")
	if head == stale {
		t.Fatal("the remote base did not move")
	}

	got, err := e.svc.FetchBase(context.Background(), testProject, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got != head {
		t.Fatalf("FetchBase = %s, want the remote base head %s", got, head)
	}
	if c := gt.Git(t, e.checkout, "cat-file", "-t", head); c != "commit" {
		t.Fatalf("the clone lacks the base head: %s", c)
	}
	if o := gt.Git(t, e.checkout, "rev-parse", "refs/remotes/origin/main"); o != head {
		t.Fatalf("origin/main in the clone = %s, want %s", o, head)
	}
	if m := gt.Git(t, e.checkout, "rev-parse", "main"); m != stale {
		t.Fatalf("the clone's local main moved to %s, want %s", m, stale)
	}
}

// FetchBase reads the base branch it is given, so a project whose base is
// not main starts each checkout at that branch's remote head.
func TestFetchBaseReadsTheGivenBranch(t *testing.T) {
	e := newEnv(t)
	main := gt.Git(t, e.checkout, "rev-parse", "main")

	other := filepath.Join(t.TempDir(), "other")
	gt.Git(t, filepath.Dir(other), "clone", "-q", e.git.RemoteDir(), other)
	trunk := commit(t, other, "trunk", map[string]string{"trunk.txt": "trunk\n"})
	gt.Git(t, other, "push", "-q", "origin", "HEAD:refs/heads/trunk")
	if trunk == main {
		t.Fatal("trunk is main")
	}

	got, err := e.svc.FetchBase(context.Background(), testProject, "trunk")
	if err != nil {
		t.Fatal(err)
	}
	if got != trunk {
		t.Fatalf("FetchBase(trunk) = %s, want the head of trunk %s", got, trunk)
	}
	if o := gt.Git(t, e.checkout, "rev-parse", "refs/remotes/origin/trunk"); o != trunk {
		t.Fatalf("origin/trunk in the clone = %s, want %s", o, trunk)
	}
}
