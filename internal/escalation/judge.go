package escalation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// JevKeyVar is the name of Jev's key. The key path (KTD22, settled here):
// it is a project manifest secret of this name. The operator stores it with
// `loop secret set TYPESAFE_API_KEY --project <id>` or `loop secret import`,
// and the server writes it into each run's .env at mode 0600 as it writes
// every manifest secret, and removes it at settlement (KTD17). The fixer's
// `escalation judge` reads it from its own checkout's .env, or from its
// process environment, and never prints it. The server holds no harness
// login and calls Jev never.
const JevKeyVar = "TYPESAFE_API_KEY"

// JevBase and JevModel are Jev's endpoint and model alias (jev.ts and
// convergence-ask.ts, alpha).
const (
	JevBase  = "https://api.typesafe.ai"
	JevModel = "jev-latest"
)

// RubricSkill is the triage skill's directory name in the pack.
const RubricSkill = brief.SkillPrefix + "sdlc-triage"

// Judge is one judge harness. Ask sends the prompt and returns the reply.
type Judge interface {
	Name() string
	Ask(ctx context.Context, prompt string) (string, error)
}

// Jev rates how far apart the two judges' answers are.
type Jev interface {
	Score(ctx context.Context, p Panel, key string) (*Reading, error)
}

// Judging is everything `escalation judge` reaches outside the process.
// Production uses Live. A test passes fakes, so no test calls a model.
type Judging struct {
	Judges []Judge
	Jev    Jev
	// Getwd and FindCheckout locate the caller's checkout. os.Getwd and
	// cli.FindCheckout when nil.
	Getwd        func() (string, error)
	FindCheckout func(dir string) (string, error)
}

// Live is the production panel: the two judges the owner named
// (judgesFor in scripts/sdlc/convergence-call.ts, alpha) and Jev over HTTP.
func Live() Judging {
	return Judging{
		Judges: []Judge{
			CommandJudge{JudgeName: "fable-5", Bin: "claude", Args: []string{"-p", "--model", "claude-fable-5-1", "--output-format", "json"}},
			CommandJudge{JudgeName: "sol-6", Bin: "codex", Args: []string{"exec", "-m", "gpt-5.6-sol", "-"}},
		},
		Jev: HTTPJev{Base: JevBase, Client: &http.Client{Timeout: 2 * time.Minute}},
	}
}

// CommandJudge asks one harness CLI. The prompt goes through stdin and no
// shell, so no argument carries prompt text.
type CommandJudge struct {
	JudgeName string
	Bin       string
	Args      []string
	Timeout   time.Duration
}

// Name is the judge's name in the panel.
func (c CommandJudge) Name() string { return c.JudgeName }

// Ask runs the harness once.
func (c CommandJudge) Ask(ctx context.Context, prompt string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, c.Args...)
	cmd.Stdin = strings.NewReader(prompt)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil && out.Len() == 0 {
		return "", fmt.Errorf("%s: %w", c.JudgeName, err)
	}
	return out.String(), nil
}

// HTTPJev calls Jev's systemone endpoint.
type HTTPJev struct {
	Base   string
	Client *http.Client
}

// Score asks Jev one Score question about the pair of judges.
func (h HTTPJev) Score(ctx context.Context, p Panel, key string) (*Reading, error) {
	body, err := json.Marshal(convergenceRequest(p))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Base+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+key)
	req.Header.Set("content-type", "application/json")
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev could not be reached: %w", errors.Unwrap(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("jev answered %d, so the convergence is unscored", resp.StatusCode)
	}
	return readConvergence(raw)
}

// convergenceRequest is the body of POST /v1/systemone (convergence-ask.ts,
// alpha): the finding, each judge's level, gate and reason, and one Score
// question over the convergence scale.
func convergenceRequest(p Panel) map[string]any {
	judges := make([]map[string]string, 0, len(p.Judges))
	for _, j := range p.Judges {
		judges = append(judges, map[string]string{"judge": j.Judge, "level": j.Level, "gate": j.Choice, "reason": j.Reason})
	}
	criteria := make([]string, 0, len(ConvergenceSteps))
	for _, s := range ConvergenceSteps {
		criteria = append(criteria, s.Name+": "+s.Looks)
	}
	return map[string]any{
		"state": map[string]any{"finding": p.Finding, "judges": judges},
		"model": JevModel,
		"questions": map[string]any{
			"convergence": map[string]any{
				"type": "score",
				"instructions": map[string]string{
					"question": "Rate how far apart the two judges' answers are. Read each reason, and not only each level. " +
						"Two judges that chose one level for different evidence are further apart than two judges that chose one level for the same evidence.",
					"note": "Do not score the finding. Do not say which judge is right.",
				},
				"criteria": criteria,
			},
		},
	}
}

