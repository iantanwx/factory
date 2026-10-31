package shape

import (
	"regexp"
	"slices"
)

// storyHeading is a heading that defines a story of a PRD (R45): US-01,
// US-02 and on.
var storyHeading = regexp.MustCompile(`^#{1,6}\s+(US-[0-9]+)\b`)

// StoryLabels lists the story labels a PRD defines, once each, in order.
// Only a story heading outside a fenced block defines one. A label in
// prose, a table or a citation names a story, often of another PRD, so it
// is no story of this one. prd submit records these labels, decompose and
// work create check a citation against them, and the citations gate
// counts them the same way.
func StoryLabels(text string) []string {
	var out []string
	for _, l := range lines(text) {
		if l.Fenced {
			continue
		}
		if m := storyHeading.FindStringSubmatch(l.Text); m != nil && !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}
