package gates

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// standards runs the gate over a checkout of files, with baseline as the
// allowed map of .loop/suppressions.json. A gate that passes everything
// looks like no gate, so each rule below is proved to fire.
func standards(t *testing.T, files map[string]string, baseline map[string]int) []Finding {
	t.Helper()
	if baseline != nil {
		raw, err := json.Marshal(map[string]any{"allowed": baseline})
		if err != nil {
			t.Fatal(err)
		}
		files[suppressionsPath] = string(raw)
	}
	fs, err := Standards(context.Background(), testRepo(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func goFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/standards/suppressed.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestStandardsCleanSourcePasses(t *testing.T) {
	r := testRepo(t, map[string]string{"src/a.ts": "export const a = 1;\n", "main.go": "package main\n"})
	files, err := r.trackedFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A scan that read nothing would pass too.
	if !slices.Equal(files, []string{"main.go", "src/a.ts"}) {
		t.Fatalf("files = %v", files)
	}
	if fs, err := Standards(context.Background(), r); err != nil || len(fs) != 0 {
		t.Fatalf("clean = %v %v", fs, err)
	}
}

func TestStandardsEscapeHatches(t *testing.T) {
	for file, line := range map[string]string{
		"a.ts":  "// @ts-ignore",
		"b.ts":  "// @ts-expect-error",
		"c.tsx": "const a = b as any;",
		"d.mjs": "// @ts-ignore",
		"e.py":  "x = f()  # type: ignore[attr-defined]",
		"f.py":  "x = f()  # pyright: ignore",
		"g.py":  "x = cast(Any, f())",
	} {
		fs := standards(t, map[string]string{file: line + "\n"}, nil)
		if !slices.Equal(rules(fs), []string{"escape_hatch"}) || fs[0].Line != 1 {
			t.Errorf("%s %q: %v", file, line, fs)
		}
	}
	// A word that holds the needle is not one.
	if fs := standards(t, map[string]string{"a.ts": "// it has anything it needs\n"}, nil); len(fs) != 0 {
		t.Errorf("prose = %v", fs)
	}
}

func TestStandardsSuppressionBaseline(t *testing.T) {
	one := "// eslint-disable-next-line some/rule -- a reason\nexport const a = 1;\n"
	if fs := standards(t, map[string]string{"a.tsx": one}, nil); !slices.Equal(rules(fs), []string{"new_suppression"}) {
		t.Errorf("not in the baseline = %v", fs)
	}
	if fs := standards(t, map[string]string{"a.tsx": one}, map[string]int{"a.tsx::some/rule": 1}); len(fs) != 0 {
		t.Errorf("in the baseline = %v", fs)
	}
	// The ratchet: one is allowed, two is not.
	if fs := standards(t, map[string]string{"a.tsx": one + one}, map[string]int{"a.tsx::some/rule": 1}); !slices.Equal(rules(fs), []string{"new_suppression"}) {
		t.Errorf("one past the baseline = %v", fs)
	}
	// And a removed suppression must lower the baseline.
	if fs := standards(t, map[string]string{"a.tsx": "export const a = 1;\n"}, map[string]int{"a.tsx::some/rule": 1}); !slices.Equal(rules(fs), []string{"stale_baseline"}) {
		t.Errorf("stale = %v", fs)
	}
	if fs := standards(t, map[string]string{"a.tsx": "// oxlint-disable-next-line some/rule\n"}, nil); !slices.Equal(rules(fs), []string{"new_suppression"}) {
		t.Errorf("oxlint = %v", fs)
	}
	if fs := standards(t, map[string]string{"a.ts": "/**\n * We never use eslint-disable here, and the set of eslint-disable rules is empty.\n */\n"}, nil); len(fs) != 0 {
		t.Errorf("prose naming the directive = %v", fs)
	}
}

func TestStandardsCountsEachLanguage(t *testing.T) {
	r := testRepo(t, map[string]string{
		"x/a.go": goFixture(t),
		"b.py":   "import os  # noqa: F401, E501\nx = 1  # noqa\n# pylint: disable=too-many-args,unused-import\n",
		"c.rs":   "#[allow(clippy::too_many_arguments, dead_code)]\nfn f() {}\n#![expect(unused)]\n",
		"d.ts":   "// eslint-disable-next-line\n",
	})
	fs, err := Standards(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		if f.Rule != "new_suppression" {
			t.Fatalf("finding %v", f)
		}
		got = append(got, f.File+"::"+strings.TrimSuffix(strings.Fields(f.Message)[3], ","))
	}
	// A directive that names no rule silences them all, so it counts as all.
	want := []string{
		"b.py::E501", "b.py::F401", "b.py::all", "b.py::too-many-args", "b.py::unused-import",
		"c.rs::clippy::too_many_arguments", "c.rs::dead_code", "c.rs::unused",
		"d.ts::all",
		"x/a.go::SA4006", "x/a.go::all", "x/a.go::errcheck",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("keys = %v\nwant   %v", got, want)
	}
	if fs := standards(t, map[string]string{"x/a.go": goFixture(t)}, map[string]int{"x/a.go::SA4006": 1, "x/a.go::all": 1, "x/a.go::errcheck": 1}); len(fs) != 0 {
		t.Fatalf("Go fixture in the baseline = %v", fs)
	}
}

func TestStandardsFocusedTest(t *testing.T) {
	for _, line := range []string{`test.only("x", () => {});`, `describe.only ("x", () => {});`, `it.only("x", () => {});`} {
		if fs := standards(t, map[string]string{"a.test.mjs": line + "\n"}, nil); !slices.Equal(rules(fs), []string{"focused_test"}) {
			t.Errorf("%q = %v", line, fs)
		}
	}
	// Each file reads the needles of its own language alone.
	if fs := standards(t, map[string]string{"a.go": "// test.only( and // @ts-ignore are TypeScript\n"}, nil); len(fs) != 0 {
		t.Errorf("TypeScript needles in Go = %v", fs)
	}
}

func TestStandardsSkipsCodeTheProjectDoesNotWrite(t *testing.T) {
	bad := "// @ts-ignore\n"
	fs := standards(t, map[string]string{
		"node_modules/dep/index.js": bad,
		"vendor/x/a.ts":             bad,
		"app/dist/a.js":             bad,
		"build/a.js":                bad,
		"gen/a.ts":                  bad,
		"x/testdata/a.ts":           bad,
		"src/api_generated.ts":      bad,
		"README.md":                 bad,
		"needles.json":              bad,
		"src/a.ts":                  "export const a = 1;\n",
	}, nil)
	if len(fs) != 0 {
		t.Fatalf("findings = %v", fs)
	}
}

func TestStandardsRejectsAMalformedBaseline(t *testing.T) {
	r := testRepo(t, map[string]string{suppressionsPath: "{"})
	if _, err := Standards(context.Background(), r); err == nil {
		t.Fatal("a malformed baseline passed")
	}
}

// The gate's own source spells no needle, so the loop's run of the gate
// needs no exclusion an agent could hide behind.
func TestStandardsSourceDoesNotMatchItself(t *testing.T) {
	langs, err := loadNeedles()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"standards.go", "standards_test.go", "commits.go", "commits_test.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if fs, counts := langFor(langs, f).scan(f, string(raw)); len(fs) != 0 || len(counts) != 0 {
			t.Errorf("%s: %v %v", f, fs, counts)
		}
	}
}

func TestStaleAnchors(t *testing.T) {
	ctx := context.Background()
	r := testRepo(t, nil)
	good := strings.TrimSpace(git(t, r.Root, "rev-parse", "HEAD"))
	commit(t, r, "feat: add a commit that main never reaches", map[string]string{"x": "x\n"})
	gone := strings.TrimSpace(git(t, r.Root, "rev-parse", "HEAD"))[:8]
	commit(t, r, "docs: cite both", map[string]string{
		"docs/solutions/correction-ledger.md": "at `" + gone + "` it did\nand `" + gone + "` again\n",
		"docs/adr/0001-x.md":                  "and `" + good + "` is fine\n",
		"AGENTS.md":                           "`" + gone + "`\n",
		"CLAUDE.md":                           "`" + gone + "`\n",
		"CODING_STANDARDS.md":                 "`" + gone + "`\n",
		".loop/policy/merge.md":               "`" + gone + "`\n",
		// Out of scope: code holds ids as fixture data, and other Markdown is not a project doc.
		"internal/x_test.go": "var id = \"`" + gone + "`\"\n",
		"README.md":          "`" + gone + "`\n",
		"docs/notes.txt":     "`" + gone + "`\n",
	})
	fs, err := Standards(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	var at []string
	for _, f := range fs {
		if f.Rule != "unreachable_commit" {
			t.Fatalf("finding %v", f)
		}
		at = append(at, fmt.Sprintf("%s:%d", f.File, f.Line))
	}
	want := []string{".loop/policy/merge.md:1", "AGENTS.md:1", "CLAUDE.md:1", "CODING_STANDARDS.md:1", "docs/solutions/correction-ledger.md:1", "docs/solutions/correction-ledger.md:2"}
	if !slices.Equal(at, want) {
		t.Fatalf("at = %v, want %v", at, want)
	}
	if !strings.Contains(fs[0].Message, "Cite the pull request instead") {
		t.Errorf("message = %q", fs[0].Message)
	}
}

func TestStaleAnchorsCannotDecideWithoutTheBase(t *testing.T) {
	r := testRepo(t, map[string]string{"docs/adr/0001-x.md": "at `aaaa111` it did\n"})
	var out bytes.Buffer
	r.Out, r.Base = &out, "origin/nope"
	fs, err := Standards(context.Background(), r)
	if err != nil || len(fs) != 0 {
		t.Fatalf("findings = %v %v", fs, err)
	}
	if !strings.Contains(out.String(), "SKIP no commit id was checked: origin/nope does not resolve here") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestCitedCommit(t *testing.T) {
	ids := func(s string) []string {
		var out []string
		for _, m := range citedCommit.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		return out
	}
	if got := ids("`abc1234` and `def5678`"); !slices.Equal(got, []string{"abc1234", "def5678"}) {
		t.Errorf("two on a line = %v", got)
	}
	if got := ids("`" + strings.Repeat("a", 40) + "`"); len(got) != 1 {
		t.Errorf("40 = %v", got)
	}
	for _, s := range []string{"`abc123`", "`" + strings.Repeat("a", 41) + "`", "ce18451d on its own", "`ce18451z`", "`CE18451D`"} {
		if got := ids(s); got != nil {
			t.Errorf("%q = %v", s, got)
		}
	}
}
