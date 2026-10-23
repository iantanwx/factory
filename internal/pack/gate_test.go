package pack

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/cli"
)

func kinds(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func checkText(t *testing.T, text string) []Finding {
	t.Helper()
	got, err := CheckText("runbooks/x.md", text, cli.Surface())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// wantOne asserts exactly one finding of kind whose detail holds want.
func wantOne(t *testing.T, got []Finding, kind, want string) {
	t.Helper()
	if len(got) != 1 || got[0].Kind != kind || !strings.Contains(got[0].Detail, want) {
		t.Fatalf("want one %s finding naming %q, got %v", kind, want, got)
	}
}

func TestGateRefusesAnUnknownVerb(t *testing.T) {
	wantOne(t, checkText(t, "Run `loop work teleport <item>` first.\n"), "verb", "work teleport")
	wantOne(t, checkText(t, "```bash\nloop wrok show <item>\n```\n"), "verb", "wrok")
	// One alternative of an alternation is enough to fail.
	wantOne(t, checkText(t, "Run `loop decision fire|explode <decision>`.\n"), "verb", "decision explode")
}

func TestGateRefusesAnUnknownFlag(t *testing.T) {
	wantOne(t, checkText(t, "Run `loop run finish --outcome done --verbose`.\n"), "flag", "--verbose")
	wantOne(t, checkText(t, "```sh\nloop pr open --title \"x\" --body-file /tmp/b.md\n```\n"), "flag", "--body-file")
	// An optional flag in brackets is still a flag.
	wantOne(t, checkText(t, "Run `loop escalation raise --score 4 --body <text> [--kind outside]`.\n"), "flag", "--kind")
}

func TestGateRefusesAnEnumValueTheVerbDoesNotTake(t *testing.T) {
	wantOne(t, checkText(t, "Run `loop run finish --outcome success`.\n"), "value", "success")
	wantOne(t, checkText(t, "Run `loop review submit <item> --verdict pass|maybe --head <sha>`.\n"), "value", "maybe")
}

func TestGateRefusesAnUnknownRefusalCode(t *testing.T) {
	wantOne(t, checkText(t, "On `LEASE_STOLEN`, stop.\n"), "code", "LEASE_STOLEN")
	wantOne(t, checkText(t, "```json\n{\"code\": \"NO_SUCH_CODE\"}\n```\n"), "code", "NO_SUCH_CODE")
	// A code in prose escapes the check, so the gate refuses it there too.
	wantOne(t, checkText(t, "On LEASE_NOT_HELD, stop.\n"), "code", "LEASE_NOT_HELD")
}

func TestGateRefusesVocabularyThatBelongsToOneProject(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"Ask Willis about the copy.\n", "Willis"},
		{"Ian merges the PRD.\n", "Ian"},
		{"Move the ticket to In Review.\n", "In Review"},
		{"The issue waits in Needs Human.\n", "Needs Human"},
		{"Move it to \"Done\".\n", `"Done"`},
		{"Read `/home/someone/repo/AGENTS.md` first.\n", "/home/"},
		{"Read /Users/someone/notes.md first.\n", "/Users/"},
		{"Keep it in `~/.config/loop`.\n", "~/"},
		{"Run `pnpm gates`.\n", "pnpm"},
		{"File it in Linear.\n", "Linear"},
	} {
		got := checkText(t, tc.text)
		if !slices.Contains(kinds(got), "vocabulary") || !strings.Contains(got[0].Detail, tc.want) {
			t.Errorf("%q: want a vocabulary finding naming %q, got %v", tc.text, tc.want, got)
		}
	}
}

func TestGateAcceptsKnownReferences(t *testing.T) {
	text := strings.Join([]string{
		"Read `.loop/brief.md`, then run `loop work show <item>`.",
		"Finish with `loop run finish --outcome done|failed|blocked`.",
		"Run `loop escalation raise --score <level> --body <text> [--choice inside|outside]`.",
		"On `LEASE_NOT_HELD` or `USAGE`, stop. Exit `WAIT` means call again.",
		"Write the report to `/tmp/fault.md` and keep `/dev/null` out of it.",
		"The `loop work` noun and the `loop schema` verb exist. So does `loop <noun> <verb>`.",
		"Every flag takes `--help`, and `loop decision fire|defer <decision>` exists.",
		"Branch `loop/<item>` and directory `.loop/` are not commands.",
		"```bash",
		"sha=$(loop pr show | jq -r .data.head) && loop review submit <item> --verdict pass --head \"$sha\"",
		"loop review rounds <item> > /tmp/rounds.json",
		"loop branch push   # the server pushes",
		"```",
		"Done is a fine word at the start of a sentence, and so is the word linear.",
	}, "\n")
	if got := checkText(t, text); len(got) != 0 {
		t.Fatalf("want no findings, got %v", got)
	}
}

func TestGateReportsTheLineOfEachFinding(t *testing.T) {
	got := checkText(t, "line one\n\n```\nloop nope\n```\n")
	if len(got) != 1 || got[0].Line != 4 || got[0].File != "runbooks/x.md" {
		t.Fatalf("want one finding on line 4, got %v", got)
	}
}

