// Package bodygate checks a body that a run writes and a person reads,
// before the server sends it to GitHub. It ports the write-point body gate
// of alpha's SDLC (scripts/sdlc/body-gate.ts, pr-budget.ts and
// body-markup.ts). The budget and the markup rules are pure, so a test
// proves each rule from a literal. The prose rules run the two linters
// the release embeds.
//
// Three faults, each with its own repair:
//
//   - length: the word budget of the caller (PRBudget for a pull request)
//   - markup: a code span that reopens a bold run, and bold as texture
//   - prose: the linter rules an author can always satisfy
//
// A rule refuses only when an author can always satisfy it. alpha's gate
// also prints a warning for each ste-lint rule that needs judgement, such
// as a long sentence. An envelope has no warning channel, so this package
// ports the refusals only.
package bodygate

import (
	"context"
	"regexp"
	"strings"
)

// Fault is one thing an author must repair, with the line that holds it.
type Fault struct {
	Line int    `json:"line"`
	Rule string `json:"rule"`
	Text string `json:"text"`
}

// fence opens or closes a fenced block.
var fence = regexp.MustCompile("^\\s*(?:```|~~~)")

// tableRow is a Markdown table row.
var tableRow = regexp.MustCompile(`^\s*\|`)

// countWords matches wc -w, so an author can reproduce each count.
func countWords(s string) int { return len(strings.Fields(s)) }

// Check is every check over one body: the caller's word budget, the markup
// rules, then the prose rules. It returns why the body must not be written,
// or "" when it may. Each fault reaches the caller at once, because a gate
// that reported one fault at a time costs one round trip for each one. An
// error means a linter could not run, and the body was not checked.
//
// The caller passes its budget, because a ticket and a pull request hold
// different sections and different numbers.
func Check(ctx context.Context, body string, budget func(string) string) (string, error) {
	faults, err := ProseFaults(ctx, body)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, p := range []string{budget(body), markupRefusal(body), proseRefusal(faults)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// CheckPR is Check with the pull request budget.
func CheckPR(ctx context.Context, body string) (string, error) { return Check(ctx, body, PRBudget) }
