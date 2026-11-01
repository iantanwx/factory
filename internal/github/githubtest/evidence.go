package githubtest

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/gitx/gittest"
)

// remoteGit runs git in the bare remote of Repo.
func (g *GitServer) remoteGit(args ...string) (string, error) {
	cmd := gittest.Command(args...)
	cmd.Dir = g.RemoteDir()
	cmd.Env = gittest.Env([]string{"PATH=" + os.Getenv("PATH")})
	out, err := cmd.Output()
	return string(out), err
}

// RefSHA reads any ref of the bare remote, or "".
func (g *GitServer) RefSHA(ref string) string {
	out, _ := g.remoteGit("rev-parse", "--verify", "--quiet", ref)
	return strings.TrimSpace(out)
}

// Parents lists the parents of commit in the bare remote.
func (g *GitServer) Parents(commit string) []string {
	out, _ := g.remoteGit("rev-list", "--parents", "-n", "1", commit)
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return nil
	}
	return fields[1:]
}

// Paths lists each file path of commit's tree in the bare remote, sorted.
func (g *GitServer) Paths(commit string) []string {
	out, _ := g.remoteGit("ls-tree", "-r", "--name-only", commit)
	return strings.Fields(out)
}

// Serve answers a link https://github.com/<owner>/<repo>/blob/<commit>/<path>
// as GitHub would: the bytes of path in commit, from the bare remote of
// Repo. A link to another repository, or to a path the commit lacks, is
// an error.
func (g *GitServer) Serve(link string) ([]byte, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	rest, ok := strings.CutPrefix(u.Path, "/"+Repo+"/blob/")
	if u.Scheme != "https" || u.Host != "github.com" || !ok {
		return nil, fmt.Errorf("%s is no blob link of %s", link, Repo)
	}
	commit, path, _ := strings.Cut(rest, "/")
	out, err := g.remoteGit("cat-file", "blob", commit+":"+path)
	if err != nil {
		return nil, fmt.Errorf("404: %s holds no %s", commit, path)
	}
	return []byte(out), nil
}

// RefusePushes makes the bare remote decline each push to a ref that
// starts with prefix, as a GitHub ruleset does.
func (g *GitServer) RefusePushes(t testing.TB, prefix string) {
	t.Helper()
	hook := "#!/bin/sh\nwhile read old new ref; do\n\tcase \"$ref\" in " + prefix +
		"*) echo \"refusing $ref\" >&2; exit 1;; esac\ndone\n"
	path := filepath.Join(g.RemoteDir(), "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
}