func readConvergence(raw []byte) (*Reading, error) {
	var body struct {
		Answers map[string]struct {
			Probabilities map[string]float64 `json:"probabilities"`
			Score         *float64           `json:"score"`
			Confidence    *float64           `json:"confidence"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("jev returned a body that is not JSON: %w", err)
	}
	a, ok := body.Answers["convergence"]
	if !ok || a.Probabilities == nil {
		return nil, errors.New("jev returned no convergence answer with probabilities")
	}
	if a.Score == nil || a.Confidence == nil {
		return nil, errors.New("jev's answer carries no numeric score and confidence")
	}
	return &Reading{Probabilities: a.Probabilities, Score: *a.Score, Confidence: *a.Confidence}, nil
}

// JudgePrompt is the prompt one judge reads about one finding
// (judgePrompt, alpha). It never names the other judge or its answer.
func JudgePrompt(rubric, finding string) string {
	return strings.Join([]string{
		"You are one of two independent judges. Another judge answers the same question separately,",
		"and you will not see its answer. Score the finding below against the rubric that follows.",
		"",
		"Answer the rubric's two questions about this one finding, and answer nothing else. Do not say",
		"what the answer to the finding is. Say what kind of answer it needs.",
		"",
		"Reply with one JSON object and no other text:",
		`{"level":"<one level name>","choice":"inside|outside","reason":"<one sentence>"}`,
		"",
		"Name the command, the rule or the arithmetic in your reason. A reason that names no evidence",
		"is the reason the other judge cannot be compared against.",
		"",
		"## The finding",
		"",
		finding,
		"",
		"## The rubric",
		"",
		rubric,
	}, "\n")
}

// ParseJudge narrows one judge's reply. It reads the last balanced JSON
// object, and it unwraps Claude Code's JSON envelope first.
func ParseJudge(judge, raw string) (JudgeAnswer, error) {
	var env struct {
		Result *string `json:"result"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &env) == nil && env.Result != nil {
		raw = *env.Result
	}
	obj, ok := lastObject(raw)
	if !ok {
		return JudgeAnswer{}, fmt.Errorf("%s returned no JSON object", judge)
	}
	var v struct {
		Level  string `json:"level"`
		Choice string `json:"choice"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(obj), &v); err != nil {
		return JudgeAnswer{}, fmt.Errorf("%s returned text that is not a JSON object", judge)
	}
	l, ok := LevelFor(v.Level)
	if !ok {
		return JudgeAnswer{}, fmt.Errorf("%s scored %q, which is no level of the rubric", judge, v.Level)
	}
	c := strings.TrimSpace(v.Choice)
	if c != Inside && c != Outside {
		return JudgeAnswer{}, fmt.Errorf("%s answered the gate %q, which is no option", judge, v.Choice)
	}
	if strings.TrimSpace(v.Reason) == "" {
		return JudgeAnswer{}, fmt.Errorf("%s gave no reason, and the reason is what Jev compares", judge)
	}
	return JudgeAnswer{Judge: judge, Level: l.Name, Choice: c, Reason: strings.TrimSpace(v.Reason)}, nil
}

// lastObject returns the last balanced {...} run in s.
func lastObject(s string) (string, bool) {
	end := strings.LastIndex(s, "}")
	if end < 0 {
		return "", false
	}
	depth := 0
	for i := end; i >= 0; i-- {
		switch s[i] {
		case '}':
			depth++
		case '{':
			depth--
			if depth == 0 {
				return s[i : end+1], true
			}
		}
	}
	return "", false
}

// jevKey reads Jev's key from the checkout's .env, then from the process
// environment. It returns the source's name, which a report may print. The
// value is never printed.
func jevKey(checkout string) (string, string) {
	if checkout != "" {
		if data, err := os.ReadFile(filepath.Join(checkout, brief.EnvFile)); err == nil {
			if v, ok := envValue(string(data), JevKeyVar); ok && v != "" {
				return v, brief.EnvFile
			}
		}
	}
	if v := os.Getenv(JevKeyVar); v != "" {
		return v, "process environment"
	}
	return "", "no source in this checkout holds " + JevKeyVar
}

// envValue reads one value of a .env that secrets.WriteEnvFile wrote: a
// single-quoted literal, or a double-quoted value with \\ \" \n \r.
func envValue(file, name string) (string, bool) {
	for _, line := range strings.Split(file, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != name {
			continue
		}
		v = strings.TrimSpace(v)
		switch {
		case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
			return v[1 : len(v)-1], true
		case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
			r := strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n", `\r`, "\r")
			return r.Replace(v[1 : len(v)-1]), true
		}
		return v, true
	}
	return "", false
}

// rubricPath finds the triage rubric: --rubric, or the pack's skill in the
// checkout for either harness.
func rubricPath(flag, cwd, checkout string) (string, error) {
	if flag != "" {
		if !filepath.IsAbs(flag) {
			flag = filepath.Join(cwd, flag)
		}
		return flag, nil
	}
	if checkout != "" {
		for _, root := range []string{".claude/skills", ".agents/skills"} {
			p := filepath.Join(checkout, root, RubricSkill, "SKILL.md")
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	return "", errors.New("no triage rubric in this checkout; pass --rubric <path to the sdlc-triage skill>")
}

func usage(rule, hint string) cli.Result {
	return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: rule, Hint: hint})
}

// JudgeVerb is the handler of `loop escalation judge` (KTD22). It runs in
// the fixer's own process: it asks both judges, then Jev, routes the panel
// with the server's thresholds, writes the panel file and prints it with
// the decision. `escalation route` submits that file, and the server routes
// it again, so the route is never a value the fixer typed.
func JudgeVerb(j Judging) cli.Handler {
	return func(ctx context.Context, inv cli.Invocation) cli.Result {
		getwd := j.Getwd
		if getwd == nil {
			getwd = os.Getwd
		}
		find := j.FindCheckout
		if find == nil {
			find = cli.FindCheckout
		}
		cwd, err := getwd()
		if err != nil {
			return usage("no_cwd", err.Error())
		}
		checkout, _ := find(cwd)
		finding := strings.TrimSpace(inv.Flags["finding"])
		if finding == "" {
			return usage("no_finding", "--finding names one finding in one sentence")
		}
		path, err := rubricPath(inv.Flags["rubric"], cwd, checkout)
		if err != nil {
			return usage("no_rubric", err.Error())
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return usage("no_rubric", fmt.Sprintf("read the rubric: %v", err))
		}
		rubric := string(raw)
		th, err := ThresholdsIn(rubric)
		if err != nil {
			return usage("rubric_thresholds", err.Error())
		}
		if th != DefaultThresholds {
			return usage("rubric_drift", fmt.Sprintf("the rubric's thresholds read %+v and the server routes with %+v; change both together", th, DefaultThresholds))
		}
		if len(j.Judges) != JudgesRequired {
			return usage("judges", fmt.Sprintf("the panel holds %d judges, and the rubric asks for %d", len(j.Judges), JudgesRequired))
		}

		prompt := JudgePrompt(rubric, finding)
		answers := make([]*JudgeAnswer, len(j.Judges))
		failed := make([]string, len(j.Judges))
		var wg sync.WaitGroup
		for i, jd := range j.Judges {
			wg.Add(1)
			go func() {
				defer wg.Done()
				reply, err := jd.Ask(ctx, prompt)
				if err == nil {
					var a JudgeAnswer
					if a, err = ParseJudge(jd.Name(), reply); err == nil {
						answers[i] = &a
						return
					}
				}
				failed[i] = err.Error()
			}()
		}
		wg.Wait()
		panel := Panel{Finding: finding, Judges: []JudgeAnswer{}}
		var notes []string
		for i := range j.Judges {
			if answers[i] != nil {
				panel.Judges = append(panel.Judges, *answers[i])
			} else {
				notes = append(notes, "judge: "+failed[i])
			}
		}
		key, from := jevKey(checkout)
		switch {
		case key == "":
			notes = append(notes, "jev: "+JevKeyVar+" is not available, so nothing scores the convergence")
		case len(panel.Judges) != JudgesRequired:
			notes = append(notes, "jev: not asked, because fewer than two judges answered")
		default:
			if r, err := j.Jev.Score(ctx, panel, key); err != nil {
				notes = append(notes, "jev: "+strings.ReplaceAll(err.Error(), key, "[REDACTED]"))
			} else {
				panel.Reading = r
			}
		}
		decision := Decide(panel, DefaultThresholds)
		file, err := writePanel(panel, inv.Flags["out"], cwd)
		if err != nil {
			return cli.Refuse(cli.Refusal{Code: cli.CodeInternal, Rule: "panel_write", Hint: err.Error()})
		}
		if notes == nil {
			notes = []string{}
		}
		return cli.Found(map[string]any{"panel": panel, "panel_file": file, "decision": decision, "key_from": from, "notes": notes})
	}
}

// writePanel writes the panel to out, or to a new temporary file, at 0600.
func writePanel(p Panel, out, cwd string) (string, error) {
	text, err := EncodePanels([]Panel{p})
	if err != nil {
		return "", err
	}
	if out == "" {
		f, err := os.CreateTemp("", "loop-panel-*.json")
		if err != nil {
			return "", err
		}
		out = f.Name()
		_ = f.Close()
	} else if !filepath.IsAbs(out) {
		out = filepath.Join(cwd, out)
	}
	return out, config.WriteFileAtomic(out, []byte(text), 0o600)
}
