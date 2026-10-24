package bodygate

import (
	"fmt"
	"regexp"
	"strings"
)

// BoldPerBlock is the most bold spans one paragraph or list item may
// hold. One bold lead-in names the item. A second bold span in the same
// block is texture, and texture signals nothing. alpha measured 95 blocks
// in four pull request bodies a person read as well written, and none held
// two.
const BoldPerBlock = 1

var (
	listItem   = regexp.MustCompile(`^ {0,3}(?:[-*+]|\d+[.)])\s`)
	heading    = regexp.MustCompile(`^#{1,6}\s`)
	blockquote = regexp.MustCompile(`^\s*>`)
	codeSpan   = regexp.MustCompile("`[^`\n]+`")
)

type delimKind int

const (
	boldOpen delimKind = iota
	boldClose
	codeEnd
)

type delim struct {
	kind delimKind
	at   int
}

// delimiters returns the ** delimiters of one line, each an open or a
// close by position, and the end of each code span. A code span hides what
// it holds, so a glob such as packages/db/** in one is no delimiter.
func delimiters(raw string) []delim {
	var found []delim
	opens := true
	for i := 0; i < len(raw); {
		if raw[i] == '`' {
			if end := strings.IndexByte(raw[i+1:], '`'); end != -1 {
				i += end + 2
				found = append(found, delim{codeEnd, i})
				continue
			}
		}
		if strings.HasPrefix(raw[i:], "**") {
			kind := boldClose
			if opens {
				kind = boldOpen
			}
			found = append(found, delim{kind, i})
			opens = !opens
			i += 2
			continue
		}
		i++
	}
	return found
}

// boldSpans counts the bold spans of one line, after each code span is
// hidden.
func boldSpans(raw string) int {
	return strings.Count(codeSpan.ReplaceAllString(raw, " CODE "), "**") / 2
}

// excerpt is the first 90 characters of a line, so a refusal names the
// fault without printing the body again.
func excerpt(raw string) string {
	flat := strings.Join(strings.Fields(raw), " ")
	if r := []rune(flat); len(r) > 90 {
		return string(r[:90]) + "..."
	}
	return flat
}

// MarkupFaults returns every markup fault of one body, in line order.
//
//   - bold-after-code: an opening ** whose previous character closes a
//     code span. The author closed a bold run before a code span and
//     opened a second run after it. A closing ** there is the ordinary
//     idiom and stays legal.
//   - bold-texture: a paragraph or a list item with more than
//     BoldPerBlock bold spans. A table row and a blockquote are outside
//     the count.
func MarkupFaults(body string) []Fault {
	var faults []Fault
	lines := strings.Split(body, "\n")
	inFence := false
	// A block is the run of lines a reader takes as one paragraph or one
	// list item. It ends at a blank line, a heading, a fence, a
	// blockquote, a table row, or the start of the next item.
	start, bold := 0, 0
	closeBlock := func() {
		if start != 0 && bold > BoldPerBlock {
			faults = append(faults, Fault{Line: start, Rule: "bold-texture",
				Text: fmt.Sprintf("%d bold spans in one block: %s", bold, excerpt(lines[start-1]))})
		}
		start, bold = 0, 0
	}
	for i, raw := range lines {
		n := i + 1
		if fence.MatchString(raw) {
			inFence = !inFence
			closeBlock()
			continue
		}
		if inFence {
			continue
		}
		ds := delimiters(raw)
		for j, d := range ds {
			if j > 0 && d.kind == boldOpen && ds[j-1].kind == codeEnd && ds[j-1].at == d.at {
				faults = append(faults, Fault{Line: n, Rule: "bold-after-code", Text: excerpt(raw)})
			}
		}
		blank := strings.TrimSpace(raw) == ""
		outside := blank || heading.MatchString(raw) || blockquote.MatchString(raw) || tableRow.MatchString(raw)
		if outside || listItem.MatchString(raw) {
			closeBlock()
		}
		if outside {
			continue
		}
		if start == 0 {
			start = n
		}
		bold += boldSpans(raw)
	}
	closeBlock()
	return faults
}

// markupRefusal returns why a body's markup must not be written, or "".
func markupRefusal(body string) string {
	faults := MarkupFaults(body)
	if len(faults) == 0 {
		return ""
	}
	lines := []string{"This body has markup faults, and nothing was written.", ""}
	kinds := map[string]bool{}
	for _, f := range faults {
		lines = append(lines, fmt.Sprintf("  OVER  line %d  %s: %s", f.Line, f.Rule, f.Text))
		kinds[f.Rule] = true
	}
	lines = append(lines, "")
	if kinds["bold-after-code"] {
		lines = append(lines, "  bold-after-code: a code span reopens a bold run. Put the code span inside one bold run, "+
			"or end the bold run and write plain text.")
	}
	if kinds["bold-texture"] {
		lines = append(lines, fmt.Sprintf("  bold-texture: one block holds more than %d bold span. Bold the lead-in that names the item, "+
			"and write the rest plain. A table row and a blockquote are outside this count.", BoldPerBlock))
	}
	return strings.Join(lines, "\n")
}
