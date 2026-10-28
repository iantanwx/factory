package overwatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A worktree under the checkout root whose repository no project
// registers belongs to another loop server, such as the orca acceptance
// lane's fixture server. The worktrees part reports no line for it (F-50).
func TestWorktreesIgnoresAWorktreeOfAnUnregisteredRepository(t *testing.T) {
	w := newSchedWorld(t)
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	fixture := filepath.Join(t.TempDir(), "fixture")
	git(filepath.Dir(fixture), "init", "-q", "-b", "main", fixture)
	git(fixture, "commit", "-q", "--allow-empty", "-m", "seed")
	name := "loop-demo-code-reviewer-demo-9pg-add-a-greeting-r-645f1481b0ab"
	git(fixture, "worktree", "add", "-q", "-b", "reviewer", filepath.Join(w.root, "fixture", name))

	p := readWorktrees(t, w)
	if strings.Contains(strings.Join(p.Lines, "\n"), name) {
		t.Fatalf("code %d, lines %q; another server's worktree is no work", p.Code, p.Lines)
	}
}
