package escalation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

type fakeJudge struct {
	name, reply string
	prompts     *[]string
}

// promptsMu guards every fake's prompts: the verb asks both judges at once.
var promptsMu sync.Mutex

func (f fakeJudge) Name() string { return f.name }

func (f fakeJudge) Ask(_ context.Context, prompt string) (string, error) {
	if f.prompts != nil {
		promptsMu.Lock()
		*f.prompts = append(*f.prompts, prompt)
		promptsMu.Unlock()
	}
	if f.reply == "" {
		return "", errors.New("the harness is down")
	}
	return f.reply, nil
}

type fakeJev struct {
	keys    []string
	reading *Reading
}

func (f *fakeJev) Score(_ context.Context, p Panel, key string) (*Reading, error) {
	f.keys = append(f.keys, key)
	if len(p.Judges) != 2 {
		return nil, errors.New("want two judges")
	}
	return f.reading, nil
}

const testRubric = "# sdlc-triage\n\n| Threshold | Value | What it gates |\n| --- | --- | --- |\n| `convergence` | 0.85 | x |\n| `confidence` | 0.85 | y |\n"

func judgeWorld(t *testing.T, jev *fakeJev, judges ...Judge) (string, cli.Handler) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("OTHER='x'\n"+JevKeyVar+"='jev-secret-key'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rubric.md"), []byte(testRubric), 0o600); err != nil {
		t.Fatal(err)
	}
	j := Judging{Judges: judges, Jev: jev, Getwd: func() (string, error) { return dir, nil },
		FindCheckout: func(string) (string, error) { return dir, nil }}
	return dir, JudgeVerb(j)
}

func inv(flags map[string]string) cli.Invocation {
	r := cli.Surface()
	v, _ := r.Verb("escalation judge")
	return cli.Invocation{Verb: v, Flags: flags, Args: []string{}}
}

// `loop escalation judge` with fake judges and a fake Jev writes a panel
// file that `escalation route` accepts.
func TestJudgeWritesAPanelFileThatRouteAccepts(t *testing.T) {
	var prompts []string
	jev := &fakeJev{reading: &Reading{Probabilities: map[string]float64{"0": 0, "1": 0.05, "2": 0.5, "3": 0.45}, Score: 2.4, Confidence: 0.9}}
	dir, h := judgeWorld(t, jev,
		fakeJudge{"fable-5", `I read it. {"level":"computed","choice":"inside","reason":"pnpm gates answers it"}`, &prompts},
		fakeJudge{"sol-6", "```json\n{\"level\":\"measured\",\"choice\":\"inside\",\"reason\":\"gh api reads it\"}\n```", &prompts})
	out := filepath.Join(dir, "panel.json")
	res := h(context.Background(), inv(map[string]string{"finding": "the gate fails on main", "rubric": "rubric.md", "out": out}))
	if res.Refusal != nil {
		t.Fatalf("judge refused: %+v", res.Refusal)
	}
	data := res.Data.(map[string]any)
	if data["panel_file"] != out || data["key_from"] != ".env" {
		t.Fatalf("data = %+v", data)
	}
	if d := data["decision"].(Decision); d.Route != RouteLoop {
		t.Fatalf("decision = %+v", d)
	}
	if len(jev.keys) != 1 || jev.keys[0] != "jev-secret-key" {
		t.Fatalf("Jev got keys %v", jev.keys)
	}
	for _, p := range prompts {
		if !strings.Contains(p, "the gate fails on main") || !strings.Contains(p, "# sdlc-triage") || strings.Contains(p, "jev-secret-key") {
			t.Fatalf("judge prompt = %q", p)
		}
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "jev-secret-key") || strings.Contains(fmtAny(res.Data), "jev-secret-key") {
		t.Fatal("the panel or the output holds the key")
	}
	ps, err := ParsePanels(string(raw))
	if err != nil || len(ps) != 1 || DecideAll(ps, DefaultThresholds).Route != RouteLoop {
		t.Fatalf("route cannot read the panel: %+v %v", ps, err)
	}
}

// A judge that fails, and no key, still write a panel: it routes to a
// person, which is the safe answer.
func TestJudgeWithAFailedJudgeAndNoKeyRoutesToAPerson(t *testing.T) {
	jev := &fakeJev{}
	dir, h := judgeWorld(t, jev, fakeJudge{name: "fable-5"},
		fakeJudge{"sol-6", `{"level":"derived","choice":"inside","reason":"the ADR says so"}`, nil})
	if err := os.Remove(filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	t.Setenv(JevKeyVar, "")
	res := h(context.Background(), inv(map[string]string{"finding": "f", "rubric": "rubric.md"}))
	if res.Refusal != nil {
		t.Fatal(res.Refusal)
	}
	data := res.Data.(map[string]any)
	if d := data["decision"].(Decision); d.Route != RoutePerson {
		t.Fatalf("decision = %+v", d)
	}
	if len(jev.keys) != 0 {
		t.Fatal("Jev was called with no key")
	}
	if _, err := os.Stat(data["panel_file"].(string)); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(data["panel_file"].(string))
}

// A rubric whose thresholds drifted from the ones the server routes with
// is refused before any judge is asked.
func TestJudgeRefusesARubricThatDrifted(t *testing.T) {
	var prompts []string
	dir, h := judgeWorld(t, &fakeJev{}, fakeJudge{"a", "{}", &prompts}, fakeJudge{"b", "{}", &prompts})
	if err := os.WriteFile(filepath.Join(dir, "rubric.md"), []byte(strings.Replace(testRubric, "0.85 | y", "0.70 | y", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	res := h(context.Background(), inv(map[string]string{"finding": "f", "rubric": "rubric.md"}))
	if res.Refusal == nil || res.Refusal.Code != cli.CodeUsage || len(prompts) != 0 {
		t.Fatalf("res = %+v, prompts %d", res, len(prompts))
	}
}

func TestParseJudgeReadsTheLastObject(t *testing.T) {
	a, err := ParseJudge("x", `{"type":"result","result":"thinking {\"level\":\"5\",\"choice\":\"outside\",\"reason\":\"only Ian knows\"}"}`)
	if err != nil || a.Level != "owner-only" || a.Choice != Outside {
		t.Fatalf("claude envelope: %+v %v", a, err)
	}
	if _, err := ParseJudge("x", "no json here"); err == nil {
		t.Fatal("prose parsed")
	}
	if _, err := ParseJudge("x", `{"level":"computed","choice":"inside","reason":""}`); err == nil {
		t.Fatal("an answer with no reason parsed")
	}
}

func TestEnvValueReadsBothQuoteForms(t *testing.T) {
	file := "A='plain $x'\nB=\"two\\nlines \\\"q\\\"\"\n"
	if v, _ := envValue(file, "A"); v != "plain $x" {
		t.Fatalf("A = %q", v)
	}
	if v, _ := envValue(file, "B"); v != "two\nlines \"q\"" {
		t.Fatalf("B = %q", v)
	}
	if _, ok := envValue(file, "C"); ok {
		t.Fatal("C found")
	}
}

func fmtAny(v any) string { return fmt.Sprintf("%+v", v) }
