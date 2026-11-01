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
