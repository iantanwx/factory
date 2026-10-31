package pack_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
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
	p, _, err := pack.Brief(nil)
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
	p, _, err := pack.Brief(nil)
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

// fixtureSkill is a fetched skill as upstream ships it: a front matter
// with its upstream name, a body that names its own path, and one
// executable file.
func fixtureSkill(name string) fstest.MapFS {
	return fstest.MapFS{
		name + "/SKILL.md": {Data: []byte("---\nname: " + name + "\ndescription: A fixture.\n---\n\n" +
			"Run `.agents/skills/" + name + "/scripts/" + name + "`.\nname: " + name + "\n")},
		name + "/scripts/" + name:        {Data: []byte("#!/bin/sh\necho " + name + "\n"), Mode: 0o755},
		name + "/reference/notes.md":     {Data: []byte("# Notes\r\n\r\nname: " + name + "\r\n"), Mode: 0o644},
		name + "/reference/deep/more.md": {Data: []byte("more\n"), Mode: 0o600},
	}
}

// fixtureSource is a pack source with one runbook, one first-party skill
// and one pin for each name. A pin's tree hash is that of the same name's
// directory in hashed; a name hashed does not hold gets a hash of nothing.
func fixtureSource(t *testing.T, hashed fs.FS, names ...string) fstest.MapFS {
	t.Helper()
	var pins []string
	for _, n := range names {
		sum := strings.Repeat("0", 64)
		if _, err := fs.Stat(hashed, n); err == nil {
			sub, err := fs.Sub(hashed, n)
			if err != nil {
				t.Fatal(err)
			}
			if sum, err = pack.TreeHash(sub); err != nil {
				t.Fatal(err)
			}
		}
		pins = append(pins, fmt.Sprintf(`{"name": %q, "repo": "acme/%s", "commit": %q, "path": "", "sha256": %q}`,
			n, n, strings.Repeat("a", 40), sum))
	}
	return fstest.MapFS{
		"manifest.json":                  {Data: []byte(`{"runbooks": {"implementer": {"skills": ["loop-sdlc-turn"]}}, "skills": ["loop-sdlc-turn"]}`)},
		"runbooks/implementer.md":        {Data: []byte("---\nname: loop-implementer\n---\n")},
		"skills/loop-sdlc-turn/SKILL.md": {Data: []byte("---\nname: loop-sdlc-turn\n---\n")},
		pack.ThirdPartyFile:              {Data: []byte(`{"skills": [` + strings.Join(pins, ", ") + `]}`)},
	}
}

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return dir
}

// C11: a run receives each verified skill as loop-<name>. Its SKILL.md
// differs from the fetched file in the one front matter name line, every
// other file is byte for byte as fetched, an executable file is 0755 and
// every other file 0644, and git ignores the directory.
func TestARunReceivesEachVerifiedSkillRenamedAndOtherwiseUntouched(t *testing.T) {
	fetched := fixtureSkill("impeccable")
	src := fixtureSource(t, fetched, "impeccable")
	p, left, err := pack.BriefOf(src, fetched)
	if err != nil || len(left) != 0 {
		t.Fatalf("left out %v, %v", left, err)
	}
	for _, h := range []agenthost.Harness{agenthost.Claude, agenthost.Codex} {
		dir := gitInit(t)
		_, err := brief.Inject(context.Background(), brief.Injection{Checkout: dir, Harness: h, Pack: p,
			Brief: brief.Brief{RunID: "r-1", Project: "alpha", Stage: "implementer", Role: "implementer"}})
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, brief.SkillRoot(h), "loop-impeccable")
		modes := map[string]os.FileMode{
			"SKILL.md": 0o644, "scripts/impeccable": 0o755, "reference/notes.md": 0o644, "reference/deep/more.md": 0o644,
		}
		var got []string
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			got = append(got, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := slices.Sorted(maps.Keys(modes)); !slices.Equal(got, want) {
			t.Fatalf("%s: loop-impeccable holds %v, want %v", h, got, want)
		}
		for rel, mode := range modes {
			body, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			want := fetched["impeccable/"+rel].Data
			if rel == "SKILL.md" {
				want = []byte("---\nname: loop-impeccable\ndescription: A fixture.\n---\n\n" +
					"Run `.agents/skills/impeccable/scripts/impeccable`.\nname: impeccable\n")
			}
			if string(body) != string(want) {
				t.Errorf("%s: %s = %q, want %q", h, rel, body, want)
			}
			fi, err := os.Stat(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != mode {
				t.Errorf("%s: %s is mode %o, want %o", h, rel, fi.Mode().Perm(), mode)
			}
		}
		exclude, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
		if err != nil {
			t.Fatal(err)
		}
		if line := "/" + brief.SkillRoot(h) + "/loop-impeccable/"; !slices.Contains(strings.Split(string(exclude), "\n"), line) {
			t.Errorf("%s: info/exclude does not list %s:\n%s", h, line, exclude)
		}
		if out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=all").Output(); err != nil || len(out) != 0 {
			t.Fatalf("%s: git sees the skills: %q %v", h, out, err)
		}
	}
}

