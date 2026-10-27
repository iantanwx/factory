package shape

import (
	"regexp"
	"strings"
)

// Question is one top-level list entry under a spec's "## Open questions".
// Decided is true exactly when the entry, nested lines included, holds a bold
// run that starts "**Answered" or "**Deferred". Fenced lines and code spans
// are not read.
type Question struct {
	Line    int    // the entry's first line in the document
	Text    string // the entry's first line, less its list marker, cut to 80 characters
	Decided bool
}

var (
	decision = regexp.MustCompile(`(?s)\*\*(Answered|Deferred)\b.*?\*\*`)
	codeSpan = regexp.MustCompile("`[^`]*`")
)

// Undecided returns each open question of spec that holds no decision.
func Undecided(spec string) []Question {
	var out []Question
	for _, s := range sections(spec) {
		if !strings.EqualFold(s.Heading, "Open questions") {
			continue
		}
		for _, e := range entries(s) {
			if q := question(e); !q.Decided {
				out = append(out, q)
			}
		}
	}
	return out
}

func question(e []line) Question {
	var read []string
	for _, l := range e {
		if !l.Fenced {
			read = append(read, codeSpan.ReplaceAllString(l.Text, ""))
		}
	}
	text := []rune(strings.TrimSpace(listItem.ReplaceAllString(e[0].Text, "")))
	if len(text) > 80 {
		text = text[:80]
	}
	return Question{
		Line:    e[0].N,
		Text:    string(text),
		Decided: decision.MatchString(strings.Join(read, "\n")),
	}
}
