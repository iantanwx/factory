package github

import (
	"context"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// C3: AddedBy answers the newest first-parent commit that adds the path:
// the second add of an ADR added, deleted and added again, and the merge
// commit, not its second parent, for an ADR that a merge brought in. A
// later commit that edits the file adds nothing, and a path no commit
// adds answers "".
func TestC3AddedByIsTheNewestFirstParentAdd(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	commit(t, dir, "base", map[string]string{"README.md": "hello\n"})
	commit(t, dir, "add", map[string]string{"docs/adr/0001-x.md": "one\n"})
	commit(t, dir, "delete", map[string]string{"docs/adr/0001-x.md": ""})
	again := commit(t, dir, "add again", map[string]string{"docs/adr/0001-x.md": "two\n"})
	commit(t, dir, "edit", map[string]string{"docs/adr/0001-x.md": "three\n"})
	gitT(t, dir, "checkout", "-q", "-b", "side")
	side := commit(t, dir, "side", map[string]string{"docs/adr/0002-y.md": "y\n"})
	gitT(t, dir, "checkout", "-q", "main")
	commit(t, dir, "main", map[string]string{"x.txt": "x\n"})
	gitT(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "side")
	merge := gitT(t, dir, "rev-parse", "HEAD")
	tip := commit(t, dir, "later", map[string]string{"y.txt": "y\n"})

	m := &Mirror{git: &Git{Home: t.TempDir()}, Dir: dir}
	for _, c := range []struct{ path, want string }{
		{"docs/adr/0001-x.md", again},
		{"docs/adr/0002-y.md", merge},
		{"docs/adr/0003-z.md", ""},
	} {
		got, err := m.AddedBy(context.Background(), tip, c.path)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("AddedBy(%s) = %q, want %q (the side commit is %s, the tip %s)", c.path, got, c.want, side, tip)
		}
	}
}

// C4: Tree lists the entries git tracks directly under a directory, and
// marks each directory. A directory or a ref the repository lacks returns
// no entry and no error.
func TestC4MirrorTreeListsTheTrackedEntries(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	commit(t, dir, "skills", map[string]string{
		".claude/skills/neon/SKILL.md":      "neon\n",
		".claude/skills/neon/ref/a.md":      "a\n",
		".claude/skills/house-style":        "a file\n",
		".claude/skills-old/stale/SKILL.md": "old\n",
	})

	m := &Mirror{git: &Git{Home: t.TempDir()}, Dir: dir}
	ctx := context.Background()
	got, err := m.Tree(ctx, "main", ".claude/skills")
	want := []config.TreeEntry{{Name: "house-style"}, {Name: "neon", Dir: true}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("Tree(.claude/skills) = %+v, %v, want %+v", got, err, want)
	}
	for _, c := range []struct{ ref, dir string }{{"main", ".agents/skills"}, {"origin/main", ".claude/skills"}} {
		if got, err := m.Tree(ctx, c.ref, c.dir); err != nil || len(got) != 0 {
			t.Errorf("Tree(%s, %s) = %+v, %v, want no entry and no error", c.ref, c.dir, got, err)
		}
	}
}
