package pack_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/room"
	"github.com/iantanwx/loop/internal/sched"
)

// registry is the schema the binary prints: the surface plus the refusal
// codes of the client-side host verbs, as pack-check builds it.
func registry(t *testing.T) *cli.Registry {
	t.Helper()
	reg := cli.Surface()
	for _, c := range release.Refusals() {
		if err := reg.RegisterRefusal(c); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

func TestShippedPackPassesTheGate(t *testing.T) {
	got, err := pack.Check(pack.Source(), registry(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got {
		t.Error(f)
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	raw, err := fs.ReadFile(pack.Source(), name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTurnSkillRunsTheSameCommandAgainOnExit4(t *testing.T) {
	turn := read(t, "skills/loop-sdlc-turn/SKILL.md")
	if !strings.Contains(turn, "**On exit 4, run the same command again.**") {
		t.Fatal("the turn skill does not tell a run to repeat the same command on exit 4 (KTD13)")
	}
}

// turnSection returns section n of the turn skill, from its heading to the
// next one.
func turnSection(t *testing.T, n string) string {
	t.Helper()
	turn := read(t, "skills/loop-sdlc-turn/SKILL.md")
	_, rest, ok := strings.Cut(turn, "\n## "+n+". ")
	if !ok {
		t.Fatalf("the turn skill has no section %s", n)
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	return section
}

// The shell discipline of alpha's docs/rules/shell.md (parity 13.5): a run
// names another checkout by its path, and stops at the first failed step of
// a command that publishes.
func TestTurnSkillAddressesEachCheckoutByItsPath(t *testing.T) {
	work := turnSection(t, "4")
	for _, want := range []string{"`test -d \"$W\" || exit 1`", "`git -C \"$W\"`", "Never write `cd \"$W\" &&`"} {
		if !strings.Contains(work, want) {
			t.Errorf("section 4 of the turn skill does not hold %s", want)
		}
	}
}

func TestTurnSkillChecksEachStepBeforeAPublish(t *testing.T) {
	work := turnSection(t, "4")
	for _, want := range []string{"`|| exit 1`", "`git commit -F <file>`"} {
		if !strings.Contains(work, want) {
			t.Errorf("section 4 of the turn skill does not hold %s", want)
		}
	}
}

// TestEveryStageHasARunbook holds the manifest to the stages the server
// starts: each run stage, the coordinator and each room kind. The intake
// stage only counts epics against its cap (KTD30). An intake room serves
// it.
func TestEveryStageHasARunbook(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, s := range config.RunStages() {
		if s != "intake" {
			want = append(want, s)
		}
	}
	want = append(want, sched.CoordinatorStage)
	for _, k := range room.Kinds {
		want = append(want, room.Stage(k))
	}
	var got []string
	for s := range m.Runbooks {
		got = append(got, s)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("runbooks = %v, want one for each stage the server starts: %v", got, want)
	}
	for _, k := range room.Kinds {
		if !m.Runbooks[room.Stage(k)].Attended {
			t.Errorf("room %s is attended, and its runbook says it is not", k)
		}
	}
	if !m.Runbooks[sched.CoordinatorStage].Attended {
		t.Error("the coordinator is attended, and its runbook says it is not")
	}
}

func TestInjectionWritesEveryRunbookAndSkillForEachHarness(t *testing.T) {
	p, err := pack.Brief()
	if err != nil {
		t.Fatal(err)
	}
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []agenthost.Harness{agenthost.Claude, agenthost.Codex} {
		dir := t.TempDir()
		if err := p.Write(dir, h); err != nil {
			t.Fatal(err)
		}
		for stage := range m.Runbooks {
			path := filepath.Join(dir, filepath.FromSlash(brief.Runbook(h, stage)))
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: the runbook of %s is not where the brief names it: %v", h, stage, err)
			}
			if !strings.Contains(string(body), "name: loop-"+stage+"\n") {
				t.Errorf("%s: %s holds another runbook", h, path)
			}
		}
		for _, s := range m.Skills {
			if _, err := os.Stat(filepath.Join(dir, brief.SkillRoot(h), s, "SKILL.md")); err != nil {
				t.Errorf("%s: skill %s is missing: %v", h, s, err)
			}
		}
		root := brief.AgentRoot(h)
		for _, a := range m.Agents {
			body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(root), a+".md"))
			switch {
			case root == "":
				if err == nil {
					t.Errorf("%s: a harness with no agent directory received agent %s", h, a)
				}
			case err != nil:
				t.Errorf("%s: agent %s is missing: %v", h, a, err)
			case !strings.Contains(string(body), "name: "+a+"\n"):
				t.Errorf("%s: %s.md holds another agent", h, a)
			}
		}
	}
	if brief.AgentRoot(agenthost.Codex) != "" {
		t.Errorf("codex agent root = %q, want none", brief.AgentRoot(agenthost.Codex))
	}
	if brief.AgentRoot(agenthost.Claude) != ".claude/agents" {
		t.Errorf("claude agent root = %q, want .claude/agents", brief.AgentRoot(agenthost.Claude))
	}
	want := map[agenthost.Harness]string{agenthost.Claude: ".claude/skills/", agenthost.Codex: ".agents/skills/"}
	for h, prefix := range want {
		if !strings.HasPrefix(brief.Runbook(h, "implementer"), prefix+"loop-implementer/") {
			t.Errorf("%s runbook = %s, want it under %s", h, brief.Runbook(h, "implementer"), prefix)
		}
	}
	if _, err := p.Skills(); err != nil {
		t.Fatalf("the laid-out pack holds an entry outside the loop- namespace: %v", err)
	}
}

// TestShippedTriagePasses holds the shipped rubric to the corpus and the
// server's literal, as pack-check does.
func TestShippedTriagePasses(t *testing.T) {
	corpus, err := os.ReadFile(filepath.Join("..", "..", pack.CorpusPath))
	if err != nil {
		t.Fatal(err)
	}
	got, err := pack.CheckTriage(pack.Source(), corpus)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got {
		t.Error(f)
	}
}

func TestEachPolicyHasATemplate(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	for stage, rb := range m.Runbooks {
		if rb.Policy == "" {
			continue
		}
		if _, err := fs.Stat(pack.Source(), pack.PolicyTemplate(rb.Policy)); err != nil {
			t.Errorf("runbook %s names policy %s, and loop-init holds no template: %v", stage, rb.Policy, err)
		}
	}
}

func TestLayoutRefusesTwoSourcesForOneDirectory(t *testing.T) {
	src := fstest.MapFS{
		"manifest.json":                  {Data: []byte(`{"runbooks": {"sdlc-turn": {"skills": []}}, "skills": ["loop-sdlc-turn"]}`)},
		"runbooks/sdlc-turn.md":          {Data: []byte("x")},
		"skills/loop-sdlc-turn/SKILL.md": {Data: []byte("y")},
	}
	if _, err := pack.Layout(src); err == nil || !strings.Contains(err.Error(), "loop-sdlc-turn") {
		t.Fatalf("want a refusal that names loop-sdlc-turn, got %v", err)
	}
}

// TestCoordinatorShipsItsReadOnlySubagents holds the pack to the four
// named helpers of the coordinator (runbook section 11): each one ships,
// the coordinator's runbook lists it, and the release injects it.
func TestCoordinatorShipsItsReadOnlySubagents(t *testing.T) {
	want := []string{"loop-alert-root-cause", "loop-checkout-forensics", "loop-doctor", "loop-escalation-prep"}
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(slices.Values(m.Agents)); !slices.Equal(got, want) {
		t.Fatalf("manifest agents = %v, want %v", got, want)
	}
	if got := slices.Sorted(slices.Values(m.Runbooks[sched.CoordinatorStage].Agents)); !slices.Equal(got, want) {
		t.Fatalf("coordinator agents = %v, want %v", got, want)
	}
	p, err := pack.Brief()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.AgentNames()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("injected agents = %v, want %v", got, want)
	}
}

// tickertownCommit is the tickertown commit each ported skill names as its
// source.
const tickertownCommit = "64e293f9ae2db7831d12462e76ffe1aa3deacd8c"

// TestPortedSkillsShipWithTheirReaders proves C3 and C4 of the tickertown
// skills spec: the pack holds exactly the listed skills, each writing and
// code skill is flagged, carries the Codex file and names its source, and
// the runbooks that list it are its row of the spec's reader table.
func TestPortedSkillsShipWithTheirReaders(t *testing.T) {
	want := []string{
		"loop-boundaries", "loop-brainstorm", "loop-bro", "loop-commit", "loop-domain-modeling",
		"loop-encode-lessons", "loop-fault", "loop-init", "loop-least-code", "loop-reader-load", "loop-reflect",
		"loop-sdlc-documents", "loop-sdlc-glossary", "loop-sdlc-review", "loop-sdlc-split", "loop-sdlc-triage",
		"loop-sdlc-turn", "loop-test-behavior-not-implementation", "loop-unslop", "loop-verifiable-units",
	}
	dirs, err := fs.ReadDir(pack.Source(), "skills")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range dirs {
		got = append(got, d.Name())
	}
	if slices.Sort(got); !slices.Equal(got, want) {
		t.Errorf("pack skills = %v, want %v", got, want)
	}

	readers := map[string][]string{
		"loop-commit": {"amender", "implementer", "intake-room", "review-room", "spec-author"},
		"loop-bro":    {"decomposer"},
		"loop-unslop": {"amender", "decomposer", "intake-room", "spec-author"},

		"loop-boundaries":                       {"code-reviewer", "implementer"},
		"loop-least-code":                       {"code-reviewer", "implementer"},
		"loop-reader-load":                      {"code-reviewer", "implementer"},
		"loop-verifiable-units":                 {"implementer"},
		"loop-test-behavior-not-implementation": {"code-reviewer", "implementer", "reproducer"},
		"loop-domain-modeling":                  {"doc-reviewer", "implementer", "spec-author"},
	}
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	for skill, stages := range readers {
		source := "skills/" + strings.TrimPrefix(skill, "loop-") + "/SKILL.md"
		text := read(t, "skills/"+skill+"/SKILL.md")
		head, _, _ := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
		if !slices.Contains(strings.Split(head, "\n"), "disable-model-invocation: true") {
			t.Errorf("%s front matter holds no disable-model-invocation: true", skill)
		}
		if codex := read(t, "skills/"+skill+"/agents/openai.yaml"); codex != "policy:\n  allow_implicit_invocation: false\n" {
			t.Errorf("%s/agents/openai.yaml = %q", skill, codex)
		}
		comment, _, _ := strings.Cut(text, "-->")
		if !strings.Contains(comment, "<!--") || !strings.Contains(comment, source) || !strings.Contains(comment, tickertownCommit) {
			t.Errorf("%s holds no comment that names %s at tickertown commit %s", skill, source, tickertownCommit)
		}
		if !slices.Contains(m.Skills, skill) {
			t.Errorf("the manifest skills list does not hold %s", skill)
		}
		var listed []string
		for stage, rb := range m.Runbooks {
			if slices.Contains(rb.Skills, skill) {
				listed = append(listed, stage)
			}
		}
		if slices.Sort(listed); !slices.Equal(listed, stages) {
			t.Errorf("runbooks that list %s = %v, want %v", skill, listed, stages)
		}
	}
}
