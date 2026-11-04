package shape

import (
	"fmt"
	"regexp"
	"strings"
)

// Owed is one line that a code verdict's checklist owes.
type Owed struct {
	ID   string `json:"id"`             // a contract id, D<n> or stop<n>
	Text string `json:"text,omitempty"` // the Done means line or the stop condition; "" for a contract
}

// The results of a checklist line.
const (
	Pass   = "pass"
	Fail   = "fail"
	Waived = "waived"
)

// Check is one line of a verdict's checklist.
type Check struct {
	ID       string
	Result   string // Pass, Fail or Waived
	Evidence string // the text after the result; "" when the line holds none
}

var (
	doneMeans      = Need{Name: "Done means"}
	stopConditions = Need{Name: "Stop conditions"}
	// checkLine is one checklist line: an optional list marker, the id, the
	// result and the evidence, such as "- C1 pass: the test fails at the base."
	checkLine = regexp.MustCompile("^\\s*(?:(?:[-*+]|[0-9]+[.)])\\s+)?`?(C-bug-[0-9]+|C[0-9]+|D[0-9]+|stop[0-9]+|X[0-9]+)`?:?\\s+" +
		"(?i:(pass|fail|waived))\\b[\\s:.,;-]*(.*)$")
)

// Checklist returns each line that a code verdict on body owes, in order:
// each contract id, D<n> for each line of Done means, and stop<n> for each
// stop condition. A line is a top-level list item. A section with text and
// no list item is one line.
func Checklist(s Shape, body string) []Owed {
	var out []Owed
	for _, id := range ContractIDs(s, body) {
		out = append(out, Owed{ID: id})
	}
	secs := sections(body)
	for _, part := range []struct {
		need   Need
		prefix string
	}{{doneMeans, "D"}, {stopConditions, "stop"}} {
		for i, text := range itemTexts(matching(secs, part.need)) {
			out = append(out, Owed{ID: fmt.Sprintf("%s%d", part.prefix, i+1), Text: text})
		}
	}
	return out
}

// itemTexts returns the text of each top-level list item of secs on one
// line, with no list marker.
func itemTexts(secs []section) []string {
	var out []string
	for _, sec := range secs {
		items := entries(sec)
		if len(items) == 0 {
			items = [][]line{sec.Lines}
		}
		for _, item := range items {
			var words []string
			for _, l := range item {
				words = append(words, strings.Fields(l.Text)...)
			}
			if text := listItem.ReplaceAllString(strings.Join(words, " "), ""); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

// Checks returns each checklist line of review, in order. A line inside a
// fenced block is no checklist line.
func Checks(review string) []Check {
	var out []Check
	for _, l := range lines(review) {
		if l.Fenced {
			continue
		}
		if m := checkLine.FindStringSubmatch(l.Text); m != nil {
			out = append(out, Check{ID: m[1], Result: strings.ToLower(m[2]), Evidence: strings.TrimSpace(m[3])})
		}
	}
	return out
}
