package gates

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runbooks runs the gate over a checkout of base, and returns each finding
// as "file:line rule".
func runbooks(t *testing.T, base map[string]string) []Finding {
	t.Helper()
	fs, err := Runbooks(context.Background(), testRepo(t, base))
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func wantRules(t *testing.T, got []Finding, want ...string) {
	t.Helper()
	if !slices.Equal(rules(got), want) {
		t.Fatalf("want rules %v, got %v", want, got)
	}
}

func TestRunbooksPassesDocumentsThatMatchTheSystem(t *testing.T) {
	got := runbooks(t, map[string]string{
		".loop/config.yaml": "merge:\n  mechanism: direct\ngates:\n  - name: e2e\n    run: make e2e\n",
		"AGENTS.md": strings.Join([]string{
			"Read `docs/agents/implementer.md` and `.loop/policy/implementer.md`.",
			"Read `.loop/brief.md` first. Cite ADRs by number, as `docs/adr/0001`.",
			"Run `loop gates --only lint`, then `loop gates --only e2e`.",
			"Each file under `docs/agents/<item>.md` and `scripts/*.sh` is fine.",
			"Run `scripts/check.sh --fast` before you push.",
			"```bash",
			"loop work show <item>",
			"scripts/check.sh --fast",
			"```",
			"```go",
			"for loop := range xs {}",
			"```",
			"Both runbooks agree. All two runbooks in docs/agents exist.",
		}, "\n"),
		"docs/agents/implementer.md":  "The implementer reads the spec.\n",
		"docs/agents/reviewer.md":     "The implementer reads the spec. Copy `the loop for stalls`.\n",
		"docs/adr/0001-start.md":      "x\n",
		".loop/policy/implementer.md": "Policy.\n",
		"scripts/check.sh":            "true\n",
	})
	wantRules(t, got)
}

func TestRunbooksReadsEachAgentFacingDocument(t *testing.T) {
	bad := "Run `loop wrok show`.\n"
	got := runbooks(t, map[string]string{
		"AGENTS.md":                bad,
		"CLAUDE.md":                bad,
		"CODING_STANDARDS.md":      bad,
		".loop/policy/x.md":        bad,
		"docs/commands/x.md":       bad,
		"docs/rules/x.md":          bad,
		"docs/templates/x.md":      bad,
		"skills/x/SKILL.md":        bad,
		".claude/agents/x.md":      bad,
		"apps/web/AGENTS.md":       bad,
		"apps/web/PRODUCT.md":      bad, // briefs a person, who can ask
		"docs/adr/0001-x.md":       bad,
		"skills/x/references/y.md": bad,
	})
	var files []string
	for _, f := range got {
		files = append(files, f.File)
	}
	want := []string{".claude/agents/x.md", ".loop/policy/x.md", "AGENTS.md", "CLAUDE.md", "CODING_STANDARDS.md",
		"apps/web/AGENTS.md", "docs/commands/x.md", "docs/rules/x.md", "docs/templates/x.md", "skills/x/SKILL.md"}
	if !slices.Equal(files, want) {
		t.Fatalf("want a finding in each of %v, got %v", want, got)
	}
}

func TestRunbooksChecksLoopCommandsAgainstTheSchema(t *testing.T) {
	got := runbooks(t, map[string]string{"AGENTS.md": strings.Join([]string{
		"Run `loop work teleport <item>`.",
		"```sh",
		"loop run finish --verbose",
		"loop run finish --outcome success",
		"```",
		"If `loop work show <item>` refuses with `LEASE_STOLEN`, stop.",
		"Set `GITHUB_TOKEN` and run `pnpm test`.",
	}, "\n")})
	wantRules(t, got, "verb", "flag", "value", "code")
	if got[0].Line != 1 || got[1].Line != 3 || got[3].Line != 6 {
		t.Fatalf("want lines 1, 3 and 6, got %v", got)
	}
}

func TestRunbooksRefusesAGateThatIsNotThere(t *testing.T) {
	got := runbooks(t, map[string]string{
		".loop/config.yaml": "merge:\n  mechanism: direct\ngates:\n  - name: e2e\n    run: make e2e\n",
		"AGENTS.md":         "Run `loop gates --only e2e` and `loop gates --only=smoke`.\n```\nloop gates --fast --only 'runbook'\n```\n",
	})
	wantRules(t, got, "gate", "gate")
	if !strings.Contains(got[0].Message, "smoke") || !strings.Contains(got[1].Message, "runbook") {
		t.Fatalf("want smoke and runbook named, got %v", got)
	}
}

func TestRunbooksRefusesAPathGitDoesNotTrack(t *testing.T) {
	// The design/frames fault: a bare path inside a fence, which a check of
	// code spans alone never read.
	got := runbooks(t, map[string]string{
		"AGENTS.md": "Read the frame:\n\n```\ndocs/frames/<node-id>.json\n```\n\nRead `skills/gone/SKILL.md`.\n",
		"docs/x.md": "x\n",
	})
	wantRules(t, got, "path", "path")
	if got[0].Line != 4 || got[1].Line != 7 {
		t.Fatalf("want lines 4 and 7, got %v", got)
	}
}

func TestRunbooksReadsGitAndNotTheDisk(t *testing.T) {
	r := testRepo(t, map[string]string{"AGENTS.md": "Read `docs/notes.md`.\n", ".gitignore": "docs/notes.md\n"})
	write(t, r.Root, map[string]string{"docs/notes.md": "on disk only\n"})
	got, err := Runbooks(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	wantRules(t, got, "path")
}

func TestPathsIn(t *testing.T) {
	refs := func(text string) []string {
		var out []string
		for _, p := range pathsIn(text) {
			out = append(out, p.ref)
		}
		return out
	}
	for _, tc := range []struct {
		text string
		want []string
	}{
		// A path in plain prose is no promise.
		{"We keep them under docs/adr and cite by number.\n", nil},
		{"Read `skills/x/SKILL.md` first.\n", []string{"skills/x/SKILL.md"}},
		// A code span inside a fence reports once.
		{"```\ncp `docs/templates/adr.md` docs/templates/adr.md\n```\n", []string{"docs/templates/adr.md"}},
		{"```\nscripts/gates.sh --docs | tee x\n```\n", []string{"scripts/gates.sh"}},
		// A URL or an import path holds a root and names no repository path.
		{"```\ngo get github.com/x/y/internal/z\ncurl https://h/docs/x\n```\n", nil},
		{"```\n./scripts/x.sh\n```\n", []string{"scripts/x.sh"}},
	} {
		if got := refs(tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("%q: want %v, got %v", tc.text, tc.want, got)
		}
	}
}

func TestConcreteOf(t *testing.T) {
	for ref, want := range map[string]string{
		"docs/frames/<node-id>.png":          "docs/frames",
		"scripts/sdlc/*.test.ts":             "scripts/sdlc",
		"docs/prd/":                          "docs/prd",
		"<anything>/x":                       "",
		"scripts/sdlc/gates.sh --docs":       "scripts/sdlc/gates.sh",
		"docs/plans/$ITEM.md":                "docs/plans",
		"docs/agents/implementer.md:42":      "docs/agents/implementer.md",
		"docs/rules/x.md#the-rule":           "docs/rules/x.md",
		"apps/web/src/app/(signed-in)/":      "apps/web/src/app/(signed-in)",
		"docs/x.md).":                        "docs/x.md",
		"internal/gates/{runbooks,prose}.go": "internal/gates",
	} {
		if got := concreteOf(ref); got != want {
			t.Errorf("concreteOf(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestCountFindings(t *testing.T) {
	runbooks := map[string]int{"runbooks": 11, "policies": 2}
	for _, tc := range []struct {
		text  string
		want  int
		line  int
		count map[string]int
	}{
		// The AGENTS.md fault: a total written as a word, one behind.
		{text: "All ten runbooks assume it.", want: 1, line: 1},
		{text: "All 10 runbooks assume it.", want: 1, line: 1},
		{text: "All eleven runbooks assume it.", want: 0},
		// A count of a subset is true, and is no claim about all of them.
		{text: "Three runbooks review work.", want: 0},
		// A subset larger than the whole is wrong with no total word.
		{text: "Three runbooks review work.", want: 1, line: 1, count: map[string]int{"runbooks": 2}},
		{text: "Ten runbooks in docs/agents assume it.", want: 1, line: 1},
		{text: "Never write `All ten runbooks assume it`.", want: 0},
		{text: "```\nAll ten runbooks assume it.\n```", want: 0},
		// A total wraps across a line break.
		{text: "Read the skill. All\nten runbooks assume it.", want: 1, line: 2},
		{text: "All ten\nrunbooks assume it.", want: 1, line: 1},
		{text: "There are three policy files.", want: 1, line: 1},
		{text: "All 2 policies apply.", want: 0},
		// A set with no file in the repository names something else.
		{text: "All twelve runbooks of the loop assume it.", want: 0, count: map[string]int{"runbooks": 0}},
	} {
		count := runbooks
		if tc.count != nil {
			count = tc.count
		}
		got := countFindings("AGENTS.md", tc.text, count)
		if len(got) != tc.want || tc.want > 0 && got[0].Line != tc.line {
			t.Errorf("%q: want %d finding(s) on line %d, got %v", tc.text, tc.want, tc.line, got)
		}
	}
}

func TestRunbooksCountsTheDirectory(t *testing.T) {
	got := runbooks(t, map[string]string{
		"AGENTS.md":         "All three runbooks assume it.\n",
		"docs/agents/a.md":  "a\n",
		"docs/agents/b.md":  "b\n",
		"docs/agents/c.txt": "c\n",
	})
	wantRules(t, got, "count")
	if !strings.Contains(got[0].Message, "holds 2") {
		t.Fatalf("want the real count named, got %v", got)
	}
}

func TestClaimFindings(t *testing.T) {
	forms := agentForms([]string{"docs/agents/decomposer.md"})
	claims := func(files map[string]string) []Finding {
		var cs []agentClaim
		names := make([]string, 0, len(files))
		for f := range files {
			names = append(names, f)
		}
		slices.Sort(names)
		for _, f := range names {
			cs = append(cs, claimsIn(f, files[f], forms)...)
		}
		return claimFindings(cs)
	}
	// The fault that shipped: two documents, one fact, opposite answers. The
	// agent's own document holds the answer.
	got := claims(map[string]string{
		"docs/agents/decomposer.md": "The implementer never opens the spec, so copy it.\n",
		".loop/policy/implementer.md": "Read it. The implementer reads the\nspec section it names.\n" +
			"The implementer reads the spec.\n",
	})
	if len(got) != 1 || got[0].File != "docs/agents/decomposer.md" || !strings.Contains(got[0].Message, ".loop/policy/implementer.md:1") {
		t.Fatalf("want one claim against the decomposer, got %v", got)
	}
	// With no owner, each side of a contradiction is reported.
	got = claims(map[string]string{
		"AGENTS.md":           "The code reviewer never reads the spec.\n",
		"CODING_STANDARDS.md": "Each code-reviewer reads the spec.\n",
	})
	if len(got) != 2 || !strings.Contains(got[0].Message, "never reads the spec") {
		t.Fatalf("want both sides reported, got %v", got)
	}
	// Claims that agree, and a wrong example in a fence, report nothing.
	got = claims(map[string]string{
		"AGENTS.md": "The implementer reads your ticket.\n\n```\nThe implementer never reads the ticket.\n```\n",
		"x.md":      "The implementer always reads the ticket.\n",
	})
	if len(got) != 0 {
		t.Fatalf("want no claim, got %v", got)
	}
	if c := claimsIn("x.md", "The decomposer does not read the PRD.\n", forms); len(c) != 1 || c[0].reads || c[0].artifact != "prd" {
		t.Fatalf("want one denial, got %v", c)
	}
}

func TestForbidsRepair(t *testing.T) {
	for s, want := range map[string]bool{
		"Do not restart or manually start the ingest service to manufacture evidence.": true,
		"Observation must never rerun that work to obtain another evidence set.":       true,
		"Never repair the night, so the receipt stays natural.":                        true,
		"A replay, or extra manual run cannot add a night.":                            true,
		"A restarted run does not count as a night.":                                   true,
		"Never use update time or manual runs as health evidence.":                     false,
		"It does not find a night that the vendor never started.":                      false,
		"Start the ingest again, and record the evidence on the work item.":            false,
		"Never run the install script for this fault.":                                 false,
	} {
		if got := forbidsRepair(s); got != want {
			t.Errorf("forbidsRepair(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestRepairFindings(t *testing.T) {
	got := repairFindings("docs/plans/x.md", "# Plan\n\nRead the log.\nDo not restart the\nservice, to keep the evidence.\n")
	if len(got) != 1 || got[0].Line != 4 || got[0].Rule != "repair" {
		t.Fatalf("want one finding on line 4, got %v", got)
	}
	quoted := "```text\nDo not restart it, to keep the evidence.\n```\n\n`Do not restart it, for evidence.`\n"
	if got := repairFindings("x.md", quoted); len(got) != 0 {
		t.Fatalf("a fence and a code span are no instruction, got %v", got)
	}
	got = runbooks(t, map[string]string{"docs/plans/a/b.md": "Never rerun the job, so the evidence stays clean.\n"})
	wantRules(t, got, "repair")
}

// fixture reads one file of testdata/runbooks.
func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "runbooks", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// wantAt checks that each finding has rule and sits on the line at the same
// index of lines.
func wantAt(t *testing.T, got []Finding, rule string, lines ...int) {
	t.Helper()
	var at []int
	for _, f := range got {
		if f.Rule != rule || f.File != "AGENTS.md" {
			t.Fatalf("want only %s findings in AGENTS.md, got %v", rule, got)
		}
		at = append(at, f.Line)
	}
	if !slices.Equal(at, lines) {
		t.Fatalf("want %s on lines %v, got %v", rule, lines, got)
	}
}

func TestRunbooksMakeTargetMustExist(t *testing.T) {
	got := runbooks(t, map[string]string{
		"Makefile": fixture(t, "Makefile"),
		"AGENTS.md": strings.Join([]string{
			"Run `make nosuch` first.",
			"```bash",
			"make build",
			"make proto-check",
			"make linux/amd64",
			"make dist/loop.tar",
			"make test GO=go1.26",
			"$ make -k test race",
			"make -j 4 build",
			"make build && make gone",
			"make <target>",
			"make build > out.log 2>&1 # quiet",
			"```",
		}, "\n"),
	})
	wantAt(t, got, "make_target", 1, 10)
	if !strings.Contains(got[0].Message, "`make nosuch`") || !strings.Contains(got[1].Message, "`make gone`") {
		t.Fatalf("want nosuch and gone named, got %v", got)
	}

	got = runbooks(t, map[string]string{"AGENTS.md": "Run `make build`.\n"})
	wantAt(t, got, "make_target", 1)
}

func TestRunbooksPackageScriptMustExist(t *testing.T) {
	got := runbooks(t, map[string]string{
		"package.json": fixture(t, "package.json"),
		"AGENTS.md": strings.Join([]string{
			"```bash",
			"pnpm run nosuch",
			"npm run nosuch",
			"yarn run nosuch",
			"bun run nosuch",
			"pnpm nosuch",
			"yarn nosuch",
			"pnpm install",
			"pnpm exec nosuch",
			"npm test",
			"npm nosuch",
			"pnpm --filter web nosuch",
			"pnpm -C web nosuch",
			"pnpm -r nosuch",
			"pnpm run build --prod",
			"yarn lint",
			"```",
		}, "\n"),
	})
	wantAt(t, got, "package_script", 2, 3, 4, 5, 6, 7)

	got = runbooks(t, map[string]string{"AGENTS.md": "Run `pnpm run build`.\n"})
	wantAt(t, got, "package_script", 1)
}

func TestRunbooksReportsNoCommandThatIsNotOne(t *testing.T) {
	doc := strings.Join([]string{
		"The operator wrote `... by yourself. make sure this is part ...`.",
		"Run `make nosuch` and `echo make gone`.",
		"```go",
		"m := make(map[string]int)",
		"```",
		"Run `pnpm run nosuch`.",
	}, "\n")
	for name, makefile := range map[string]string{
		"an include line":         "include extra.mk\nbuild:\n\ttrue\n",
		"a target that keeps a $": "OUT ?= bin\nbuild:\n\ttrue\n$(OUT)/loop:\n\ttrue\n",
	} {
		t.Run(name, func(t *testing.T) {
			got := runbooks(t, map[string]string{"Makefile": makefile, "AGENTS.md": doc})
			// The script call on line 6 is the one command the gate can
			// check, so it shows that the gate read the document.
			wantAt(t, got, "package_script", 6)
		})
	}

	// A closed Makefile shows that position alone keeps the words out.
	got := runbooks(t, map[string]string{"Makefile": "build:\n\ttrue\n", "AGENTS.md": strings.Replace(doc, "`make nosuch` and ", "", 1)})
	wantAt(t, got, "package_script", 6)
}
