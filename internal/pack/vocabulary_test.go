package pack_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
	packsrc "github.com/iantanwx/loop/pack"
)

// retiredHits names each file, line and retired word of fsys. A word
// matches whole, in any case.
func retiredHits(t *testing.T, fsys fs.FS, words []string) []string {
	t.Helper()
	var hits []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, w := range words {
				if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(w) + `\b`).MatchString(line) {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", path, i+1, w))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

// C9: no file of the embedded pack holds a retired word.
func TestC9PackHoldsNoRetiredWord(t *testing.T) {
	if hits := retiredHits(t, packsrc.Files, config.RetiredWords()); len(hits) > 0 {
		t.Fatalf("the pack holds a retired word:\n%s", strings.Join(hits, "\n"))
	}
}

// The walk finds a retired word whole, in any case, and names its file and
// line. It ignores the word inside a longer word.
func TestRetiredHitsNameFileLineAndWord(t *testing.T) {
	fsys := fstest.MapFS{"runbooks/a.md": {Data: []byte("# A\nAsk the Old-Stage run.\nThe old-stager is fine.\n")}}
	got := retiredHits(t, fsys, []string{"old-stage"})
	if len(got) != 1 || got[0] != "runbooks/a.md:2: old-stage" {
		t.Fatalf("hits = %q, want one at runbooks/a.md:2", got)
	}
}

// C9: the planner rename retires spec-author and spec author, and no file
// of the embedded pack holds either.
func TestC9PackNamesNoSpecAuthor(t *testing.T) {
	words := []string{"spec-author", "spec author"}
	for _, w := range words {
		if !slices.Contains(config.RetiredWords(), w) {
			t.Errorf("RetiredWords() holds no %q", w)
		}
	}
	if hits := retiredHits(t, packsrc.Files, words); len(hits) > 0 {
		t.Fatalf("the pack names the spec-author:\n%s", strings.Join(hits, "\n"))
	}
}

// C10: the planner's runbook ships under its new name, and lays out as
// loop-planner/SKILL.md where the brief names it. The manifest holds no
// spec-author.
func TestC10PlannerRunbookShipsUnderItsNewName(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Runbooks["spec-author"]; ok {
		t.Error("the manifest still holds the spec-author runbook")
	}
	if _, ok := m.Runbooks["planner"]; !ok {
		t.Fatal("the manifest holds no planner runbook")
	}
	p, _, err := pack.Brief(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := p.Write(dir, agenthost.Claude); err != nil {
		t.Fatal(err)
	}
	if got := brief.Runbook(agenthost.Claude, "planner"); got != ".claude/skills/loop-planner/SKILL.md" {
		t.Fatalf("the planner runbook path = %s", got)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", "loop-planner", "SKILL.md"))
	if err != nil || !strings.Contains(string(body), "name: loop-planner\n") {
		t.Fatalf("loop-planner/SKILL.md holds no planner runbook: %v", err)
	}
}
