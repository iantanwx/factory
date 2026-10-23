package pack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// The fixtures are a two-level rubric and a corpus that agree, so each
// test changes one thing (triage-rubric.test.ts, alpha).
var rubricParts = map[string]string{
	"levels": "| Level | Name | What answers the finding | Route |\n| --- | --- | --- | --- |\n" +
		"| 1 | `computed` | A command in this repository | loop |\n" +
		"| 2 | `owner-only` | A fact the owner holds | person |",
	"one": "### Level 1, computed\n\nChoose this level when a command answers it.\n\nNot this level when you can name no command.",
	"two": "### Level 2, owner-only\n\nChoose this level when the owner alone knows.\n\nNot this level when a command reads the fact.",
	"gate": "| Option | When to choose it |\n| --- | --- |\n" +
		"| `inside` | Every answer changes files here |\n| `outside` | One answer acts outside |",
	"route": "## The route\n\n- One judge answers `outside`.\n- One judge scores level 2.",
}

func rubricText(over map[string]string) string {
	var parts []string
	for _, k := range []string{"levels", "one", "two", "gate", "route"} {
		v, ok := over[k]
		if !ok {
			v = rubricParts[k]
		}
		if v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, "\n\n")
}

const corpusFixture = `{"about": "x", "cases": [
  {"id": "C1", "stage": "implementer", "route": "loop", "findings": [
    {"id": "C1-F1", "level": "computed", "gate": "inside", "route": "loop", "finding": "a command answers it"}]},
  {"id": "C2", "stage": "implementer", "route": "person", "findings": [
    {"id": "C2-F1", "level": "owner-only", "gate": "inside", "route": "person", "finding": "only the owner knows"}]}
]}`

func triageKinds(text, corpus string) []string {
	r := parseRubric(text)
	return kinds(append(checkRubric("rubric", text, r), checkCorpus(r, []byte(corpus))...))
}

func TestTriageFixturesAgree(t *testing.T) {
	if got := triageKinds(rubricText(nil), corpusFixture); len(got) != 0 {
		t.Fatalf("the fixtures disagree: %v", got)
	}
}

func TestTriageRubricFaults(t *testing.T) {
	extra := "| 3 | `derived` | Our own documents | loop |"
	for _, tc := range []struct {
		name, text, want string
	}{
		{"a level with no section", rubricText(map[string]string{"two": ""}), "description"},
		{"a level that never says when not to choose it",
			rubricText(map[string]string{"two": "### Level 2, owner-only\n\nChoose this level when the owner alone knows."}), "borderline"},
		{"a route that is neither loop nor person",
			strings.Replace(rubricText(nil), "| A fact the owner holds | person |", "| A fact | owner |", 1), "route"},
		{"two levels with one name",
			strings.Replace(rubricText(nil), "`owner-only` | A fact the owner holds", "`computed` | A fact", 1), "duplicate"},
		{"a scale that alternates",
			strings.Replace(rubricText(nil), "| person |", "| person |\n"+extra, 1) +
				"\n\n### Level 3, derived\n\nChoose it.\n\nNot this level when it is clear.", "ordered"},
		{"a gate with three options",
			strings.Replace(rubricText(nil), "| One answer acts outside |", "| One answer acts outside |\n| `unclear` | Nobody knows |", 1), "gate"},
		{"a route section that names an option the gate lacks",
			rubricText(map[string]string{"route": "## The route\n\n- One judge answers `external`."}), "escalates"},
		{"no level at all", "nothing here", "levels"},
	} {
		if got := triageKinds(tc.text, corpusFixture); !slices.Contains(got, tc.want) {
			t.Errorf("%s: kinds %v, want %s", tc.name, got, tc.want)
		}
	}
}

