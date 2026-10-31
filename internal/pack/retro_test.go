package pack_test

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/retro"
)

const retroRunbook = "runbooks/retro.md"

// C28: the manifest names the retro runbook, unattended, with the retro
// rubric among its skills, and the runbook names itself loop-retro.
func TestC28RetroRunbookIsUnattendedWithTheRubric(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	rb, ok := m.Runbooks["retro"]
	if !ok {
		t.Fatal("the manifest names no retro runbook")
	}
	if rb.Attended {
		t.Error("the retro runs with nobody present, and its runbook says it is attended")
	}
	if !slices.Contains(rb.Skills, "loop-retro-rubric") {
		t.Errorf("retro skills = %v, want loop-retro-rubric among them", rb.Skills)
	}
	if !strings.Contains(read(t, retroRunbook), "\nname: loop-retro\n") {
		t.Error("the retro runbook does not name itself loop-retro")
	}
}

// C28: the runbook runs record, held, judge and route in that order, and
// finishes after the route.
func TestC28RetroRunbookEndsEveryRetroWithARoute(t *testing.T) {
	text := read(t, retroRunbook)
	steps := []string{"loop retro record", "loop retro held", "loop retro judge --file", "loop retro route --file", "loop run finish --outcome done"}
	last := -1
	for _, s := range steps {
		i := strings.Index(text, s)
		if i < 0 {
			t.Fatalf("the retro runbook never runs %q", s)
		}
		if i < last {
			t.Fatalf("the retro runbook runs %q before the step ahead of it; want the order %q", s, steps)
		}
		last = i
	}
}

// C28: a run that finds no learning sends an empty panel file, and the
// route reads that file as zero panels.
func TestC28RetroRunbookSendsAnEmptyPanelFile(t *testing.T) {
	line := regexp.MustCompile(`(?m)^.*'(\{"panels": \[\]\})'.*panels\.json$`).FindStringSubmatch(read(t, retroRunbook))
	if line == nil {
		t.Fatal(`the retro runbook writes no empty panel file: want a line that writes '{"panels": []}' to panels.json`)
	}
	ps, err := retro.ParsePanels([]byte(line[1]))
	if err != nil {
		t.Fatalf("route refuses the runbook's empty panel file %s: %v", line[1], err)
	}
	if len(ps) != 0 {
		t.Fatalf("route reads %d panels from %s, want none", len(ps), line[1])
	}
}

// C28: the runbook's learnings file names each case by a reference that
// the route accepts.
func TestC28RetroRunbookNamesEachCaseByItsReference(t *testing.T) {
	block := regexp.MustCompile("(?s)```json\n(\\{\"learnings\".*?)```").FindStringSubmatch(read(t, retroRunbook))
	if block == nil {
		t.Fatal("the retro runbook shows no learnings file")
	}
	var f struct {
		Learnings []retro.Learning `json:"learnings"`
	}
	if err := json.Unmarshal([]byte(block[1]), &f); err != nil {
		t.Fatalf("the runbook's learnings file does not parse: %v\n%s", err, block[1])
	}
	if len(f.Learnings) == 0 {
		t.Fatalf("the runbook's learnings file holds no learning:\n%s", block[1])
	}
	for i, l := range f.Learnings {
		if len(l.Cases) == 0 {
			t.Errorf("learning %d of the runbook's file names no case", i+1)
		}
		for _, c := range l.Cases {
			if _, err := retro.Parse(string(c)); err != nil {
				t.Errorf("learning %d names case %q: %v", i+1, c, err)
			}
		}
	}
}

// C28: the route lands each learning, so the run itself opens no pull
// request, creates no item and files no loop fault.
func TestC28RetroRunbookLandsNothingItself(t *testing.T) {
	text := read(t, retroRunbook)
	for _, banned := range []string{"loop pr open", "loop work create", "loop fault report"} {
		if strings.Contains(text, banned) {
			t.Errorf("the retro runbook names %q; only `loop retro route` lands a learning", banned)
		}
	}
}
