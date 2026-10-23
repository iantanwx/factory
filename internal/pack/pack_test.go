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
		if _, err := fs.Stat(pack.Source(), "skills/loop-init/policy/"+rb.Policy+".md"); err != nil {
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
