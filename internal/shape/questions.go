package shape

import (
	"regexp"
	"strings"
)

// NeedsADR starts an open question about a decision that passes the ADR
// test. Only an answer that cites an ADR decides it.
const NeedsADR = "**Needs an ADR:**"

// Question is one top-level list entry under a spec's "## Open questions".
// Decided is true exactly when the entry, nested lines included, holds a bold
// run that starts "**Answered" or "**Deferred". An entry that starts with
// NeedsADR is decided only by an "**Answered" run whose text, up to the next
// decision run, cites "ADR NNNN". Fenced lines and code spans are not read.
type Question struct {
	Line     int    // the entry's first line in the document
	Text     string // the entry's first line, less its list marker, cut to 80 characters
	Decided  bool
	NeedsADR bool
}

var (
	decision = regexp.MustCompile(`(?s)\*\*(Answered|Deferred)\b.*?\*\*`)
	codeSpan = regexp.MustCompile("`[^`]*`")
	adrCited = regexp.MustCompile(`\bADR \d{4}\b`)
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
	body := strings.Join(read, "\n")
	text := []rune(strings.TrimSpace(listItem.ReplaceAllString(e[0].Text, "")))
	q := Question{Line: e[0].N, NeedsADR: strings.HasPrefix(string(text), NeedsADR)}
	if len(text) > 80 {
		text = text[:80]
	}
	q.Text = string(text)
	if q.NeedsADR {
		q.Decided = answeredByADR(body)
	} else {
		q.Decided = decision.MatchString(body)
	}
	return q
}

// answeredByADR reports whether an "**Answered" run in body cites an ADR
// before the next decision run starts.
func answeredByADR(body string) bool {
	runs := decision.FindAllStringIndex(body, -1)
	for i, r := range runs {
		end := len(body)
		if i+1 < len(runs) {
			end = runs[i+1][0]
		}
		if strings.HasPrefix(body[r[0]:], "**Answered") && adrCited.MatchString(body[r[0]:end]) {
			return true
		}
	}
	return false
}
