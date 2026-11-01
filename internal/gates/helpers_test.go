package gates

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// testRepo makes a git checkout with base committed on main, as origin/main,
// and returns a Repo whose Base is origin/main. Commit more with commit.
func testRepo(t *testing.T, base map[string]string) *Repo {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	write(t, dir, base)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "chore: start")
	git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	r, err := Open(context.Background(), dir, "origin/main", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// cloneRepo clones src, whose main is then origin/main of the clone.
func cloneRepo(t *testing.T, src string) *Repo {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, src, "clone", "-q", src, dir)
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	r, err := Open(context.Background(), dir, "origin/main", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// commit writes files and commits them with msg on the current branch.
func commit(t *testing.T, r *Repo, msg string, files map[string]string) {
	t.Helper()
	write(t, r.Root, files)
	git(t, r.Root, "add", "-A")
	git(t, r.Root, "commit", "-q", "--allow-empty", "-m", msg)
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// git runs git in dir and returns its output. The git starts no auto
// maintenance or auto gc, which could still write into the repository after
// git returns and race t.TempDir's cleanup (factorio-d14g).
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// rules lists the rule of each finding.
func rules(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return out
}
