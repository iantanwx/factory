package gates

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/pack"
)

// The lessons gate (lessons.ts, alpha) holds a project's correction ledger
// to the loop's ladder. The ladder, its scopes and its channels ship in
// the pack, so a project cannot drift them. The ledger records each
// correction a person stated, the rung it reached and the files that hold
// that rung. Both are prose, and nothing else joins them, so a rung renamed
// in one is silently absent from the other, and a row can claim a
// mechanism that nobody wired.
//
// Nothing reads a conversation, so no gate fails when a correction
// reaches no row. The gate checks each row that exists. It never reads the
// Line column, a path on a pull request's branch that may never merge,
// and it never judges whether a rung is the right one.

// LedgerPath is where a project keeps its correction ledger.
const LedgerPath = "docs/solutions/correction-ledger.md"

// LadderSource is the ladder's path in the pack.
const LadderSource = "skills/loop-encode-lessons/SKILL.md"

// lessonChannels are the channels these rules read. The ladder must define
// the same set, or the ledger accepts a row that no rule checks.
var lessonChannels = []string{"chat", "review"}

// ladderRung is one rung of the ladder. Strength 1 is the strongest.
type ladderRung struct {
	strength  int
	mechanism string
}

type ladder struct {
	rungs            []ladderRung
	scopes, channels []string
}

func parseLadder(text string) ladder {
	var l ladder
	for _, row := range pack.Table(text, "strength", "mechanism", "where it lives in a project") {
		n, err := strconv.Atoi(pack.Bare(row.Cells[0]))
		if err != nil {
			n = -1
		}
		l.rungs = append(l.rungs, ladderRung{n, pack.Bare(row.Cells[1])})
	}
	for _, row := range pack.Table(text, "scope", "the correction binds", "what the reviser does") {
		l.scopes = append(l.scopes, pack.Bare(row.Cells[0]))
	}
	for _, row := range pack.Table(text, "channel", "where the person wrote it", "what locates the correction") {
		l.channels = append(l.channels, pack.Bare(row.Cells[0]))
	}
	return l
}

// ledgerRow is one correction, as the ledger records it.
type ledgerRow struct {
	line int
	// correction is the person's words, exact on every channel.
	correction, channel, date, pr string
	// at is the file and line a review comment marks. No rule opens the
	// path; a chat row must write -.
	at                  string
	lesson, scope, rung string
	// holds is each backticked name of the Where it lives cell: a file or
	// a gate, or the decision of a pending row.
	holds []string
}

type ledger struct {
	rows []ledgerRow
	// sections is each lesson slug that has a ### section.
	sections []string
}

var backticked = regexp.MustCompile("`([^`]+)`")

// namesIn reads each backticked name of a cell. A cell of - names nothing.
func namesIn(cell string) []string {
	var out []string
	for _, m := range backticked.FindAllStringSubmatch(cell, -1) {
		if n := strings.TrimSpace(m[1]); n != "" && n != "-" {
			out = append(out, n)
		}
	}
	return out
}

// slugOf reads the first backticked name of a heading's title.
func slugOf(title string) string {
	if m := backticked.FindStringSubmatch(title); m != nil {
		return m[1]
	}
	return ""
}

func parseLedger(text string) ledger {
	var l ledger
	for _, r := range pack.Table(text, "correction", "channel", "date", "pr", "line", "lesson", "scope", "rung", "where it lives") {
		c := r.Cells
		l.rows = append(l.rows, ledgerRow{
			line: r.Line, correction: pack.Bare(c[0]), channel: pack.Bare(c[1]), date: pack.Bare(c[2]),
			pr: pack.Bare(c[3]), at: pack.Bare(c[4]), lesson: pack.Bare(c[5]), scope: pack.Bare(c[6]),
			rung: pack.Bare(c[7]), holds: namesIn(c[8]),
		})
	}
	lines := strings.Split(text, "\n")
	for i, level := range pack.Headings(lines) {
		if level == 3 {
			if s := slugOf(lines[i][4:]); s != "" {
				l.sections = append(l.sections, s)
			}
		}
	}
	return l
}

// checkLadder checks the ladder alone: strengths run 1 to N with no gap,
// no mechanism twice, two scopes, and the channels these rules read.
func checkLadder(l ladder) []Finding {
	var out []Finding
	add := func(rule, format string, args ...any) {
		out = append(out, Finding{File: LadderSource, Rule: rule, Message: "the loop's ladder: " + fmt.Sprintf(format, args...)})
	}
	if len(l.rungs) == 0 {
		add("ladder", "defines no rung")
	}
	seen := map[string]bool{}
	for i, r := range l.rungs {
		if r.strength != i+1 {
			add("ordered", "row %d reads strength %d, and the ladder runs from 1 with no gap", i+1, r.strength)
		}
		if seen[r.mechanism] {
			add("duplicate_rung", "two rungs name %q", r.mechanism)
		}
		seen[r.mechanism] = true
	}
	if len(l.scopes) != 2 {
		add("scopes", "defines %d scopes, and a correction has two", len(l.scopes))
	}
	if got, want := slices.Sorted(slices.Values(l.channels)), slices.Sorted(slices.Values(lessonChannels)); !slices.Equal(got, want) {
		add("channels", "defines channels %v, and the lessons gate reads %v", got, want)
	}
	return out
}

// Lessons checks the project's correction ledger against the loop's
// ladder. A project with no ledger passes.
func Lessons(_ context.Context, r *Repo) ([]Finding, error) {
	raw, err := os.ReadFile(filepath.Join(r.Root, LedgerPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ladderText, err := fs.ReadFile(pack.Source(), LadderSource)
	if err != nil {
		return nil, err
	}
	return lessonFindings(r, string(raw), parseLadder(string(ladderText))), nil
}

// lessonFindings is every finding of one ledger text.
func lessonFindings(r *Repo, text string, l ladder) []Finding {
	led := parseLedger(text)
	out := checkLadder(l)
	out = append(out, checkLedger(r, text, l, led)...)
	return append(out, checkSummary(text, led, l)...)
}
