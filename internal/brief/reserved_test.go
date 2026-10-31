package brief

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
)

// C5: a tracked loop-* directory that the pack lacks refuses the run under
// either skill root, and the refused writer writes no injected file. A
// tracked directory outside the prefix refuses nothing.
func TestTrackedLoopDirThePackLacksRefusesTheRun(t *testing.T) {
	ctx := context.Background()
	writers := map[string]func(dir string) error{
		"Inject": func(dir string) error {
			_, err := Inject(ctx, Injection{Checkout: dir, Harness: agenthost.Claude, Pack: testPack(), Brief: testBrief()})
			return err
		},
		"RefreshPack": func(dir string) error { return RefreshPack(ctx, dir, agenthost.Claude, testPack()) },
	}
	for name, write := range writers {
		for _, tracked := range []string{".claude/skills/loop-x/SKILL.md", ".agents/skills/loop-x/SKILL.md"} {
			dir := gitRepo(t, tracked)
			err := write(dir)
			want := filepath.ToSlash(filepath.Dir(tracked))
			var re *ReservedError
			if !errors.As(err, &re) || !slices.Equal(re.Dirs, []string{want}) {
				t.Fatalf("%s, tracked %s: err = %v; want a *ReservedError for %s", name, tracked, err, want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: the error %q does not name %s", name, err, want)
			}
			for _, p := range []string{PromptFile, BriefJSON, Runbook(agenthost.Claude, "implementer"), ".claude/agents/loop-helper.md"} {
				if _, err := os.Stat(filepath.Join(dir, p)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s, tracked %s: the refused writer wrote %s", name, tracked, p)
				}
			}
		}
		dir := gitRepo(t, ".claude/skills/house-style/SKILL.md", ".agents/skills/house-style/SKILL.md")
		if err := write(dir); err != nil {
			t.Fatalf("%s: a tracked house-style skill refused the run: %v", name, err)
		}
	}
}

// C6: the writer clears each untracked loop-* directory that the pack
// lacks, under the harness root. It keeps every byte of an untracked
// directory outside the prefix, writes each pack directory, and changes no
// tracked file.
func TestWriterClearsOnlyStaleUntrackedLoopDirs(t *testing.T) {
	ctx := context.Background()
	writers := map[string]func(dir string) error{
		"Inject": func(dir string) error {
			_, err := Inject(ctx, Injection{Checkout: dir, Harness: agenthost.Claude, Pack: testPack(), Brief: testBrief()})
			return err
		},
		"RefreshPack": func(dir string) error { return RefreshPack(ctx, dir, agenthost.Claude, testPack()) },
	}
	house := map[string]string{
		".claude/skills/house-style/SKILL.md":     "# house style\n",
		".claude/skills/house-style/refs/tone.md": "plain\n",
	}
	for name, write := range writers {
		dir := gitRepo(t, ".claude/skills/committed/SKILL.md")
		for p, body := range map[string]string{
			".claude/skills/loop-neon/SKILL.md":       "# stale\n",
			".claude/skills/loop-neon/refs/branch.md": "stale\n",
		} {
			writeFile(t, dir, p, body)
		}
		for p, body := range house {
			writeFile(t, dir, p, body)
		}
		if err := write(dir); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".claude/skills/loop-neon")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: the stale .claude/skills/loop-neon is still there (%v)", name, err)
		}
		for p, want := range house {
			if got, err := os.ReadFile(filepath.Join(dir, p)); err != nil || string(got) != want {
				t.Fatalf("%s: %s = %q, %v; want %q", name, p, got, err, want)
			}
		}
		for _, s := range []string{"loop-implementer/SKILL.md", "loop-implementer/refs/checklist.md", "loop-code-reviewer/SKILL.md"} {
			if _, err := os.Stat(filepath.Join(dir, ".claude/skills", s)); err != nil {
				t.Fatalf("%s: the pack's %s is missing: %v", name, s, err)
			}
		}
		if out := git(t, dir, "status", "--porcelain", "--untracked-files=no"); out != "" {
			t.Fatalf("%s changed a tracked file:\n%s", name, out)
		}
	}
}

func writeFile(t *testing.T, dir, p, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
