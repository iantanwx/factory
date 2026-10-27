// Package shape reads Markdown text and returns its faults: a spec's open
// questions, and a ticket or bug body against its shape. It does no I/O, and
// it imports no package of the loop, so every package that writes such text
// can call it.
package shape

import (
	"regexp"
	"strings"
)

// line is one line of a document.
type line struct {
	N      int    // the line's number in the document, from 1
	Text   string // the line, with no line break
	Fenced bool   // the line is a fence marker, or inside a fenced block
}

// section is one "## " heading of a document and the lines under it. The text
// before the first heading is a section with no heading. No line of a fenced
// block starts a section.
type section struct {
	Heading string // the heading's text, trimmed
	Line    int    // the heading's line number, or 0 for the text before it
	Lines   []line
}

// sections splits text at its "## " headings.
func sections(text string) []section {
	out := []section{{}}
	fence := "" // the open block's marker, or "" outside a block
	for i, raw := range strings.Split(text, "\n") {
		l := line{N: i + 1, Text: strings.TrimSuffix(raw, "\r")}
		t := strings.TrimSpace(l.Text)
		switch {
		case fence != "":
			l.Fenced = true
			if len(t) >= len(fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
		case fenceOpen.MatchString(t):
			l.Fenced = true
			fence = fenceOpen.FindString(t)
		case strings.HasPrefix(l.Text, "## "):
			out = append(out, section{Heading: strings.TrimSpace(l.Text[3:]), Line: l.N})
			continue
		}
		out[len(out)-1].Lines = append(out[len(out)-1].Lines, l)
	}
	return out
}

var (
	fenceOpen = regexp.MustCompile("^(```+|~~~+)")
	// listItem is a top-level list marker and the space after it.
	listItem = regexp.MustCompile(`^([-*+]|[0-9]+[.)])(\s+|$)`)
	heading  = regexp.MustCompile(`^#{1,6}(\s|$)`)
)

// entries returns the top-level list items of a section. An item holds each
// line under it up to the next top-level item or heading, so a nested item
// belongs to its entry. A list item inside a fenced block is no entry.
func entries(s section) [][]line {
	var out [][]line
	in := false
	for _, l := range s.Lines {
		switch {
		case l.Fenced:
		case listItem.MatchString(l.Text):
			out = append(out, nil)
			in = true
		case heading.MatchString(l.Text):
			in = false
		}
		if in {
			out[len(out)-1] = append(out[len(out)-1], l)
		}
	}
	return out
}

// countWords counts the words of lines.
func countWords(lines []line) int {
	n := 0
	for _, l := range lines {
		n += len(strings.Fields(l.Text))
	}
	return n
}
