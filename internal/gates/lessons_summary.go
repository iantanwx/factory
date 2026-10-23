package gates

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// The ledger describes itself in one marked block that its own rows
// derive (lessons-summary.ts, alpha). Prose that counted the rows by hand
// went stale while every gate passed. A count is a rule about what the
// table holds, so it lives in one place: the derivation. The gate compares
// the committed block with a fresh one, and its finding prints the block
// to paste.

// The markers the block sits between.
const (
	SummaryOpen  = "<!-- lessons:summary: the lessons gate derives these lines -->"
	SummaryClose = "<!-- /lessons:summary -->"
)

// rank sorts a rung by its number, and a named key after every number.
func rank(key string) int {
	if n, err := strconv.Atoi(key); err == nil {
		return n
	}
	return math.MaxInt
}

// tally counts the rows by one cell, sorted. An empty cell counts nowhere.
func tally(rows []ledgerRow, of func(ledgerRow) string) [][2]string {
	counts := map[string]int{}
	for _, r := range rows {
		if k := of(r); k != "" {
			counts[k]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, [2]string{k, strconv.Itoa(counts[k])})
	}
	return out
}

// render writes counts as "chat × 7, review × 9", or none.
func render(counts [][2]string) string {
	if len(counts) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		parts = append(parts, c[0]+" × "+c[1])
	}
	return strings.Join(parts, ", ")
}

// unreached lists the ladder's rungs that no row has reached.
func unreached(led ledger, l ladder) []int {
	held := map[string]bool{}
	for _, r := range led.rows {
		held[r.rung] = true
	}
	var out []int
	for _, x := range l.rungs {
		if !held[strconv.Itoa(x.strength)] {
			out = append(out, x.strength)
		}
	}
	return out
}

// summarise is each fact about the ledger that its own table answers.
func summarise(led ledger, l ladder) []string {
	prs, lessons := map[string]bool{}, map[string]bool{}
	for _, r := range led.rows {
		if r.pr != "" && r.pr != "-" {
			prs[r.pr] = true
		}
		lessons[r.lesson] = true
	}
	channels := tally(led.rows, func(r ledgerRow) string { return r.channel })
	missing := "none"
	if u := unreached(led, l); len(u) > 0 {
		parts := make([]string, 0, len(u))
		for _, n := range u {
			parts = append(parts, strconv.Itoa(n))
		}
		missing = strings.Join(parts, ", ")
	}
	return []string{
		fmt.Sprintf("**%d corrections, %d lessons, %d channels, %d pull requests.**", len(led.rows), len(lessons), len(channels), len(prs)),
		"",
		"- By channel: " + render(channels),
		"- By scope: " + render(tally(led.rows, func(r ledgerRow) string { return r.scope })),
		"- By rung: " + render(tally(led.rows, func(r ledgerRow) string {
			if r.rung == noRung {
				return "no rung"
			}
			return r.rung
		})),
		"- Rungs of the ladder no row has reached: " + missing,
		// A document row needs no section, so the two numbers differ.
		fmt.Sprintf("- Lessons with a section of their own: %d of %d", len(led.sections), len(lessons)),
	}
}

// summaryBlock is the whole block, markers included.
func summaryBlock(led ledger, l ladder) string {
	lines := append([]string{SummaryOpen, ""}, summarise(led, l)...)
	return strings.Join(append(lines, "", SummaryClose), "\n")
}

// committedSummary returns the block between the markers and its line, or
// false when a marker is gone.
func committedSummary(text string) (string, int, bool) {
	open := strings.Index(text, SummaryOpen)
	if open < 0 {
		return "", 0, false
	}
	end := strings.Index(text[open:], SummaryClose)
	if end < 0 {
		return "", 0, false
	}
	return text[open : open+end+len(SummaryClose)], strings.Count(text[:open], "\n") + 1, true
}

// checkSummary reports a block the rows no longer derive, and a ledger
// with no block. The two read apart, because their repairs differ.
func checkSummary(text string, led ledger, l ladder) []Finding {
	fresh := summaryBlock(led, l)
	held, line, ok := committedSummary(text)
	if !ok {
		return []Finding{{File: LedgerPath, Rule: "summary",
			Message: "the ledger holds no summary block. Add it below the table:\n" + fresh}}
	}
	if held == fresh {
		return nil
	}
	return []Finding{{File: LedgerPath, Line: line, Rule: "summary",
		Message: "the summary block is not what this ledger's rows derive. Replace it with:\n" + fresh}}
}
