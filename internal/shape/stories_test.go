package shape_test

import (
	"reflect"
	"testing"

	"github.com/iantanwx/loop/internal/shape"
)

// storiesPRD holds one story for each UI mark, a fenced example and a
// mark in prose.
const storiesPRD = "# Fleet\n\n## Stories\n\n" +
	"### US-01 See the fleet\n\n**UI:** yes\n\n> \"show me\"\n\n" + // 5
	"### US-02 Export the fleet\n\n**UI:** no\n\n" + // 11
	"### US-03 Rename a ship\n\nThe line `**UI:** yes` in prose marks nothing.\n\n" + // 15
	"### US-04 Sort the fleet\n\n**UI:** maybe\n\n" + // 19
	"### US-05 Filter the fleet\n\n**UI:** yes\n**UI:** no\n\n" + // 23
	"```\n### US-09 An example\n\n**UI:** yes\n```\n\n" +
	"### US-06 Pin a ship\n\n## Requirements\n\n**UI:** yes\n" // 34

func TestStoriesReadEachStoryAndItsUILine(t *testing.T) {
	want := []shape.Story{
		{Label: "US-01", Line: 5, UI: shape.UIYes},
		{Label: "US-02", Line: 11, UI: shape.UINo},
		{Label: "US-03", Line: 15, UI: shape.UIMissing},
		{Label: "US-04", Line: 19, UI: shape.UIBad},
		{Label: "US-05", Line: 23, UI: shape.UIBad},
		// The UI line under the next heading belongs to no story.
		{Label: "US-06", Line: 34, UI: shape.UIMissing},
	}
	if got := shape.Stories(storiesPRD); !reflect.DeepEqual(got, want) {
		t.Fatalf("Stories =\n%+v\nwant\n%+v", got, want)
	}
}

func TestUIStoriesListOnlyTheYesLabels(t *testing.T) {
	if got := shape.UIStories(storiesPRD); !reflect.DeepEqual(got, []string{"US-01"}) {
		t.Fatalf("UIStories = %v", got)
	}
	if got := shape.UIStories("# No stories\n"); got != nil {
		t.Fatalf("UIStories of no story = %v", got)
	}
}

func TestUIFaultNamesAMissingOrBadUILine(t *testing.T) {
	var got []string
	for _, s := range shape.Stories(storiesPRD) {
		got = append(got, s.Label+": "+s.UIFault())
	}
	want := []string{
		"US-01: ",
		"US-02: ",
		"US-03: holds no **UI:** line",
		"US-04: holds two **UI:** lines, or one that reads neither yes nor no",
		"US-05: holds two **UI:** lines, or one that reads neither yes nor no",
		"US-06: holds no **UI:** line",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UIFault =\n%q\nwant\n%q", got, want)
	}
}
