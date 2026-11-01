package shape

import (
	"regexp"
	"slices"
	"strings"
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

// UIMark is what the UI line of one story says: whether the story touches
// the UI of a project that declares one.
type UIMark int

const (
	UIMissing UIMark = iota // the story holds no UI line
	UIYes                   // **UI:** yes
	UINo                    // **UI:** no
	UIBad                   // two UI lines, or a value other than yes and no
)

// uiLine is the UI line of a story, `**UI:** yes`, at the start of a line.
var uiLine = regexp.MustCompile(`^\*\*UI:\*\*(.*)$`)

// Story is one story heading of a PRD and its UI line.
type Story struct {
	Label string
	Line  int
	UI    UIMark
}

// Stories lists each story heading of a PRD outside a fenced block, in
// order, with the UI line under it. A story's lines end at the next
// heading.
func Stories(prd string) []Story {
	var out []Story
	seen := 0 // the UI lines of the last story
	for _, l := range lines(prd) {
		if l.Fenced {
			continue
		}
		if m := storyHeading.FindStringSubmatch(l.Text); m != nil {
			out = append(out, Story{Label: m[1], Line: l.N})
			seen = 0
			continue
		}
		if heading.MatchString(l.Text) {
			seen = -1
			continue
		}
		m := uiLine.FindStringSubmatch(l.Text)
		if m == nil || seen < 0 || len(out) == 0 {
			continue
		}
		s := &out[len(out)-1]
		seen++
		switch v := strings.TrimSpace(m[1]); {
		case seen > 1:
			s.UI = UIBad
		case v == "yes":
			s.UI = UIYes
		case v == "no":
			s.UI = UINo
		default:
			s.UI = UIBad
		}
	}
	return out
}

// UIFault says what is wrong with the UI line of the story, or "" when it
// reads yes or no. The prd gate asks for the line in a project that
// declares a UI.
func (s Story) UIFault() string {
	switch s.UI {
	case UIMissing:
		return "holds no **UI:** line"
	case UIBad:
		return "holds two **UI:** lines, or one that reads neither yes nor no"
	}
	return ""
}

// UIStories lists the labels of the stories whose UI line reads yes, once
// each, in order.
func UIStories(prd string) []string {
	var out []string
	for _, s := range Stories(prd) {
		if s.UI == UIYes && !slices.Contains(out, s.Label) {
			out = append(out, s.Label)
		}
	}
	return out
}
