package pack

import (
	"io/fs"
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

// agentFixture is the valid fixture plus one agent that the implementer
// runbook lists and names.
func agentFixture() fstest.MapFS {
	src := fixture()
	src["manifest.json"] = &fstest.MapFile{Data: []byte(`{"runbooks": {"implementer": {"attended": false, "skills": ["loop-sdlc-turn"],
		"agents": ["loop-helper"], "policy": "implementer"}}, "skills": ["loop-sdlc-turn"], "agents": ["loop-helper"]}`)}
	src["runbooks/implementer.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-implementer\ndescription: Build one ticket.\n---\n\n" +
		"Read `../loop-sdlc-turn/SKILL.md` first. Read `.loop/policy/implementer.md` when it exists.\n" +
		"Start the `loop-helper` agent for a read.\n")}
	src["agents/loop-helper.md"] = agent("loop-helper", "Read, Grep, Glob, Bash", "Run `loop work show <item>`. Read `.claude/skills/loop-sdlc-turn/SKILL.md`.\n")
	return src
}

func agent(name, tools, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\nname: " + name + "\ndescription: Reads one thing.\ntools: " + tools + "\n---\n\n" + body)}
}

func TestGatePassesAFixtureWithAnAgent(t *testing.T) {
	if got := check(t, agentFixture()); len(got) != 0 {
		t.Fatalf("want no findings, got %v", got)
	}
}

func TestGateRefusesAnAgentToolThatWrites(t *testing.T) {
	src := agentFixture()
	src["agents/loop-helper.md"] = agent("loop-helper", "Read, Write, Bash", "Run `loop work show <item>`.\n")
	wantOne(t, check(t, src), "agent", "Write")
	src["agents/loop-helper.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-helper\ndescription: Reads.\n---\n\nRead.\n")}
	wantOne(t, check(t, src), "agent", "tools")
}

func TestGateRefusesAWriteVerbInAnAgent(t *testing.T) {
	src := agentFixture()
	src["agents/loop-helper.md"] = agent("loop-helper", "Read, Bash", "Run `loop work note <item> --body x`.\n")
	wantOne(t, check(t, src), "agent", "work note")
	src["agents/loop-helper.md"] = agent("loop-helper", "Read, Bash", "```bash\nloop fault list && loop inbox answer <entry> --body x\n```\n")
	wantOne(t, check(t, src), "agent", "inbox answer")
}

func TestGateRefusesAVerbTheCoordinatorCannotCallInAnAgent(t *testing.T) {
	src := agentFixture()
	src["agents/loop-helper.md"] = agent("loop-helper", "Read, Bash", "Run `loop pr show`.\n")
	wantOne(t, check(t, src), "agent", "pr show")
}

func TestGateRequiresTheManifestToNameEveryAgent(t *testing.T) {
	src := agentFixture()
	src["agents/loop-stray.md"] = agent("loop-stray", "Read", "Read.\n")
	wantOne(t, check(t, src), "manifest", "agents/loop-stray.md")

	src = agentFixture()
	delete(src, "agents/loop-helper.md")
	wantOne(t, check(t, src), "manifest", "loop-helper")

	// An agent that no runbook lists never runs.
	src = agentFixture()
	src["manifest.json"] = &fstest.MapFile{Data: []byte(`{"runbooks": {"implementer": {"attended": false, "skills": ["loop-sdlc-turn"],
		"policy": "implementer"}}, "skills": ["loop-sdlc-turn"], "agents": ["loop-helper"]}`)}
	wantOne(t, check(t, src), "manifest", "loop-helper")
}

func TestGateRequiresAgentFrontmatterThatNamesTheFile(t *testing.T) {
	src := agentFixture()
	src["agents/loop-helper.md"] = agent("helper", "Read", "Read.\n")
	wantOne(t, check(t, src), "frontmatter", "loop-helper")
}

func TestGateRequiresEachRunbookToNameItsAgents(t *testing.T) {
	src := agentFixture()
	src["runbooks/implementer.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-implementer\ndescription: Build.\n---\n\n" +
		"Read `../loop-sdlc-turn/SKILL.md` first. Read `.loop/policy/implementer.md` when it exists.\n")}
	wantOne(t, check(t, src), "agent", "loop-helper")
}