func TestTriageCorpusFaults(t *testing.T) {
	for _, tc := range []struct {
		name, from, to, want string
	}{
		{"a level the rubric never defines", `"level": "computed"`, `"level": "measured"`, "level"},
		{"a gate answer the rubric never defines", `"gate": "inside", "route": "loop"`, `"gate": "maybe", "route": "loop"`, "gate"},
		{"a route that does not follow from its level", `"gate": "inside", "route": "loop"`, `"gate": "inside", "route": "person"`, "follows"},
		// The escalating gate answer overrides a level that routes to the loop.
		{"outside routed to the loop", `"gate": "inside", "route": "loop"`, `"gate": "outside", "route": "loop"`, "follows"},
		{"a case route that its findings do not compose to", `"route": "person", "findings"`, `"route": "loop", "findings"`, "composes"},
		{"a case with no finding", `"findings": [
    {"id": "C1-F1", "level": "computed", "gate": "inside", "route": "loop", "finding": "a command answers it"}]`, `"findings": []`, "case"},
		{"a field the corpus does not define", `"about": "x"`, `"about": "x", "owner": "y"`, "corpus"},
	} {
		c := strings.Replace(corpusFixture, tc.from, tc.to, 1)
		if c == corpusFixture {
			t.Fatalf("%s: the fixture holds no %s", tc.name, tc.from)
		}
		if got := triageKinds(rubricText(nil), c); !slices.Contains(got, tc.want) {
			t.Errorf("%s: kinds %v, want %s", tc.name, got, tc.want)
		}
	}
}

func TestTriageLevelNoFindingScoresIsUnused(t *testing.T) {
	c := strings.Replace(corpusFixture, `"level": "owner-only", "gate": "inside", "route": "person"`, `"level": "computed", "gate": "outside", "route": "person"`, 1)
	if got := triageKinds(rubricText(nil), c); !slices.Equal(got, []string{"unused"}) {
		t.Fatalf("kinds %v, want unused", got)
	}
}

func TestTriageFindingNamesItsLine(t *testing.T) {
	c := strings.Replace(corpusFixture, `"level": "computed"`, `"level": "measured"`, 1)
	got := checkCorpus(parseRubric(rubricText(nil)), []byte(c))
	if len(got) == 0 || got[0].Line != 3 {
		t.Fatalf("findings %v, want the first on line 3", got)
	}
}

// TestTriageServerDrift re-words one cell of the shipped rubric at a time.
// A drift in one place and not the other routes by a scale nobody wrote.
func TestTriageServerDrift(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "pack", RubricSource))
	if err != nil {
		t.Fatal(err)
	}
	real := string(raw)
	if got := checkServer("rubric", real, parseRubric(real)); len(got) != 0 {
		t.Fatalf("the shipped rubric and the server disagree: %v", got)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"a route", "| This repository's own documents and rules | loop |", "| This repository's own documents and rules | person |"},
		{"the cell a refusal quotes", "| This repository's own documents and rules |", "| Our documents |"},
		{"a level the server lacks", "| `measured` |", "| `sampled` |"},
		{"the escalating option", "- One judge answers `outside`.", "- One judge answers `inside`."},
		{"a convergence cell", "| 2 | `align` |", "| 2 | `agree` |"},
		{"a threshold", "| `confidence` | 0.85 |", "| `confidence` | 0.9 |"},
		{"a threshold that is no fraction", "| `confidence` | 0.85 |", "| `confidence` | high |"},
	} {
		text := strings.Replace(real, tc.from, tc.to, 1)
		if text == real {
			t.Fatalf("%s: the rubric holds no %q", tc.name, tc.from)
		}
		if got := checkServer("rubric", text, parseRubric(text)); !slices.Contains(kinds(got), "server") {
			t.Errorf("%s: no server finding", tc.name)
		}
	}
}

func TestTriageUnscoredRaise(t *testing.T) {
	src := fstest.MapFS{
		"skills/loop-x/SKILL.md": {Data: []byte("A refusal of `loop escalation raise` is prose.\n\n```bash\n" +
			"loop escalation raise --body \"x\"\nloop escalation raise --score 4 --body \"x\"\n```\n")},
	}
	got, err := checkReach(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != "unscored" || got[0].Line != 4 {
		t.Fatalf("findings %v, want one unscored on line 4", got)
	}
}

func TestTableMatchesTheWholeHeader(t *testing.T) {
	prefix := "| Case | Finding | Level |\n| --- | --- | --- |\n| C9 | F1 | `computed` |"
	if rows := Table(prefix, "case", "finding", "level", "gate"); rows != nil {
		t.Fatalf("a header prefix matched: %v", rows)
	}
	if rows := Table(prefix, "case", "finding", "level"); len(rows) != 1 || rows[0].Line != 3 {
		t.Fatalf("rows %v", rows)
	}
}

func TestSectionSkipsFencedHeadings(t *testing.T) {
	text := "### `a`\n\n```bash\n# a comment\n```\n\nbody\n\n### `b`\n\nother\n"
	got := Section(text, 3, func(t string) bool { return t == "`a`" })
	if !strings.Contains(got, "body") || strings.Contains(got, "other") {
		t.Fatalf("section = %q", got)
	}
}
