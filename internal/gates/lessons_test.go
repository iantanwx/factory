package gates

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

// The tests build their own ladder, ledger and checkout, so an edit to a
// real document cannot turn one green (lessons.test.ts,
// lessons-channel.test.ts and lessons-summary.test.ts, alpha).

const ladderRungs = "| Strength | Mechanism | Where it lives in a project |\n| --- | --- | --- |\n" +
	"| 1 | A type that cannot hold the bad value | The code itself |\n" +
	"| 2 | A gate that fails CI | A gate |"

const ladderRest = "| Scope | The correction binds | What the amender does |\n| --- | --- | --- |\n" +
	"| `repository` | Every later document | Names its lesson |\n" +
	"| `document` | One paragraph | Reaches no rung |\n\n" +
	"| Channel | Where the person wrote it | What locates the correction |\n| --- | --- | --- |\n" +
	"| `review` | A pull request | The pull request and the line |\n" +
	"| `chat` | A conversation | The date and the words |"

func testLadder(rungs string) ladder { return parseLadder(rungs + "\n\n" + ladderRest) }

// spec is one ledger row. An empty field takes the default: a review row
// at rung 2 that names the lint gate.
type spec struct {
	correction, channel, date, pr, line, lesson, scope, rung, holds string
}

func or(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

const restSection = "### `rest`\n\n**Stronger rungs.** Rung 1 fits nothing.\n"

func ledgerText(rows []spec, sections string) string {
	var b strings.Builder
	b.WriteString("| Correction | Channel | Date | PR | Line | Lesson | Scope | Rung | Where it lives |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s | `%s` | `%s` | %s | %s |\n",
			or(r.correction, "a correction"), or(r.channel, "review"), or(r.date, "2026-09-30"), or(r.pr, "#1"),
			or(r.line, "`docs/x.md:1`"), or(r.lesson, "rest"), or(r.scope, "repository"), or(r.rung, "2"), or(r.holds, "`lint`"))
	}
	return b.String() + "\n" + sections
}

// lessonRepo is a checkout with a config that names a gate of its own,
// and two files.
func lessonRepo(t *testing.T) *Repo {
	return testRepo(t, map[string]string{
		".loop/config.yaml": "merge:\n  mechanism: direct\ngates:\n  - name: lint\n    run: \"true\"\n  - name: rest-paths\n    run: \"true\"\n",
		"scripts/check.sh":  "#!/bin/sh\n",
		"AGENTS.md":         "x\n",
	})
}

// ledgerRules runs the ledger rules alone, with no summary block.
func ledgerRules(t *testing.T, rows []spec, sections string, l ladder) []string {
	t.Helper()
	text := ledgerText(rows, sections)
	return rules(checkLedger(lessonRepo(t), text, l, parseLedger(text)))
}

func TestShippedLadderPasses(t *testing.T) {
	raw, err := fs.ReadFile(pack.Source(), LadderSource)
	if err != nil {
		t.Fatal(err)
	}
	l := parseLadder(string(raw))
	if len(l.rungs) != 7 || !slices.Equal(l.scopes, []string{"repository", "document"}) || len(l.channels) != 2 {
		t.Fatalf("the shipped ladder reads %d rungs, scopes %v, channels %v", len(l.rungs), l.scopes, l.channels)
	}
	if got := checkLadder(l); len(got) != 0 {
		t.Fatalf("the shipped ladder fails: %v", got)
	}
	if !slices.ContainsFunc(l.rungs, func(r ladderRung) bool { return r.mechanism == scriptMechanism }) {
		t.Fatalf("the shipped ladder names no rung %q, so the wired rule reaches no row", scriptMechanism)
	}
}

func TestLadderFaults(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"a gap in the strengths", strings.Replace(ladderRungs, "| 2 |", "| 3 |", 1) + "\n\n" + ladderRest, []string{"ordered"}},
		{"one mechanism twice", strings.Replace(ladderRungs, "A gate that fails CI", "A type that cannot hold the bad value", 1) + "\n\n" + ladderRest, []string{"duplicate_rung"}},
		{"nothing", "nothing here", []string{"ladder", "scopes", "channels"}},
		{"a channel the rules cannot read", ladderRungs + "\n\n" + ladderRest + "\n| `slack` | A channel | The date |", []string{"channels"}},
	} {
		if got := rules(checkLadder(parseLadder(tc.text))); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := checkLadder(testLadder(ladderRungs)); len(got) != 0 {
		t.Fatalf("the two-rung ladder fails: %v", got)
	}
}