func TestGateChecksEachPathOfAnAgent(t *testing.T) {
	src := agentFixture()
	src["agents/loop-helper.md"] = agent("loop-helper", "Read", "Read `.claude/skills/loop-sdlc-triage/SKILL.md`.\n")
	wantOne(t, check(t, src), "path", ".claude/skills/loop-sdlc-triage/SKILL.md")
	// A subagent runs at the checkout root and is not told where its
	// definition lives, so a path relative to the agent file names nothing.
	for _, rel := range []string{"../skills/loop-sdlc-turn/SKILL.md", "../loop-sdlc-turn/SKILL.md"} {
		src["agents/loop-helper.md"] = agent("loop-helper", "Read", "Read `"+rel+"`.\n")
		wantOne(t, check(t, src), "path", rel)
	}
}

func TestGateChecksTheVocabularyOfAnAgent(t *testing.T) {
	src := agentFixture()
	src["agents/loop-helper.md"] = agent("loop-helper", "Read", "Run `pnpm board`.\n")
	wantOne(t, check(t, src), "vocabulary", "pnpm")
}

// coordinatorFixture is the valid fixture plus a coordinator runbook
// whose section 12 holds rule.
func coordinatorFixture(rule string) fstest.MapFS {
	src := fixture()
	src["manifest.json"] = &fstest.MapFile{Data: []byte(`{"runbooks": {
		"implementer": {"attended": false, "skills": ["loop-sdlc-turn"], "policy": "implementer"},
		"coordinator": {"attended": true, "skills": ["loop-sdlc-turn"], "policy": "coordinator"}},
		"skills": ["loop-sdlc-turn"]}`)}
	src["runbooks/coordinator.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-coordinator\ndescription: The coordinator.\n---\n\n" +
		"Read `../loop-sdlc-turn/SKILL.md` first. Read `.loop/policy/coordinator.md` when it exists.\n\n" +
		"## 12. Never\n\n- Never decide what the operator must decide.\n" + rule + "\n")}
	return src
}

// The coordinator runbook as it stood before factorio-8qm: section 12
// sent the operator to merge each PRD and spec, and the coordinator handed
// the merge back.
func TestGateRefusesACoordinatorRuleThatHandsAMergeToTheOperator(t *testing.T) {
	wantOne(t, check(t, coordinatorFixture("- Never merge a PRD or a spec. The operator's merge is the sign-off.")),
		"hand-back", "operator's merge")
}

func TestGatePassesACoordinatorRuleThatRecordsTheApproval(t *testing.T) {
	src := coordinatorFixture("- Never sign off a PRD or a spec without the operator's approval. Record that approval with\n" +
		"  `loop inbox answer <entry> --body \"<the operator's words>\"`, and the server merges.")
	if got := check(t, src); len(got) != 0 {
		t.Fatalf("want no findings, got %v", got)
	}
}

func TestGateRefusesEachHandBackInTheCoordinatorsFiles(t *testing.T) {
	for _, tc := range []struct{ file, text, want string }{
		{"runbooks/coordinator.md", "A fault that a release ends is a reason to upgrade. The operator upgrades the host.", "operator upgrades"},
		{"runbooks/coordinator.md", "When both pass, tell the operator to run `gh pr merge 12`.", "gh pr merge"},
		{"runbooks/coordinator.md", "Ask the operator to run `loop inbox answer <entry> --body <text>`.", "loop inbox answer"},
		{"runbooks/coordinator.md", "```bash\ngh pr merge --merge 12\n```", "gh pr merge"},
		// A sentence that wraps across lines is one sentence.
		{"skills/loop-sdlc-turn/SKILL.md", "- **Never merge.** A\n  person merges a PRD or a spec, and that merge is the sign-off.", "person merges"},
	} {
		src := coordinatorFixture("")
		f := src[tc.file]
		f.Data = append(append([]byte{}, f.Data...), []byte("\n"+tc.text+"\n")...)
		got := check(t, src)
		if len(got) != 1 || got[0].Kind != "hand-back" || !strings.Contains(got[0].Detail, tc.want) || got[0].File != tc.file {
			t.Errorf("%q: want one hand-back finding in %s naming %q, got %v", tc.text, tc.file, tc.want, got)
		}
	}
}

// Only the coordinator's own files are held to it: a stage run cannot run
// a host verb, and a room may tell the operator what the operator runs.
func TestGateHoldsOnlyTheCoordinatorsFilesToTheHandBackRule(t *testing.T) {
	src := coordinatorFixture("")
	f := src["runbooks/implementer.md"]
	f.Data = append(append([]byte{}, f.Data...), []byte("\nThe operator upgrades the host.\n")...)
	if got := check(t, src); len(got) != 0 {
		t.Fatalf("want no findings, got %v", got)
	}
}

