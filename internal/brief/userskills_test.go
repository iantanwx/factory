package brief

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
)

func neonSkill() UserSkill {
	return UserSkill{
		Pin: config.SkillPin{Name: "neon", Repo: "neondatabase/agent-skills", Commit: strings.Repeat("c", 40),
			Path: "skills/neon", SHA256: strings.Repeat("a", 64)},
		Tree: fstest.MapFS{
			"SKILL.md":       {Data: []byte("---\nname: neon\n---\n")},
			"scripts/run.sh": {Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		},
	}
}

// C13: Inject writes a user skill under its own name, with each byte and
// mode, keeps its directory out of git, names it in the brief, and keeps
// the kernel's pack hash.
func TestC13InjectWritesAUserSkillUnderItsOwnName(t *testing.T) {
	dir := gitRepo(t)
	res, err := Inject(context.Background(), Injection{Checkout: dir, Harness: agenthost.Codex,
		Pack: testPack().WithUser([]UserSkill{neonSkill()}), Brief: testBrief()})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]struct {
		data string
		mode os.FileMode
	}{"SKILL.md": {"---\nname: neon\n---\n", 0o644}, "scripts/run.sh": {"#!/bin/sh\n", 0o755}} {
		p := filepath.Join(dir, ".agents/skills/neon", name)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want.data || fi.Mode().Perm() != want.mode {
			t.Errorf("%s = %q at %o, want %q at %o", name, got, fi.Mode().Perm(), want.data, want.mode)
		}
	}
	if out := git(t, dir, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("git status:\n%s", out)
	}
	if h, _ := testPack().Hash(); res.PackHash != h {
		t.Errorf("pack hash with a user skill = %s, kernel = %s", res.PackHash, h)
	}
	raw, err := os.ReadFile(filepath.Join(dir, BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	if want := `"user_skills": [
    {
      "name": "neon",
      "repo": "neondatabase/agent-skills",
      "commit": "` + strings.Repeat("c", 40) + `",
      "sha256": "` + strings.Repeat("a", 64) + `"
    }
  ]`; !strings.Contains(string(raw), want) {
		t.Errorf("brief.json lacks %s:\n%s", want, raw)
	}
}

// C13: a tracked file under a user skill's directory refuses the run, as a
// tracked kernel directory does.
func TestC13ATrackedUserSkillDirIsAConflict(t *testing.T) {
	dir := gitRepo(t, ".claude/skills/neon/SKILL.md")
	_, err := Inject(context.Background(), Injection{Checkout: dir, Harness: agenthost.Claude,
		Pack: testPack().WithUser([]UserSkill{neonSkill()}), Brief: testBrief()})
	var ce *ConflictError
	if !errors.As(err, &ce) || !slices.Contains(ce.Paths, ".claude/skills/neon/SKILL.md") {
		t.Fatalf("err = %v, want a conflict on .claude/skills/neon/SKILL.md", err)
	}
}

// C14: a user skill never takes a kernel name. A name in the loop- prefix,
// or a kernel directory's name, refuses the pack and writes nothing.
func TestC14AUserSkillNeverTakesAKernelName(t *testing.T) {
	for _, name := range []string{"loop-x", "loop-implementer"} {
		s := neonSkill()
		s.Pin.Name = name
		dir := t.TempDir()
		if err := testPack().WithUser([]UserSkill{s}).Write(dir, agenthost.Claude); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("user skill %s: err = %v, want a refusal that names it", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".claude")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("user skill %s: a refused pack wrote into the checkout", name)
		}
	}
}
