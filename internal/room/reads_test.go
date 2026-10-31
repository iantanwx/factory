package room

import "testing"

// A reason names an item only by its whole id.
func TestReasonNamesWholeItemIDs(t *testing.T) {
	for _, c := range []struct {
		text, item string
		want       bool
	}{
		{"split factorio-a4yq into two epics", "factorio-a4yq", true},
		{"read factorio-a4yq.", "factorio-a4yq", true},
		{"(factorio-a4yq, factorio-vlle)", "factorio-vlle", true},
		{"factorio-a4yq", "factorio-a4yq", true},
		{"split tk-10 into two epics", "tk-1", false},
		{"split xtk-1 into two epics", "tk-1", false},
		{"split epic-a.1 into two epics", "epic-a", false},
		{"tk-10 and tk-1", "tk-1", true},
		{"no id here", "tk-1", false},
	} {
		if got := names(c.text, c.item); got != c.want {
			t.Errorf("names(%q, %q) = %v, want %v", c.text, c.item, got, c.want)
		}
	}
}