// A loop verb the coordinator cannot call is the operator's to run.
func TestGatePassesAnOperatorVerbTheCoordinatorCannotCall(t *testing.T) {
	src := coordinatorFixture("The operator runs `loop secret set <name>` for a missing secret.")
	if got := check(t, src); len(got) != 0 {
		t.Fatalf("want no findings, got %v", got)
	}
}

// codexFile is the agents/openai.yaml a flagged skill holds, so that Codex
// leaves the skill out of its list as Claude Code does.
const codexFile = "policy:\n  allow_implicit_invocation: false\n"

// flaggedFixture is the valid fixture plus loop-flagged: a skill that holds
// the flag and the Codex file, and that the implementer runbook lists and
// names by its path.
func flaggedFixture() fstest.MapFS {
	src := fixture()
	src["manifest.json"] = &fstest.MapFile{Data: []byte(`{"runbooks": {"implementer": {"attended": false,
		"skills": ["loop-sdlc-turn", "loop-flagged"], "policy": "implementer"}}, "skills": ["loop-sdlc-turn", "loop-flagged"]}`)}
	src["runbooks/implementer.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-implementer\ndescription: Build one ticket.\n---\n\n" +
		"Read `../loop-sdlc-turn/SKILL.md` first. Read `.loop/policy/implementer.md` when it exists.\n" +
		"Read `../loop-flagged/SKILL.md` before you commit.\n")}
	src["skills/loop-flagged/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-flagged\ndescription: One step.\n" +
		"disable-model-invocation: true\n---\n\nDo the step.\n")}
	src["skills/loop-flagged/agents/openai.yaml"] = &fstest.MapFile{Data: []byte(codexFile)}
	return src
}

func TestFlaggedSkillNeedsAReader(t *testing.T) {
	if got := check(t, flaggedFixture()); len(got) != 0 {
		t.Fatalf("a flagged skill that a runbook lists: want no findings, got %v", got)
	}

	// No runbook lists it, so no run is ever told to read it.
	src := flaggedFixture()
	src["manifest.json"] = &fstest.MapFile{Data: []byte(`{"runbooks": {"implementer": {"attended": false,
		"skills": ["loop-sdlc-turn"], "policy": "implementer"}}, "skills": ["loop-sdlc-turn", "loop-flagged"]}`)}
	wantOne(t, check(t, src), "manifest", "loop-flagged")

	// Codex ignores the flag, so the flag alone lists the skill in each Codex run.
	src = flaggedFixture()
	delete(src, "skills/loop-flagged/agents/openai.yaml")
	wantOne(t, check(t, src), "manifest", "loop-flagged")

	src = flaggedFixture()
	src["skills/loop-flagged/agents/openai.yaml"] = &fstest.MapFile{Data: []byte("policy:\n  allow_implicit_invocation: true\n")}
	wantOne(t, check(t, src), "manifest", "loop-flagged")

	// The Codex file alone hides from Codex a skill that Claude Code lists.
	src = flaggedFixture()
	src["skills/loop-flagged/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: loop-flagged\ndescription: One step.\n---\n\nDo the step.\n")}
	wantOne(t, check(t, src), "manifest", "loop-flagged")
}

func TestEncodeLessonsIsFlaggedAndStillRead(t *testing.T) {
	raw, err := fs.ReadFile(Source(), "skills/loop-encode-lessons/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if fields, why := frontFields(string(raw)); why != "" || fields["disable-model-invocation"] != "true" {
		t.Errorf("loop-encode-lessons front matter: want disable-model-invocation: true, got %v %s", fields, why)
	}
	codex, err := fs.ReadFile(Source(), "skills/loop-encode-lessons/agents/openai.yaml")
	if err != nil || string(codex) != codexFile {
		t.Errorf("loop-encode-lessons/agents/openai.yaml: want %q, got %q (%v)", codexFile, codex, err)
	}
	m, err := ReadManifest(Source())
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"amender", "coordinator"} {
		if !slices.Contains(m.Runbooks[stage].Skills, "loop-encode-lessons") {
			t.Errorf("the %s runbook does not list loop-encode-lessons", stage)
		}
	}
}
