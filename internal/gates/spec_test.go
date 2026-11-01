package gates

import (
	"context"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	docshape "github.com/iantanwx/loop/internal/shape"
	"github.com/iantanwx/loop/pack"
)

const specFile = "docs/plans/loop-a3f8-engine-cache-spec.md"

func goodSpec(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/spec/good-spec.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func specTemplate(t *testing.T) string {
	t.Helper()
	raw, err := fs.ReadFile(pack.Files, "skills/loop-sdlc-documents/spec.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// lineOf is the number of the last line of text that starts with prefix.
func lineOf(t *testing.T, text, prefix string) int {
	t.Helper()
	n := 0
	for i, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, prefix) {
			n = i + 1
		}
	}
	if n == 0 {
		t.Fatalf("no line starts with %q", prefix)
	}
	return n
}

// C10: the spec gate refuses a misshaped spec, and names the rule and the line.
func TestC10SpecGateRefusesAMisshapedSpec(t *testing.T) {
	good := goodSpec(t)
	if got := docshape.CheckSpec(specFile, good); len(got) != 0 {
		t.Fatalf("good spec = %v", got)
	}
	swapped := strings.Replace(strings.Replace(good, "## Data\n", "## Swap\n", 1), "## Interfaces\n", "## Data\n", 1)
	swapped = strings.Replace(swapped, "## Swap\n", "## Interfaces\n", 1)
	cases := []struct {
		name     string
		path     string
		text     string
		rule     string
		atPrefix string // the finding's line starts with this, or "" for no line
	}{
		{"a missing heading", specFile, strings.Replace(good, "## Flows\n", "### Flows\n", 1), "section", ""},
		{"a repeated heading", specFile, good + "\n## Work\n\nMore work.\n", "section", "## Work"},
		{"an out-of-order heading", specFile, swapped, "section", "## Interfaces"},
		{"an empty section", specFile, strings.Replace(good, "None, because this epic draws nothing.\n", "\n", 1), "empty", "## Observable behaviour"},
		{"a kept placeholder line", specFile, strings.Replace(good, "None.\n\n## Interfaces", "<the schema change, or None>\n\n## Interfaces", 1), "template", "<the schema"},
		{"a kept example marker", specFile, strings.Replace(good, "None.\n\n## Flows", "None.\n\n> **Good**\n\n## Flows", 1), "template", "> **Good**"},
		{"a filename that does not start with the epic id", "docs/plans/loop-b9-engine-cache-spec.md", good, "name", "- **Epic:**"},
		{"a filename that holds the epic id with no dash", "docs/plans/loop-a3f8engine-spec.md", good, "name", "- **Epic:**"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := docshape.CheckSpec(c.path, c.text)
			if len(got) != 1 || got[0].Rule != c.rule {
				t.Fatalf("got %v, want one %s finding", got, c.rule)
			}
			want := 0
			if c.atPrefix != "" {
				want = lineOf(t, c.text, c.atPrefix)
			}
			if got[0].Line != want {
				t.Fatalf("line = %d, want %d: %v", got[0].Line, want, got[0])
			}
		})
	}
}

// C10: a copy of the template fails on every rule but empty.
func TestC10SpecGateRefusesACopyOfTheTemplate(t *testing.T) {
	got := docshape.CheckSpec("docs/plans/loop-a3f8-copy-spec.md", specTemplate(t))
	seen := map[string]bool{}
	for _, f := range got {
		seen[f.Rule] = true
	}
	if !seen["template"] || !seen["name"] {
		t.Fatalf("template copy = %v", got)
	}
}

// C10: the gate reads every docs/plans/*-spec.md, and no other file. C18
// adds docs/specs/.
func TestC10SpecGateReadsEachSpec(t *testing.T) {
	good := goodSpec(t)
	r := testRepo(t, map[string]string{
		specFile:                                  good,
		"docs/plans/loop-b1-other-spec.md":        good,
		"docs/plans/loop-a3f8-engine-notes.md":    "not a spec",
		"docs/prd/loop-a3f8-engine-cache-spec.md": "not in docs/plans",
	})
	got, err := Spec(context.Background(), r)
	if err != nil || len(got) != 1 || got[0].Rule != "name" || got[0].File != "docs/plans/loop-b1-other-spec.md" || got[0].Line == 0 {
		t.Fatalf("got %v %v", got, err)
	}
	if got, err := Spec(context.Background(), testRepo(t, nil)); err != nil || len(got) != 0 {
		t.Fatalf("no docs/plans = %v %v", got, err)
	}
}

// C10: each spec on the base passes, and --docs runs the gate after prd.
func TestC10SpecGatePassesOnTheRepository(t *testing.T) {
	got, err := Spec(context.Background(), &Repo{Root: "../.."})
	if err != nil || len(got) != 0 {
		t.Fatalf("specs of the repository = %v %v", got, err)
	}
	docs, _ := Select(nil, true, false, "")
	names := make([]string, len(docs))
	for i, g := range docs {
		names[i] = g.Name
	}
	if i := slices.Index(names, "spec"); i < 0 || i != slices.Index(names, "prd")+1 {
		t.Fatalf("--docs = %v", names)
	}
}

var (
	templatePlaceholder = regexp.MustCompile(`<[^<>]+>`)
	templateMarkers     = []string{"**Good**", "> **Good**", "> **Bad, and why**"}
)

// C11: the gate's lists match the template. The headings equal
// docshape.SpecSections in order, and the marker and placeholder lines outside
// a fence equal docshape.TemplateLines as a set.
func TestC11SpecListsMatchTheTemplate(t *testing.T) {
	var headings, lines []string
	fenced, instructions := false, false
	for _, l := range strings.Split(specTemplate(t), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```"):
			fenced = !fenced
			continue
		case fenced:
			continue
		case strings.HasPrefix(l, "## "):
			headings = append(headings, strings.TrimSpace(l[3:]))
			continue
		}
		first := !instructions && len(headings) == 0 && strings.HasPrefix(t, ">")
		instructions = instructions || first
		if first || templatePlaceholder.MatchString(t) || slices.Contains(templateMarkers, t) || strings.HasPrefix(t, "> **Bad.**") {
			lines = append(lines, t)
		}
	}
	if !slices.Equal(headings, docshape.SpecSections) {
		t.Errorf("template headings = %q\nSpecSections      = %q", headings, docshape.SpecSections)
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	want := slices.Clone(docshape.TemplateLines)
	slices.Sort(want)
	if !slices.Equal(lines, want) {
		t.Errorf("template lines:\n%s\nTemplateLines:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
