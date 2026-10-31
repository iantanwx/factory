package pipeline_test

import (
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// prd submit records only the labels that a story heading of the PRD
// defines. A label in prose that cites a story of another PRD is no story
// of this one, so no ticket can cite it.
func TestPRDSubmitRecordsOnlyStoryHeadings(t *testing.T) {
	w := newWorld(t)
	w.commitPRD("# Widgets\n\n## US-01 A trader sees widgets\n\n## US-02 A trader sorts widgets\n\n" +
		"## Boundaries with other epics\n\nThe design stage owns US-07 of docs/prd/design-stage.md.\n")
	data := ok(t, w.submit("k-1"))
	var got []string
	for _, s := range data["stories"].([]any) {
		got = append(got, s.(string))
	}
	if want := []string{"US-01", "US-02"}; !slices.Equal(got, want) {
		t.Fatalf("stories = %v, want %v", got, want)
	}
}

// C-bug-1: a story heading in a fenced block is an example, and a label
// in a table or a citation is no heading. A PRD that defines no story
// heading outside a fence holds no story, so submit refuses it.
func TestPRDSubmitRefusesAPRDWithStoryLabelsOnlyOutsideHeadings(t *testing.T) {
	w := newWorld(t)
	w.commitPRD("# Widgets\n\n| Story | Owner |\n| --- | --- |\n| US-01 | design |\n\n" +
		"## Requirements\n\n- Sort widgets.\n  → § US-02\n\n```\n## US-03 An example story\n```\n")
	if r := wantCode(t, w.submit("k-1"), cli.CodeUsage); r.Rule != "no_story_labels" {
		t.Fatalf("rule = %s, want no_story_labels", r.Rule)
	}
}

// C-bug-1: work create checks a cited story against the story headings
// of the merged PRD. A label the PRD names only in prose is no story of
// it.
func TestWorkCreateRefusesAStoryTheMergedPRDNamesOnlyInProse(t *testing.T) {
	w := newWorld(t)
	epic := w.adrBound()
	w.onBase(map[string]string{prdPath: prdText + "\n## Boundaries with other epics\n\nThe design stage owns US-07 of docs/prd/design-stage.md.\n"})
	res := w.call(w.coordinator(), "", "work create", map[string]string{"project": testProject, "epic": epic, "story": "US-07",
		"request": sendWords, "title": "Decide the widget store", "body": "Write the ADR that decides where widgets live."})
	if r := wantCode(t, res, cli.CodeStoryUnknown); r.Rule != "story_not_in_prd" {
		t.Fatalf("rule = %s, want story_not_in_prd", r.Rule)
	}
}
