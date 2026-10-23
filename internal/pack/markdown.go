package pack

import (
	"regexp"
	"strings"
)

// The checks that read a table out of a prose document share these
// readers (md-table.ts, alpha). The document stays the one source: a
// person reads the table, and a check reads the same cells.

// Cells returns the trimmed cells of one table row, or false when the
// line is no row.
func Cells(line string) ([]string, bool) {
	t := strings.TrimSpace(line)
	if len(t) < 2 || !strings.HasPrefix(t, "|") || !strings.HasSuffix(t, "|") {
		return nil, false
	}
	parts := strings.Split(t[1:len(t)-1], "|")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts, true
}

// Bare strips the backticks a cell wraps a name in.
func Bare(cell string) string { return strings.TrimSpace(strings.ReplaceAll(cell, "`", "")) }

// TableRow is one body row of a table and its line, from 1.
type TableRow struct {
	Line  int
	Cells []string
}

// Table returns the body rows of the first table whose header reads
// exactly header, lower case and bare. A whole-header match keeps two
// tables that share column names apart.
func Table(text string, header ...string) []TableRow {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		head, ok := Cells(l)
		if !ok || len(head) != len(header) {
			continue
		}
		match := true
		for k, c := range head {
			if strings.ToLower(Bare(c)) != header[k] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		var rows []TableRow
		for j := i + 2; j < len(lines); j++ {
			row, ok := Cells(lines[j])
			if !ok || len(row) != len(head) {
				break
			}
			rows = append(rows, TableRow{Line: j + 1, Cells: row})
		}
		return rows
	}
	return nil
}

var heading = regexp.MustCompile(`^(#{1,6}) `)

// HeadingLevel returns the level of a Markdown heading, or 0.
func HeadingLevel(line string) int {
	if m := heading.FindStringSubmatch(line); m != nil {
		return len(m[1])
	}
	return 0
}

// Headings returns the level of each line that is a heading, and 0 for
// every other line. A line in a fenced block is no heading, so a shell
// comment cannot end a section.
func Headings(lines []string) []int {
	out := make([]int, len(lines))
	in := false
	for i, l := range lines {
		if fence.MatchString(l) {
			in = !in
			continue
		}
		if !in {
			out[i] = HeadingLevel(l)
		}
	}
	return out
}

// Section returns the text under the first heading of the level that
// match accepts, up to the next heading of that level or higher. A
// deeper subheading stays inside it.
func Section(text string, level int, match func(title string) bool) string {
	lines := strings.Split(text, "\n")
	levels := Headings(lines)
	for i, l := range lines {
		if levels[i] != level || !match(l[level+1:]) {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if levels[j] > 0 && levels[j] <= level {
				end = j
				break
			}
		}
		return strings.TrimSpace(strings.Join(lines[i+1:end], "\n"))
	}
	return ""
}