func TestParsers(t *testing.T) {
	if got := namesIn("`a.md`, `b.go`"); !slices.Equal(got, []string{"a.md", "b.go"}) {
		t.Errorf("namesIn = %v", got)
	}
	if got := namesIn("-"); got != nil {
		t.Errorf("namesIn(-) = %v", got)
	}
	if got := namesIn("`-`"); got != nil {
		t.Errorf("namesIn(`-`) = %v", got)
	}
	led := parseLedger("## `two`\n### `three`\n#### `four`\n```\n### `fenced`\n```\n")
	if !slices.Equal(led.sections, []string{"three"}) {
		t.Errorf("sections = %v, want only the third-level heading outside a fence", led.sections)
	}
}

func TestLedgerRows(t *testing.T) {
	l := testLadder(ladderRungs)
	trig := "### `rest`\n\n**Trigger.** The next spec with an HTTP surface.\n"
	for _, tc := range []struct {
		name     string
		rows     []spec
		sections string
		want     []string
	}{
		{"a finished row that names a gate of the config", []spec{{}}, restSection, nil},
		{"a row that names a gate of the loop", []spec{{holds: "`secrets`"}}, restSection, nil},
		{"a row that names a file in the checkout", []spec{{holds: "`scripts/check.sh`, `AGENTS.md`"}}, restSection, nil},
		{"a scope the ladder does not define", []spec{{scope: "global", rung: "9"}}, restSection, []string{"scope"}},
		{"a rung the ladder does not hold", []spec{{rung: "9"}}, restSection, []string{"rung"}},
		{"a document row that records a rung", []spec{{scope: "document", rung: "2"}}, restSection, []string{"rung"}},
		{"a document row that names a mechanism", []spec{{scope: "document", rung: "-"}}, restSection, []string{"home"}},
		{"a document row that names nothing needs no section", []spec{{scope: "document", rung: "-", holds: "-"}}, "", nil},
		{"a numbered row that names nothing", []spec{{holds: "-"}}, restSection, []string{"home"}},
		{"a numbered row that names a missing file", []spec{{holds: "`scripts/gone.sh`"}}, restSection, []string{"home"}},
		{"a numbered row that names a path outside the checkout", []spec{{holds: "`../../etc/passwd`"}}, restSection, []string{"home"}},
		{"a numbered row that names a gate nothing runs", []spec{{holds: "`stray`"}}, restSection, []string{"wired"}},
		{"a pending row that names a decision", []spec{{rung: "pending", holds: "`proj-a3f`"}}, trig, nil},
		{"a pending row that names a child decision", []spec{{rung: "pending", holds: "`proj-a3f.1`"}}, trig, nil},
		{"a pending row that names a gate", []spec{{rung: "pending"}}, trig, []string{"home"}},
		{"a pending row that names nothing", []spec{{rung: "pending", holds: "-"}}, trig, []string{"home"}},
		{"a pending row that names a branch", []spec{{rung: "pending", holds: "`someone/de-slop`"}}, trig, []string{"home"}},
		{"a pending row that names a file", []spec{{rung: "pending", holds: "`scripts/check.sh`"}}, trig, []string{"home"}},
		{"a pending row that names two decisions", []spec{{rung: "pending", holds: "`proj-a3f`, `proj-b4c`"}}, trig, []string{"home"}},
		{"a pending lesson that states no trigger", []spec{{rung: "pending", holds: "`proj-a3f`"}}, restSection, []string{"trigger"}},
		{"a lesson with no section", []spec{{}}, "nothing here", []string{"lesson"}},
		{"a numbered lesson that refuses no stronger rung", []spec{{}}, "### `rest`\n\nIt was easy.\n", []string{"stronger"}},
		{"a rung 1 lesson refuses nothing", []spec{{rung: "1", holds: "`AGENTS.md`"}}, "### `rest`\n\nIt was easy.\n", nil},
		{"two rows of one lesson that disagree", []spec{{correction: "a"}, {correction: "b", rung: "1", holds: "`AGENTS.md`"}}, restSection, []string{"agree"}},
		{"two rows of one lesson that agree", []spec{{correction: "a"}, {correction: "b"}}, restSection, nil},
		{"one correction in two rows", []spec{{}, {}}, restSection, []string{"twice"}},
		{"a section that no row names", []spec{{}}, restSection + "\n### `stale`\n\nGone.\n", []string{"orphan"}},
		{"a section whose slug only starts with the lesson", []spec{{}}, "### `rest-verbs`\n\n**Stronger rungs.** Rung 1 fits nothing.\n", []string{"lesson", "orphan"}},
	} {
		if got := ledgerRules(t, tc.rows, tc.sections, l); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// reachRepo is a checkout whose gates reach their check scripts five
// ways: a path in the command, a path in a file the command reaches, make,
// which reaches the Makefile, a package manager, which reaches
// package.json, and a Go package pattern, which reaches its Go files.
func reachRepo(t *testing.T) *Repo {
	return testRepo(t, map[string]string{
		".loop/config.yaml": "merge:\n  mechanism: direct\ngates:\n" +
			"  - name: lint\n    run: make lint\n" +
			"  - name: rest-paths\n    run: sh scripts/check.sh --strict\n" +
			"  - name: test\n    run: go run ./tools/probe\n" +
			"  - name: lessons\n    run: pnpm lessons\n" +
			"  - name: unit\n    run: go test ./internal/...\n",
		"Makefile":                   "lint:\n\t./scripts/lint-paths.sh\n",
		"package.json":               `{"scripts":{"lessons":"node scripts/sdlc/lessons.ts"}}`,
		"scripts/lint-paths.sh":      "#!/bin/sh\n. \"$(dirname \"$0\")/lib.sh\"\n",
		"scripts/lib.sh":             "x=1\n",
		"scripts/check.sh":           "#!/bin/sh\n",
		"scripts/stray.sh":           "#!/bin/sh\n",
		"scripts/ghost.sh":           "#!/bin/sh\n",
		"scripts/sdlc/lessons.ts":    "export {};\n",
		"scripts/unit.sh":            "#!/bin/sh\n",
		"tools/probe/main.go":        "package main\n",
		"internal/x/check_test.go":   "package x\n\nvar cmd = exec.Command(\"sh\", \"../../scripts/unit.sh\")\n",
		"internal/x/testdata/run.go": "package main\n\nvar cmd = exec.Command(\"sh\", \"scripts/ghost.sh\")\n",
		"internal/x/_skip/run.go":    "package main\n\nvar cmd = exec.Command(\"sh\", \"scripts/ghost.sh\")\n",
		"internal/x/.hidden/run.go":  "package main\n\nvar cmd = exec.Command(\"sh\", \"scripts/ghost.sh\")\n",
		"internal/x/notes/stray.txt": "scripts/stray.sh\n",
	})
}

// rootPatternRepo is a checkout whose one gate tests every Go package
// from the root, as `go test ./...` does.
func rootPatternRepo(t *testing.T) *Repo {
	return testRepo(t, map[string]string{
		".loop/config.yaml":        "merge:\n  mechanism: direct\ngates:\n  - name: test\n    run: go test ./...\n",
		"internal/x/check_test.go": "package x\n\nvar cmd = exec.Command(\"sh\", \"../../scripts/check.sh\")\n",
		"scripts/check.sh":         "#!/bin/sh\n",
		"scripts/stray.sh":         "#!/bin/sh\n",
	})
}

func TestCheckScriptIsReachedByAGate(t *testing.T) {
	l := testLadder(ladderRungs + "\n| 3 | A lint rule | Lint |\n| 4 | " + scriptMechanism + " | A script that a gate runs |")
	section := "### `rest`\n\n**Stronger rungs.** Rung 1, rung 2 and rung 3 fit nothing.\n"
	repo, root := reachRepo(t), rootPatternRepo(t)
	for _, tc := range []struct {
		name        string
		repo        *Repo
		rung, holds string
		want        []string
	}{
		{"a script a gate's command names", repo, "4", "`scripts/check.sh`", nil},
		{"a script the Makefile names, which make reaches", repo, "4", "`scripts/lint-paths.sh`", nil},
		{"a file a reached script sources", repo, "4", "`scripts/lib.sh`", nil},
		{"a file of a directory a gate's command names", repo, "4", "`tools/probe/main.go`", nil},
		{"a script package.json names, which a package manager reaches", repo, "4", "`scripts/sdlc/lessons.ts`", nil},
		{"a script a Go test names, which a package pattern reaches", repo, "4", "`scripts/unit.sh`", nil},
		{"a script a Go test names, which ./... reaches", root, "4", "`scripts/check.sh`", nil},
		{"a package pattern reaches no file that is not Go", repo, "4", "`internal/x/notes/stray.txt`", []string{"wired"}},
		{"a package pattern skips testdata, _ and . directories", repo, "4", "`scripts/ghost.sh`", []string{"wired"}},
		{"./... reaches no script a Go file does not name", root, "4", "`scripts/stray.sh`", []string{"wired"}},
		{"a gate of the config", repo, "4", "`rest-paths`", nil},
		{"a script no gate reaches", repo, "4", "`scripts/stray.sh`", []string{"wired"}},
		{"a reached script beside one no gate reaches", repo, "4", "`scripts/check.sh`, `scripts/stray.sh`", []string{"wired"}},
		{"a file at another rung needs no gate", repo, "3", "`scripts/stray.sh`", nil},
	} {
		text := ledgerText([]spec{{rung: tc.rung, holds: tc.holds}}, section)
		if got := rules(checkLedger(tc.repo, text, l, parseLedger(text))); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStrongerNamesEveryRungAbove(t *testing.T) {
	l := testLadder(ladderRungs + "\n| 3 | A lint rule | The lint configuration |")
	only1 := "### `rest`\n\n**Stronger rungs.** Rung 1 fits nothing.\n"
	if got := ledgerRules(t, []spec{{rung: "3"}}, only1, l); !slices.Equal(got, []string{"stronger"}) {
		t.Errorf("rung 3 that refuses rung 1 alone: %v", got)
	}
	both := "### `rest`\n\n**Stronger rungs.** Rung 1 fits nothing,\nand rung 2 has no plugin.\n"
	if got := ledgerRules(t, []spec{{rung: "3"}}, both, l); got != nil {
		t.Errorf("rung 3 that refuses both: %v", got)
	}
	// The paragraph ends at a blank line, so a later paragraph refuses nothing.
	split := "### `rest`\n\n**Stronger rungs.** Rung 1 fits nothing.\n\nRung 2 has no plugin.\n"
	if got := ledgerRules(t, []spec{{rung: "3"}}, split, l); !slices.Equal(got, []string{"stronger"}) {
		t.Errorf("rung 2 refused in another paragraph: %v", got)
	}
}

func TestLedgerCitation(t *testing.T) {
	l := testLadder(ladderRungs)
	chat := spec{channel: "chat", pr: "-", line: "-"}
	with := func(f func(*spec)) []spec { s := chat; f(&s); return []spec{s} }
	for _, tc := range []struct {
		name string
		rows []spec
		want []string
	}{
		{"a chat row with no pull request and no line", []spec{chat}, nil},
		{"a review row that names its pull request", []spec{{pr: "#1766"}}, nil},
		{"a review row that names no pull request", []spec{{pr: "-"}}, []string{"source"}},
		{"a review row whose PR cell is no number", []spec{{pr: "the thread"}}, []string{"source"}},
		{"a chat row that names a pull request", with(func(s *spec) { s.pr = "#1766" }), []string{"source"}},
		{"a chat row that names a line", with(func(s *spec) { s.line = "`docs/x.md:1`" }), []string{"source"}},
		{"a date that is no date", with(func(s *spec) { s.date = "yesterday" }), []string{"date"}},
		{"a dash for a date", with(func(s *spec) { s.date = "-" }), []string{"date"}},
		{"a row that quotes nothing", with(func(s *spec) { s.correction = "-" }), []string{"words"}},
		{"a channel the ladder does not define", with(func(s *spec) { s.channel = "slack"; s.pr = "#1" }), []string{"channel"}},
	} {
		if got := ledgerRules(t, tc.rows, restSection, l); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	rows := parseLedger(ledgerText([]spec{{correction: "stop that", channel: "chat"}}, "")).rows
	if got := label(rows[0]); got != `chat 2026-09-30 "stop that"` {
		t.Errorf("label = %s", got)
	}
}

// summaryRow is one row for the summary tests: words, channel, PR,
// lesson, scope and rung.
func summaryRows(rows ...[6]string) string {
	var specs []spec
	for _, r := range rows {
		specs = append(specs, spec{correction: r[0], channel: r[1], pr: r[2], line: "-", lesson: r[3], scope: r[4], rung: r[5], holds: "`AGENTS.md`"})
	}
	return ledgerText(specs, "")
}

func summaryOf(text string) []string {
	return summarise(parseLedger(text), testLadder(ladderRungs+"\n| 3 | A lint rule | Lint |"))
}

func TestSummarise(t *testing.T) {
	got := summaryOf(summaryRows(
		[6]string{"a", "review", "#10", "one", "repository", "2"},
		[6]string{"b", "review", "#10", "one", "repository", "2"},
		[6]string{"c", "chat", "-", "two", "repository", "1"}))
	if got[0] != "**3 corrections, 2 lessons, 2 channels, 1 pull requests.**" {
		t.Errorf("head = %s", got[0])
	}
	got = summaryOf(summaryRows(
		[6]string{"a", "review", "#10", "one", "repository", "2"},
		[6]string{"b", "chat", "-", "two", "document", "-"},
		[6]string{"c", "chat", "-", "three", "repository", "pending"}))
	for _, want := range []string{
		"- By channel: chat × 2, review × 1",
		"- By scope: document × 1, repository × 2",
		"- By rung: 2 × 1, no rung × 1, pending × 1",
		"- Rungs of the ladder no row has reached: 1, 3",
		"- Lessons with a section of their own: 0 of 3",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("summary %q holds no %q", got, want)
		}
	}
	got = summaryOf(summaryRows(
		[6]string{"a", "chat", "-", "one", "repository", "pending"},
		[6]string{"b", "chat", "-", "two", "repository", "10"},
		[6]string{"c", "chat", "-", "three", "repository", "2"}))
	if !slices.Contains(got, "- By rung: 2 × 1, 10 × 1, pending × 1") {
		t.Errorf("rungs sort by number: %q", got)
	}
	got = summaryOf(summaryRows())
	if got[0] != "**0 corrections, 0 lessons, 0 channels, 0 pull requests.**" || !slices.Contains(got, "- By channel: none") {
		t.Errorf("empty ledger: %q", got)
	}
	// A half-written row adds no empty key.
	got = summaryOf(summaryRows([6]string{"a", "chat", "-", "one", "repository", "2"}) + "| `b` | | 2026-10-01 | - | - | `two` | | 2 | - |\n")
	if !slices.Contains(got, "- By channel: chat × 1") {
		t.Errorf("empty cell counted: %q", got)
	}
}

func TestCheckSummary(t *testing.T) {
	l := testLadder(ladderRungs)
	rows := summaryRows([6]string{"a", "review", "#10", "one", "repository", "2"})
	text := rows + "\n" + summaryBlock(parseLedger(rows), l) + "\n"
	if got := checkSummary(text, parseLedger(text), l); got != nil {
		t.Fatalf("a derived block fails: %v", got)
	}
	stale := strings.Replace(text, "1 corrections", "9 corrections", 1)
	got := checkSummary(stale, parseLedger(stale), l)
	if len(got) != 1 || !strings.Contains(got[0].Message, "is not what this ledger's rows derive") || !strings.Contains(got[0].Message, "1 corrections") {
		t.Fatalf("stale block: %v", got)
	}
	changed := strings.Replace(text, "`repository`", "`document`", 1)
	if got := checkSummary(changed, parseLedger(changed), l); len(got) != 1 {
		t.Fatalf("a changed row kept its block: %v", got)
	}
	for _, gone := range []string{rows, strings.Replace(text, SummaryClose, "", 1)} {
		got := checkSummary(gone, parseLedger(gone), l)
		if len(got) != 1 || !strings.Contains(got[0].Message, "holds no summary block") || !strings.Contains(got[0].Message, SummaryOpen) {
			t.Fatalf("missing marker: %v", got)
		}
	}
}

func TestLessonsGate(t *testing.T) {
	ctx := context.Background()
	r := lessonRepo(t)
	if got, err := Lessons(ctx, r); err != nil || got != nil {
		t.Fatalf("a project with no ledger: %v %v", got, err)
	}
	raw, err := fs.ReadFile(pack.Source(), LadderSource)
	if err != nil {
		t.Fatal(err)
	}
	rows := ledgerText([]spec{{channel: "chat", pr: "-", line: "-"}}, restSection)
	block := summaryBlock(parseLedger(rows), parseLadder(string(raw)))
	write(t, r.Root, map[string]string{LedgerPath: "# Correction ledger\n\n" + rows + "\n" + block + "\n"})
	if got, err := Lessons(ctx, r); err != nil || got != nil {
		t.Fatalf("a sound ledger: %v %v", got, err)
	}
	write(t, r.Root, map[string]string{LedgerPath: "# Correction ledger\n\n" + rows})
	got, err := Lessons(ctx, r)
	if err != nil || !slices.Equal(rules(got), []string{"summary"}) {
		t.Fatalf("a ledger with no block: %v %v", got, err)
	}
}
