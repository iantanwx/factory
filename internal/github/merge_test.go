package github

import (
	"strconv"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

const (
	widgetsPRD = "docs/prd/widgets.md"
	// uiConfig is a project config that declares a UI.
	uiConfig = "merge:\n  mechanism: direct\nui:\n  kind: web\n  paths: [web/**]\n"
	// plainConfig is a project config that declares no UI.
	plainConfig = "merge:\n  mechanism: direct\n"
)

// prdText is a PRD with one story for each UI line, labelled US-01 on.
func prdText(ui ...string) string {
	text := "# Widgets\n\n## User stories\n\n"
	for i, v := range ui {
		text += "### US-0" + string(rune('1'+i)) + " A story\n\n**UI:** " + v + "\n\n"
	}
	return text
}

// C12, C14: the reactor reads the project config and the PRD at the merge
// commit of a PRD pull request. In a project that declares a UI, a PRD
// with a UI: yes story lands on design, with ui_stories holding each such
// label. A project that declares no UI, or a PRD whose stories are all
// UI: no, lands on spec_needed with no ui_stories, as today.
func TestC12C14ReactorRoutesAPRDMergeByItsUIStories(t *testing.T) {
	for _, c := range []struct {
		name   string
		files  map[string]string
		status string
		ui     string
	}{
		{"a UI project with UI stories", map[string]string{".loop/config.yaml": uiConfig, widgetsPRD: prdText("yes", "no", "yes")},
			machine.Design, "US-01,US-03"},
		{"a project that declares no UI", map[string]string{".loop/config.yaml": plainConfig, widgetsPRD: prdText("yes", "no", "yes")},
			machine.SpecNeeded, ""},
		{"a project with no config", map[string]string{widgetsPRD: prdText("yes")}, machine.SpecNeeded, ""},
		{"a UI project with no UI story", map[string]string{".loop/config.yaml": uiConfig, widgetsPRD: prdText("no", "no")},
			machine.SpecNeeded, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			w := e.withWorld()
			pr := w.Add(&gt.PR{Head: "loop/placeholder", HeadSHA: shaA})
			id := e.prdEpicIn(machine.PRDSignoff, map[string]string{machine.FactDocPR: strconv.Itoa(pr.Number), machine.FactPRDPath: widgetsPRD,
				machine.FactReviewedHead: shaA})
			for path, text := range c.files {
				w.SetFile(shaM, path, text)
			}
			w.Update(pr, func(p *gt.PR) { p.Head, p.State, p.Merged, p.MergeSHA = BranchFor(id), "closed", true, shaM })
			e.tick(e.reactor())
			it := e.status(id)
			if it.Status != c.status || it.Fact(machine.FactUIStories) != c.ui || it.Fact(machine.FactPRDMerged) != shaM {
				t.Fatalf("after the PRD merge: %s with ui_stories=%q prd_merged=%q, want %s with %q and %s",
					it.Status, it.Fact(machine.FactUIStories), it.Fact(machine.FactPRDMerged), c.status, c.ui, shaM)
			}
		})
	}
}

// C13, C14: an ADR that lands for an epic in architecture whose ui_stories
// holds a label moves it to design. The same epic moves to spec_needed
// when it already holds design_merged or design_skipped, and so does an
// epic with no UI story.
func TestC13C14ADRMergeRoutesAnEpicByItsDesignOutcome(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	r := e.reactor()
	ui := map[string]string{machine.FactPRDMerged: "m1", machine.FactUIStories: "US-01"}
	with := func(k, v string) map[string]string {
		out := map[string]string{k: v}
		for fk, fv := range ui {
			out[fk] = fv
		}
		return out
	}
	want := map[string]string{
		e.prdEpicIn(machine.Architecture, ui):                                                       machine.Design,
		e.prdEpicIn(machine.Architecture, with(machine.FactDesignMerged, "d1")):                     machine.SpecNeeded,
		e.prdEpicIn(machine.Architecture, with(machine.FactDesignSkipped, "The operator: skip it")): machine.SpecNeeded,
		e.prdEpicIn(machine.Architecture, map[string]string{machine.FactPRDMerged: "m1"}):           machine.SpecNeeded,
	}
	files := map[string]string{}
	n := 7
	for id := range want {
		files[adrPath(n)] = adrText(id)
		n++
	}
	e.land(r, files)
	for id, status := range want {
		if it := e.status(id); it.Status != status || it.Fact(machine.FactADR) == "" {
			t.Errorf("epic %s with %v: %s with adr=%q, want %s", id, it.Facts, it.Status, it.Fact(machine.FactADR), status)
		}
	}
}

// C13: an epic in design whose adr fact names an ADR on the base whose
// Ticket line names another epic returns to architecture.
func TestC13ADRThatNamesAnotherEpicReturnsAnEpicInDesign(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	r := e.reactor()
	id := e.prdEpicIn(machine.Design, map[string]string{machine.FactPRDMerged: "m1", machine.FactUIStories: "US-01",
		machine.FactADR: adrPath(7), machine.FactADRMerged: "a1"})
	e.land(r, map[string]string{adrPath(7): adrText("tk-zz", id)})
	if it := e.status(id); it.Status != machine.Architecture || it.Fact(machine.FactADR) != "" {
		t.Fatalf("epic %s: %s with adr=%q, want architecture with no adr", id, it.Status, it.Fact(machine.FactADR))
	}
}

// An epic in a design state writes on loop/<epic>-design, and the design
// branch names its epic.
func TestDesignBranchOfAnEpicInADesignState(t *testing.T) {
	for _, status := range []string{machine.Design, machine.DesignReview, machine.DesignSignoff} {
		it := machine.Item{ID: "tk-1", Kind: machine.KindEpic, Status: status, Facts: map[string]string{machine.FactPRDMerged: "m1"}}
		if got := BranchOf(it); got != "loop/tk-1-design" {
			t.Errorf("BranchOf an epic in %s = %q, want loop/tk-1-design", status, got)
		}
	}
	if got := DesignEpicOf("loop/tk-1-design"); got != "tk-1" {
		t.Errorf("DesignEpicOf(loop/tk-1-design) = %q, want tk-1", got)
	}
	for _, b := range []string{"loop/tk-1", "loop/tk-1-spec", "loop/-design", "main"} {
		if got := DesignEpicOf(b); got != "" {
			t.Errorf("DesignEpicOf(%s) = %q, want none", b, got)
		}
	}
}
