package pack_test

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

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

// C9: the prd-author rename retires its words, and no file of the pack
// holds one.
func TestC9PackHoldsNoPRDAuthorWord(t *testing.T) {
	words := []string{"prd-author", "PRD author"}
	for _, w := range words {
		if !slices.Contains(config.RetiredWords(), w) {
			t.Errorf("RetiredWords() lacks %q", w)
		}
	}
	if hits := retiredHits(t, packsrc.Files, words); len(hits) > 0 {
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

// C9, work entry 3: amender is a retired word, and no file of the pack
// holds it.
func TestC9PackHoldsNoAmender(t *testing.T) {
	if !slices.Contains(config.RetiredWords(), "amender") {
		t.Errorf("RetiredWords() = %v, want amender", config.RetiredWords())
	}
	if hits := retiredHits(t, packsrc.Files, []string{"amender"}); len(hits) > 0 {
		t.Errorf("the pack names the amender:\n%s", strings.Join(hits, "\n"))
	}
}

// C10, work entry 3: the reviser's runbook lays out as loop-reviser/SKILL.md,
// and the manifest holds no amender.
func TestC10TheReviserHasItsRunbookUnderItsNewName(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Runbooks["reviser"]; !ok {
		t.Error("the manifest holds no reviser runbook")
	}
	if _, ok := m.Runbooks["amender"]; ok {
		t.Error("the manifest still holds the amender runbook")
	}
	laid, err := pack.Layout(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	b, err := fs.ReadFile(laid, "loop-reviser/SKILL.md")
	if err != nil || !strings.Contains(string(b), "name: loop-reviser\n") {
		t.Fatalf("loop-reviser/SKILL.md = %q, %v, want the reviser's runbook", b, err)
	}
}
