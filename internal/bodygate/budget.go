package bodygate

import (
	"fmt"
	"strings"
)

// PRBodyWords is the most prose words a pull request body may hold, less
// every fenced block and every table row. alpha derived 700 twice and the
// two agree: the sections a runbook asks the body to hold come to about
// 450 words, and the shortest sufficient body of four measured on
// 2026-10-01 held 456. 1.6 times either is above 700.
const PRBodyWords = 700

// TableRowWords is the most words one table row may hold, so a paragraph
// cannot hide in a table and escape PRBodyWords. The longest row alpha
// measured held 46 words.
const TableRowWords = 60

// Row is one table row of a body and its word count.
type Row struct {
	Line  int `json:"line"`
	Words int `json:"words"`
}

// Parts splits a body the way the budget counts it: the prose words, and
// each table row. A fenced block counts nowhere, because it holds the
// output of a command the runbook orders the author to record.
func Parts(body string) (prose int, rows []Row) {
	inFence := false
	for i, raw := range strings.Split(body, "\n") {
		switch {
		case fence.MatchString(raw):
			inFence = !inFence
		case inFence:
		case tableRow.MatchString(raw):
			// The cell delimiter is no word.
			rows = append(rows, Row{Line: i + 1, Words: countWords(strings.ReplaceAll(raw, "|", " "))})
		default:
			prose += countWords(raw)
		}
	}
	return prose, rows
}

// PRBudget returns why a pull request body is over budget, or "" when it
// is not. It names every fault, each with its budget, its count and what
// to cut.
func PRBudget(body string) string {
	prose, rows := Parts(body)
	var over, cut []string
	if prose > PRBodyWords {
		over = append(over, fmt.Sprintf("whole body, less fenced blocks and table rows  budget %d words, actual %d", PRBodyWords, prose))
		cut = append(cut, fmt.Sprintf("Cut the prose to %d words. Keep every file path, line number, command, count and citation, "+
			"because those are the evidence. Delete each sentence that explains a decision the reviewer does not review, "+
			"and each sentence the diff states.", PRBodyWords))
	}
	for _, r := range rows {
		if r.Words > TableRowWords {
			over = append(over, fmt.Sprintf("table row at line %d  budget %d words, actual %d", r.Line, TableRowWords, r.Words))
			cut = append(cut, "A table row holds a label, a path and a result. This row holds a paragraph. "+
				"Move the paragraph into the prose, where the whole-body budget counts it.")
		}
	}
	if len(over) == 0 {
		return ""
	}
	lines := []string{"This pull request body is over budget, and GitHub holds nothing.", ""}
	for _, o := range over {
		lines = append(lines, "  OVER  "+o)
	}
	lines = append(lines, "")
	for _, c := range cut {
		lines = append(lines, "  "+c)
	}
	return strings.Join(append(lines, "", "A fenced block is outside this count. Put the output of a command in one."), "\n")
}