// C12: the pack hash covers each fetched file's bytes and executable bit,
// and the brief records the hash of the pack Brief returned.
func TestThePackHashCoversTheFetchedSkills(t *testing.T) {
	hash := func(fetched fstest.MapFS) string {
		t.Helper()
		p, left, err := pack.BriefOf(fixtureSource(t, fetched, "impeccable"), fetched)
		if err != nil || len(left) != 0 {
			t.Fatalf("left out %v, %v", left, err)
		}
		h, err := p.Hash()
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	base := hash(fixtureSkill("impeccable"))
	bytesChanged := fixtureSkill("impeccable")
	bytesChanged["impeccable/reference/notes.md"].Data = []byte("other notes\n")
	if hash(bytesChanged) == base {
		t.Error("a fetched file's new bytes kept the pack hash")
	}
	modeChanged := fixtureSkill("impeccable")
	modeChanged["impeccable/reference/notes.md"].Mode = 0o755
	if hash(modeChanged) == base {
		t.Error("a fetched file's new executable bit kept the pack hash")
	}

	fetched := fixtureSkill("impeccable")
	p, _, err := pack.BriefOf(fixtureSource(t, fetched, "impeccable"), fetched)
	if err != nil {
		t.Fatal(err)
	}
	dir := gitInit(t)
	res, err := brief.Inject(context.Background(), brief.Injection{Checkout: dir, Harness: agenthost.Claude, Pack: p,
		Brief: brief.Brief{RunID: "r-1", Project: "alpha", Stage: "implementer", Role: "implementer"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(cli.BriefJSONFile)))
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		PackHash string `json:"pack_hash"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if b.PackHash != base || res.PackHash != base {
		t.Fatalf("brief pack_hash %s, inject %s, want Pack.Hash %s", b.PackHash, res.PackHash, base)
	}
}

// C13: a pin whose directory is missing, and one whose tree fails its
// hash, are left out and returned. The pack holds every other pin and
// every first-party skill.
func TestBriefLeavesOutAMissingOrWrongSkill(t *testing.T) {
	fetched := fixtureSkill("widget")
	maps.Copy(fetched, fixtureSkill("gadget"))
	src := fixtureSource(t, fetched, "widget", "gadget", "gizmo")
	fetched["gadget/scripts/gadget"].Mode = 0o644 // the tree no longer matches its pin
	p, left, err := pack.BriefOf(src, fetched)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, pin := range left {
		names = append(names, pin.Name)
	}
	if want := []string{"gadget", "gizmo"}; !slices.Equal(names, want) {
		t.Fatalf("left out %v, want %v", names, want)
	}
	skills, err := p.Skills()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"loop-implementer", "loop-sdlc-turn", "loop-widget"}; !slices.Equal(skills, want) {
		t.Fatalf("pack skills = %v, want %v", skills, want)
	}
	// With no fetched set, every pin is left out.
	if _, left, err := pack.BriefOf(src, nil); err != nil || len(left) != 3 {
		t.Fatalf("no fetched set left out %v, %v", left, err)
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

// C19: the kernel pins asd-ste100 and impeccable, and no other skill.
func TestC19TheKernelPinsASDAndImpeccableOnly(t *testing.T) {
	var names []string
	for _, p := range pack.ThirdPartyPins() {
		names = append(names, p.Name)
	}
	if want := []string{"asd-ste100", "impeccable"}; !slices.Equal(names, want) {
		t.Fatalf("the kernel pins %v, want %v", names, want)
	}
}

// C19: a run of a project with no skills: receives the kernel pins from a
// fetched set that holds a tree for each of them. It holds loop-asd-ste100
// and loop-impeccable, and no Neon skill under either root.
func TestC19ARunHoldsNoNeonSkill(t *testing.T) {
	src := fstest.MapFS{}
	err := fs.WalkDir(pack.Source(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(pack.Source(), path)
		src[path] = &fstest.MapFile{Data: data, Mode: 0o644}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// The fetched set holds a fixture tree for each embedded pin, and the
	// source pins each tree by its hash, so every embedded pin reaches the run.
	fetched := fstest.MapFS{}
	pins := pack.ThirdPartyPins()
	for i, p := range pins {
		maps.Copy(fetched, fixtureSkill(p.Name))
		sub, err := fs.Sub(fetched, p.Name)
		if err != nil {
			t.Fatal(err)
		}
		if pins[i].SHA256, err = pack.TreeHash(sub); err != nil {
			t.Fatal(err)
		}
	}
	file, err := json.Marshal(map[string][]pack.ThirdParty{"skills": pins})
	if err != nil {
		t.Fatal(err)
	}
	src[pack.ThirdPartyFile] = &fstest.MapFile{Data: file}
	p, left, err := pack.BriefOf(src, fetched)
	if err != nil || len(left) != 0 {
		t.Fatalf("left out %v, %v", left, err)
	}
	for _, h := range []agenthost.Harness{agenthost.Claude, agenthost.Codex} {
		dir := gitInit(t)
		_, err := brief.Inject(context.Background(), brief.Injection{Checkout: dir, Harness: h, Pack: p,
			Brief: brief.Brief{RunID: "r-1", Project: "alpha", Stage: "implementer", Role: "implementer"}})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"loop-asd-ste100", "loop-impeccable"} {
			if _, err := os.Stat(filepath.Join(dir, brief.SkillRoot(h), name, "SKILL.md")); err != nil {
				t.Errorf("%s: a run holds no %s: %v", h, name, err)
			}
		}
		for _, root := range []string{".claude/skills", ".agents/skills"} {
			for _, name := range []string{"loop-neon", "loop-neon-postgres", "neon", "neon-postgres"} {
				if _, err := os.Stat(filepath.Join(dir, root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s: a run holds %s/%s (%v)", h, root, name, err)
				}
			}
		}
	}
}
