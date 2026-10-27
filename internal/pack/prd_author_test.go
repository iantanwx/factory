package pack_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

const prdAuthorRunbook = "runbooks/prd-author.md"

// C15: the manifest names the prd-author runbook, unattended, with the six
// skills of spec factorio-4n0 Work entry 4, and the runbook names itself
// loop-prd-author.
func TestPRDAuthorRunbookIsUnattendedWithItsSkills(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	rb, ok := m.Runbooks["prd-author"]
	if !ok {
		t.Fatal("the manifest names no prd-author runbook")
	}
	if rb.Attended {
		t.Error("the prd-author runs with nobody present, and its runbook says it is attended")
	}
	want := []string{"loop-sdlc-turn", "loop-sdlc-glossary", "loop-sdlc-triage", "loop-sdlc-split", "loop-sdlc-documents", "loop-brainstorm"}
	if !slices.Equal(slices.Sorted(slices.Values(rb.Skills)), slices.Sorted(slices.Values(want))) {
		t.Errorf("prd-author skills = %v, want %v", rb.Skills, want)
	}
	if !strings.Contains(read(t, prdAuthorRunbook), "\nname: loop-prd-author\n") {
		t.Error("the prd-author runbook does not name itself loop-prd-author")
	}
}

// C15: ADR 0001 D4 ports no intake author. Outside tests, pack, internal
// and cmd hold no name of tickertown's intake author or its coverage verb.
// The walk must read the PRD author's runbook, so a walk that reads
// nothing cannot pass.
func TestNoIntakeAuthorIsPorted(t *testing.T) {
	root := filepath.Join("..", "..")
	banned := []string{"intake-author", "sdlc-intake", "intake coverage"}
	readRunbook := false
	for _, dir := range []string{"pack", "internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(path, "_test.go") {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if filepath.ToSlash(path) == "../../pack/"+prdAuthorRunbook {
				readRunbook = true
			}
			for _, b := range banned {
				if strings.Contains(string(raw), b) {
					t.Errorf("%s names %q", path, b)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !readRunbook {
		t.Errorf("the walk did not read pack/%s", prdAuthorRunbook)
	}
}

// C18: the PRD author's runbook holds ADR 0001's source rule. The request
// and amendment facts are the only sources a story quotes, a requirement
// with no source goes under Open questions, a request with no story goes
// to a level 4 escalation, and the author never splits.
func TestPRDAuthorRunbookHoldsTheSourceRule(t *testing.T) {
	text := strings.Join(strings.Fields(read(t, prdAuthorRunbook)), " ")
	for _, want := range []string{
		"These two facts are the only source a story quotes",
		"the `request` fact",
		"the `amendment` fact",
		"A requirement with no source goes under Open questions",
		"`loop escalation raise --score 4`",
		"Never split",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the prd-author runbook does not hold %q", want)
		}
	}
}
