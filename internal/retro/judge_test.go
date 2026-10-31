package retro_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/retro"
)

// prompts records every prompt each fake judge reads. The verb asks both
// judges at once.
type prompts struct {
	mu      sync.Mutex
	byJudge map[string][]string
}

func (p *prompts) of(judge string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byJudge[judge]
}

// fakeJudge answers each prompt with reply(prompt), in Claude Code's JSON
// envelope, and records the prompt.
type fakeJudge struct {
	name  string
	reply func(prompt string) string
	log   *prompts
}

func (f fakeJudge) Name() string { return f.name }

func (f fakeJudge) Ask(_ context.Context, prompt string) (string, error) {
	f.log.mu.Lock()
	f.log.byJudge[f.name] = append(f.log.byJudge[f.name], prompt)
	f.log.mu.Unlock()
	env, err := json.Marshal(map[string]string{"result": "I read the record.\n" + f.reply(prompt)})
	return string(env), err
}

type fakeJev struct {
	keys   []string
	panels []escalation.Panel
}

func (f *fakeJev) Score(_ context.Context, p escalation.Panel, key string) (*escalation.Reading, error) {
	f.keys = append(f.keys, key)
	f.panels = append(f.panels, p)
	return sureReading(), nil
}

// judgeCheckout is a checkout that holds the pack's retro rubric, the
// pack's triage rubric changed by triage, and Jev's key.
func judgeCheckout(t *testing.T, triage func(string) string) string {
	t.Helper()
	dir := t.TempDir()
	for skill, change := range map[string]func(string) string{"loop-retro-rubric": nil, "loop-sdlc-triage": triage} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "pack", "skills", skill, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if change != nil {
			text = change(text)
		}
		into := filepath.Join(dir, ".claude", "skills", skill)
		if err := os.MkdirAll(into, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(into, "SKILL.md"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(escalation.JevKeyVar+"='jev-key'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func judging(dir string, jev *fakeJev, loopProject bool, record func(context.Context) (retro.Record, error), judges ...escalation.Judge) retro.Judging {
	return retro.Judging{Judges: judges, Jev: jev, Record: record,
		Getwd: func() (string, error) { return dir, nil }, FindCheckout: func(string) (string, error) { return dir, nil },
		LoopProject: func(context.Context, string) bool { return loopProject }}
}

func judgeCall(t *testing.T, j retro.Judging, dir, learnings, out string) cli.Result {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "learnings.json"), []byte(learnings), 0o600); err != nil {
		t.Fatal(err)
	}
	v, ok := cli.Surface().Verb("retro judge")
	if !ok {
		t.Fatal("the surface has no retro judge verb")
	}
	flags := map[string]string{"file": "learnings.json"}
	if out != "" {
		flags["out"] = out
	}
	return retro.JudgeVerb(j)(context.Background(), cli.Invocation{Verb: v, Flags: flags})
}

// replyFor answers project_policy, two_or_more, edit, with a reason that
// names the judge and the learning, so a prompt that leaks it shows.
func replyFor(judge string) func(string) string {
	return func(prompt string) string {
		n := "2"
		if strings.Contains(prompt, "race tests") {
			n = "1"
		}
		return `{"target":"project_policy","evidence":"two_or_more","change":"edit","reason":"` + judge + ` reason ` + n + `"}`
	}
}

// C23. Each judge's prompt holds the rubric, the learning and its cases,
// and no other judge's name or answer. The panel file that the verb writes
// parses in `loop retro route` with every answer and the reading unchanged.
func TestC23JudgesAnswerAloneAndThePanelCrossesIntact(t *testing.T) {
	w := newWorld(t)
	w.retroOf("prj", "r-retro", "r-a1", "r-a2")
	r, _, err := retro.Bind(context.Background(), w.st, "prj", "r-retro")
	if err != nil {
		t.Fatal(err)
	}
	rec, ref := retro.BuildRecord(context.Background(), retro.World{Store: w.st, Machine: w.m}, r)
	if ref != nil {
		t.Fatal(ref)
	}
	texts := map[retro.CaseRef]string{}
	for _, c := range rec.Item.Runs {
		texts[c.Ref] = c.Text
	}
	if texts["run:r-a1"] == "" || texts["run:r-a2"] == "" {
		t.Fatalf("the record holds runs %+v", rec.Item.Runs)
	}

	dir := judgeCheckout(t, nil)
	log := &prompts{byJudge: map[string][]string{}}
	jev := &fakeJev{}
	j := judging(dir, jev, false, func(context.Context) (retro.Record, error) { return rec, nil },
		fakeJudge{"judge-amber", replyFor("judge-amber"), log}, fakeJudge{"judge-birch", replyFor("judge-birch"), log})
	out := filepath.Join(dir, "panel.json")
	res := judgeCall(t, j, dir, `{"learnings": [
		{"statement": "The implementer runs the race tests before it pushes", "cases": ["run:r-a1", "run:r-a2"]},
		{"statement": "The policy names the lint command", "cases": ["run:r-a2", "learning:L-9"], "extends": "L-9"}]}`, out)
	if res.Refusal != nil {
		t.Fatalf("judge refused: %+v", res.Refusal)
	}
	if got := res.Data.(map[string]any)["panel_file"]; got != out {
		t.Fatalf("panel_file = %v, want %s", got, out)
	}

	rubric, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", "loop-retro-rubric", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{"The implementer runs the race tests before it pushes", "The policy names the lint command"}
	cases := [][]retro.CaseRef{{"run:r-a1", "run:r-a2"}, {"run:r-a2", "learning:L-9"}}
	leaks := []string{"judge-amber", "judge-birch", "reason 1", "reason 2", "jev-key"}
	for _, judge := range []string{"judge-amber", "judge-birch"} {
		ps := log.of(judge)
		if len(ps) != 2 {
			t.Fatalf("%s read %d prompts, want 2", judge, len(ps))
		}
		for _, p := range ps {
			i := 0
			if !strings.Contains(p, statements[0]) {
				i = 1
			}
			if !strings.Contains(p, statements[i]) || strings.Contains(p, statements[1-i]) || !strings.Contains(p, string(rubric)) {
				t.Fatalf("%s's prompt holds the wrong learning or no rubric:\n%s", judge, p)
			}
			for _, c := range cases[i] {
				if !strings.Contains(p, string(c)) || !strings.Contains(p, texts[c]) {
					t.Fatalf("%s's prompt lacks case %s or its text %q:\n%s", judge, c, texts[c], p)
				}
			}
			for _, leak := range leaks {
				if strings.Contains(p, leak) {
					t.Fatalf("%s's prompt names %q:\n%s", judge, leak, p)
				}
			}
		}
	}
	if len(jev.panels) != 2 || len(jev.keys) != 2 || jev.keys[0] != "jev-key" || len(jev.panels[0].Judges) != 2 {
		t.Fatalf("Jev read %+v with keys %v", jev.panels, jev.keys)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	panels, err := retro.ParsePanels(raw)
	if err != nil {
		t.Fatal(err)
	}
	pair := func(n string) []retro.Answer {
		return []retro.Answer{
			{Judge: "judge-amber", Target: retro.ProjectPolicy, Evidence: retro.TwoOrMore, Change: retro.Edit, Reason: "judge-amber reason " + n},
			{Judge: "judge-birch", Target: retro.ProjectPolicy, Evidence: retro.TwoOrMore, Change: retro.Edit, Reason: "judge-birch reason " + n},
		}
	}
	want := []retro.Panel{
		{Learning: retro.Learning{Statement: statements[0], Cases: cases[0]}, Answers: pair("1"), Reading: sureReading()},
		{Learning: retro.Learning{Statement: statements[1], Cases: cases[1], Extends: "L-9"}, Answers: pair("2"), Reading: sureReading()},
	}
	if !reflect.DeepEqual(panels, want) {
		t.Fatalf("the panel file reads\n%+v\nwant\n%+v", panels, want)
	}
}

// C23 with the route: the first learning's panel, as the verb wrote it, is
// a file that `loop retro route` accepts, and it lands.
func TestC23RouteAcceptsThePanelFileTheVerbWrites(t *testing.T) {
	w := newWorld(t)
	w.retroOf("prj", "r-retro", "r-a1", "r-a2")
	dir := judgeCheckout(t, nil)
	log := &prompts{byJudge: map[string][]string{}}
	j := judging(dir, &fakeJev{}, false, nil,
		fakeJudge{"judge-amber", replyFor("judge-amber"), log}, fakeJudge{"judge-birch", replyFor("judge-birch"), log})
	out := filepath.Join(dir, "panel.json")
	res := judgeCall(t, j, dir, `{"learnings": [{"statement": "The implementer runs the race tests before it pushes", "cases": ["run:r-a1", "run:r-a2"]}]}`, out)
	if res.Refusal != nil {
		t.Fatalf("judge refused: %+v", res.Refusal)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	routed := w.route("prj", "r-retro", string(raw))
	if routed.Refusal != nil {
		t.Fatalf("route refused the panel file: %+v", routed.Refusal)
	}
	got, err := json.Marshal(routed.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"route":"land"`) || !strings.Contains(string(got), `"target":"project_policy"`) {
		t.Fatalf("route answered %s, want one land at project_policy", got)
	}
}

// C23. A triage rubric whose thresholds differ from the server's stops
// the verb before any judge reads a prompt.
func TestC23RubricDriftRefuses(t *testing.T) {
	dir := judgeCheckout(t, func(s string) string {
		return strings.Replace(s, "| `convergence` | 0.85 |", "| `convergence` | 0.70 |", 1)
	})
	log := &prompts{byJudge: map[string][]string{}}
	j := judging(dir, &fakeJev{}, false, nil,
		fakeJudge{"judge-amber", replyFor("judge-amber"), log}, fakeJudge{"judge-birch", replyFor("judge-birch"), log})
	res := judgeCall(t, j, dir, `{"learnings": [{"statement": "s", "cases": ["run:r-a1", "run:r-a2"]}]}`, "")
	if res.Refusal == nil || res.Refusal.Code != cli.CodeUsage || res.Refusal.Rule != "rubric_drift" {
		t.Fatalf("judge answered %+v, want USAGE rubric_drift", res)
	}
	if len(log.of("judge-amber"))+len(log.of("judge-birch")) != 0 {
		t.Fatal("a judge read a prompt against a drifted rubric")
	}
}

// C23. A learning with no case is refused before any judge reads it.
func TestC23ALearningWithNoCaseRefusesUsage(t *testing.T) {
	dir := judgeCheckout(t, nil)
	log := &prompts{byJudge: map[string][]string{}}
	j := judging(dir, &fakeJev{}, false, nil,
		fakeJudge{"judge-amber", replyFor("judge-amber"), log}, fakeJudge{"judge-birch", replyFor("judge-birch"), log})
	res := judgeCall(t, j, dir, `{"learnings": [{"statement": "s", "cases": ["run:r-a1"]}, {"statement": "t", "cases": []}]}`, "")
	if res.Refusal == nil || res.Refusal.Code != cli.CodeUsage {
		t.Fatalf("judge answered %+v, want USAGE", res)
	}
	if len(log.of("judge-amber"))+len(log.of("judge-birch")) != 0 {
		t.Fatal("a judge read a prompt of a file that holds a learning with no case")
	}
}

// C18. Outside the loop's own project, each judge's prompt states that
// pack_pr is not an answer. In the loop's own project, no prompt says so.
func TestC18PromptOutsideTheLoopProjectRulesOutPackPR(t *testing.T) {
	const rule = "`pack_pr` is not an answer"
	for _, loopProject := range []bool{false, true} {
		dir := judgeCheckout(t, nil)
		log := &prompts{byJudge: map[string][]string{}}
		j := judging(dir, &fakeJev{}, loopProject, nil,
			fakeJudge{"judge-amber", replyFor("judge-amber"), log}, fakeJudge{"judge-birch", replyFor("judge-birch"), log})
		if res := judgeCall(t, j, dir, `{"learnings": [{"statement": "s", "cases": ["run:r-a1", "run:r-a2"]}]}`, ""); res.Refusal != nil {
			t.Fatal(res.Refusal)
		}
		for _, judge := range []string{"judge-amber", "judge-birch"} {
			for _, p := range log.of(judge) {
				if strings.Contains(p, rule) == loopProject {
					t.Fatalf("loop project %v: %s's prompt reads:\n%s", loopProject, judge, p)
				}
			}
		}
	}
}

// C18. A checkout whose origin is the loop's repository is the loop's own
// project, in either URL form. Any other origin, or none, is not.
func TestC18LoopCheckoutReadsTheOrigin(t *testing.T) {
	for url, want := range map[string]bool{
		"git@github.com:slothware-labs/factorio.git":   true,
		"https://github.com/Slothware-Labs/factorio":   true,
		"https://github.com/slothware-labs/factorio-x": false,
		"git@github.com:o/r.git":                       false,
		"":                                             false,
	} {
		dir := t.TempDir()
		git := func(args ...string) {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
		git("init", "-q")
		if url != "" {
			git("remote", "add", "origin", url)
		}
		if got := retro.LoopCheckout(context.Background(), dir); got != want {
			t.Fatalf("LoopCheckout with origin %q = %v, want %v", url, got, want)
		}
	}
}