func TestCheckCommandsGatesCommandsAndNotThePackVocabulary(t *testing.T) {
	check := func(text string) []Finding {
		t.Helper()
		got, err := CheckCommands("AGENTS.md", text, cli.Surface())
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	wantOne(t, check("Run `loop work teleport <item>` first.\n"), "verb", "work teleport")
	wantOne(t, check("```sh\nloop run finish --verbose\n```\n"), "flag", "--verbose")
	wantOne(t, check("Run `loop run finish --outcome success`.\n"), "value", "success")
	// A code on a line that runs loop is a refusal code of that command.
	wantOne(t, check("If `loop work show <item>` refuses with `LEASE_STOLEN`, stop.\n"), "code", "LEASE_STOLEN")
	// An unknown word after loop in command position is still a verb.
	wantOne(t, check("```\nx && loop wrok show\n```\n"), "verb", "wrok")
	// A project's own names, tools and upper-case words are its business,
	// and so is loop as a word.
	text := strings.Join([]string{
		"Run `pnpm test` with `GITHUB_TOKEN` set. Ian owns MAX_RETRIES in Linear.",
		"Quote `watch the loop for stalls` and `so the loop is fast`.",
		"On `NOT_FOUND` from `loop work show <item>`, stop.",
		"```bash",
		"loop gates --fast",
		"```",
	}, "\n")
	if got := check(text); len(got) != 0 {
		t.Fatalf("want no findings, got %v", got)
	}
}

// fixture is a minimal valid pack source.
func fixture() fstest.MapFS {
	return fstest.MapFS{
		"manifest.json": {Data: []byte(`{"runbooks": {"implementer": {"attended": false, "skills": ["loop-sdlc-turn"], "policy": "implementer"}},
			"skills": ["loop-sdlc-turn"]}`)},
		"runbooks/implementer.md": {Data: []byte("---\nname: loop-implementer\ndescription: Build one ticket.\n---\n\n" +
			"Read `../loop-sdlc-turn/SKILL.md` first. Read `.loop/policy/implementer.md` when it exists.\n")},
		"skills/loop-sdlc-turn/SKILL.md": {Data: []byte("---\nname: loop-sdlc-turn\ndescription: One run.\n---\n\nRun `loop run finish --outcome done`.\n")},
	}
}

func check(t *testing.T, src fstest.MapFS) []Finding {
	t.Helper()
	got, err := Check(src, cli.Surface())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestGatePassesAValidFixture(t *testing.T) {
	if got := check(t, fixture()); len(got) != 0 {
		t.Fatalf("want no findings, got %v", got)
	}
}

func TestGateChecksEveryFileOfThePack(t *testing.T) {
	src := fixture()
	src["skills/loop-sdlc-turn/reference.md"] = &fstest.MapFile{Data: []byte("Run `loop work vanish`.\n")}
	wantOne(t, check(t, src), "verb", "work vanish")
}

func TestGateRequiresEachRunbookToNameItsSkillsByPath(t *testing.T) {
	src := fixture()
	src["runbooks/implementer.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-implementer\ndescription: Build.\n---\n\n" +
		"Read the turn skill. Read `.loop/policy/implementer.md` when it exists.\n")}
	wantOne(t, check(t, src), "skill", "../loop-sdlc-turn/SKILL.md")
}

func TestGateRequiresEveryPackPathToExist(t *testing.T) {
	src := fixture()
	src["skills/loop-sdlc-turn/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-sdlc-turn\ndescription: One run.\n---\n\n" +
		"Read `../loop-sdlc-triage/SKILL.md`.\n")}
	wantOne(t, check(t, src), "path", "../loop-sdlc-triage/SKILL.md")
}

func TestGateRequiresTheManifestToNameEveryFile(t *testing.T) {
	src := fixture()
	src["runbooks/stray.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-stray\ndescription: x\n---\n")}
	wantOne(t, check(t, src), "manifest", "runbooks/stray.md")

	src = fixture()
	src["skills/loop-extra/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-extra\ndescription: x\n---\n")}
	wantOne(t, check(t, src), "manifest", "loop-extra")
}

func TestGateRequiresFrontmatterThatNamesTheDirectory(t *testing.T) {
	src := fixture()
	src["skills/loop-sdlc-turn/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: sdlc-turn\ndescription: One run.\n---\n")}
	wantOne(t, check(t, src), "frontmatter", "loop-sdlc-turn")
}

func TestGateRequiresTheRunbookToNameItsPolicyFile(t *testing.T) {
	src := fixture()
	src["runbooks/implementer.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-implementer\ndescription: Build.\n---\n\n" +
		"Read `../loop-sdlc-turn/SKILL.md` first.\n")}
	wantOne(t, check(t, src), "policy", ".loop/policy/implementer.md")
}

func TestGateRefusesAnUnattendedRunbookThatAsksAPerson(t *testing.T) {
	src := fixture()
	src["runbooks/implementer.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-implementer\ndescription: Build.\n---\n\n" +
		"Read `../loop-sdlc-turn/SKILL.md` and `.loop/policy/implementer.md`. If unsure, ask the operator.\n")}
	wantOne(t, check(t, src), "attended", "ask the operator")
}
