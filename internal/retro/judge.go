package retro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/secrets"
)

// RubricSkill is the retro rubric's directory name in the pack.
const RubricSkill = brief.SkillPrefix + "retro-rubric"

// Judging is everything `retro judge` reaches outside the process. It
// reuses the judges and Jev of `escalation judge`. A test passes fakes, so
// no test calls a model.
type Judging struct {
	Judges []escalation.Judge
	Jev    escalation.Jev
	// Getwd and FindCheckout locate the caller's checkout. os.Getwd and
	// cli.FindCheckout when nil.
	Getwd        func() (string, error)
	FindCheckout func(dir string) (string, error)
	// Record reads the record of the run's retro, so each judge reads the
	// text of each case. Production sets it with ServerRecord; nil gives
	// each case its reference only.
	Record func(ctx context.Context) (Record, error)
	// LoopProject reports whether the checkout is the loop's own project.
	// LoopCheckout when nil.
	LoopProject func(ctx context.Context, checkout string) bool
}

// LiveJudging is the panel of `escalation judge`, with the record of the
// run's retro from the server.
func LiveJudging(reg *cli.Registry, remote cli.Handler) Judging {
	live := escalation.Live()
	return Judging{Judges: live.Judges, Jev: live.Jev, Record: ServerRecord(reg, remote)}
}

// ServerRecord reads the record of the run's retro with `retro record`. A
// refusal, or no retro, is an *escalation.RecordRefusal that holds the
// server's answer.
func ServerRecord(reg *cli.Registry, remote cli.Handler) func(ctx context.Context) (Record, error) {
	return func(ctx context.Context) (Record, error) {
		v, ok := reg.Verb("retro record")
		if !ok {
			return Record{}, errors.New("this build has no retro record verb")
		}
		res := remote(ctx, cli.Invocation{Verb: v, Flags: map[string]string{}})
		if res.Refusal != nil || res.Exit == cli.ExitNothing {
			return Record{}, &escalation.RecordRefusal{Result: res}
		}
		raw, err := json.Marshal(res.Data)
		if err != nil {
			return Record{}, err
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			return Record{}, fmt.Errorf("the server answered a record that does not parse: %w", err)
		}
		return rec, nil
	}
}

// LoopCheckout reports whether the origin of checkout is LoopRepo, the
// rule the route applies to the project's registered repository. A
// checkout with no origin is outside the loop's own project.
func LoopCheckout(ctx context.Context, checkout string) bool {
	out, err := gitx.Run(ctx, checkout, "config", "--get", "remote.origin.url")
	if err != nil {
		return false
	}
	url := strings.TrimSuffix(strings.TrimSpace(string(out)), ".git")
	return strings.EqualFold(url, "git@github.com:"+LoopRepo) || strings.EqualFold(url, "https://github.com/"+LoopRepo)
}

// JudgeVerb is the handler of `loop retro judge`. It runs in the retro
// run's own process, where the harness logins are. For each learning it
// asks both judges the rubric's three questions, then Jev about the pair,
// and it writes every panel to one file that `retro route` sends.
func JudgeVerb(j Judging) cli.Handler {
	return func(ctx context.Context, inv cli.Invocation) cli.Result {
		getwd, find, loop := j.Getwd, j.FindCheckout, j.LoopProject
		if getwd == nil {
			getwd = os.Getwd
		}
		if find == nil {
			find = cli.FindCheckout
		}
		if loop == nil {
			loop = LoopCheckout
		}
		cwd, err := getwd()
		if err != nil {
			return cli.Reject(cli.CodeUsage, "no_cwd", err.Error())
		}
		checkout, err := find(cwd)
		if err != nil {
			return cli.Reject(cli.CodeUsage, "no_checkout", err.Error())
		}
		learnings, ref := readLearnings(inv.Flags["file"], cwd)
		if ref != nil {
			return ref.Result()
		}
		rubric, ref := readSkill(checkout, RubricSkill)
		if ref != nil {
			return ref.Result()
		}
		triage, ref := readSkill(checkout, escalation.RubricSkill)
		if ref != nil {
			return ref.Result()
		}
		th, err := escalation.ThresholdsIn(triage)
		if err != nil {
			return cli.Reject(cli.CodeUsage, "rubric_thresholds", err.Error())
		}
		if th != escalation.DefaultThresholds {
			return cli.Reject(cli.CodeUsage, "rubric_drift", fmt.Sprintf("the triage rubric's thresholds read %+v and the server routes with %+v; change both together", th, escalation.DefaultThresholds))
		}
		if len(j.Judges) != escalation.JudgesRequired {
			return cli.Reject(cli.CodeUsage, "judges", fmt.Sprintf("the panel holds %d judges, and the rubric asks for %d", len(j.Judges), escalation.JudgesRequired))
		}
		texts := map[CaseRef]string{}
		if j.Record != nil && len(learnings) > 0 {
			rec, err := j.Record(ctx)
			if r, ok := errors.AsType[*escalation.RecordRefusal](err); ok {
				return r.Result
			}
			if err != nil {
				return cli.Reject(cli.CodeInternal, "retro_record", err.Error())
			}
			for _, cs := range rec.caseLists() {
				for _, c := range cs {
					texts[c.Ref] = c.Text
				}
			}
		}

		key, inLoop := jevKey(checkout), loop(ctx, checkout)
		panels := make([]Panel, 0, len(learnings))
		notes := []string{}
		for i, l := range learnings {
			p, failed := j.panel(ctx, judgePrompt(rubric, l, texts, inLoop), l, key)
			for _, f := range failed {
				notes = append(notes, fmt.Sprintf("learning %d: %s", i+1, redact(f, key)))
			}
			panels = append(panels, p)
		}
		file, err := writePanels(panels, inv.Flags["out"], cwd)
		if err != nil {
			return cli.Reject(cli.CodeInternal, "panel_write", err.Error())
		}
		return cli.Found(map[string]any{"panel_file": file, "panels": len(panels), "notes": notes})
	}
}

// panel asks both judges about one learning at once, then Jev about the
// pair. It returns why each judge or Jev gave no answer.
func (j Judging) panel(ctx context.Context, prompt string, l Learning, key string) (Panel, []string) {
	answers := make([]*Answer, len(j.Judges))
	failed := make([]string, len(j.Judges))
	var wg sync.WaitGroup
	for i, jd := range j.Judges {
		wg.Go(func() {
			reply, err := jd.Ask(ctx, prompt)
			if err == nil {
				var a Answer
				if a, err = parseReply(jd.Name(), reply); err == nil {
					answers[i] = &a
					return
				}
			}
			failed[i] = "judge: " + err.Error()
		})
	}
	wg.Wait()
	p := Panel{Learning: l}
	var notes []string
	for i := range j.Judges {
		if answers[i] != nil {
			p.Answers = append(p.Answers, *answers[i])
		} else {
			notes = append(notes, failed[i])
		}
	}
	switch {
	case key == "":
		notes = append(notes, "jev: "+escalation.JevKeyVar+" is not available, so nothing scores the convergence")
	case len(p.Answers) != escalation.JudgesRequired:
		notes = append(notes, "jev: not asked, because fewer than two judges answered")
	default:
		if r, err := j.Jev.Score(ctx, jevPanel(p), key); err != nil {
			notes = append(notes, "jev: "+err.Error())
		} else {
			p.Reading = r
		}
	}
	return p, notes
}

// jevPanel puts a retro panel in the shape Jev reads for a triage panel:
// the learning is the finding, and each judge's level holds its three
// answers.
func jevPanel(p Panel) escalation.Panel {
	finding := p.Learning.Statement
	for _, c := range p.Learning.Cases {
		finding += "\n- " + string(c)
	}
	out := escalation.Panel{Finding: finding}
	for _, a := range p.Answers {
		out.Judges = append(out.Judges, escalation.JudgeAnswer{Judge: a.Judge, Reason: a.Reason,
			Level: fmt.Sprintf("target %s, evidence %s, change %s", a.Target, a.Evidence, a.Change)})
	}
	return out
}

// judgePrompt is the prompt one judge reads about one learning. It never
// names the other judge or any answer. Outside the loop's own project it
// rules out pack_pr.
func judgePrompt(rubric string, l Learning, texts map[CaseRef]string, loopProject bool) string {
	lines := []string{
		"You are one of two independent judges. Another judge answers the same questions separately,",
		"and you will not see its answer. Judge the learning below against the rubric that follows.",
		"",
		"Answer the rubric's three questions about this one learning, and answer nothing else. Do not",
		"write the fix. Say where it lives, how many cases show it, and how much it changes the loop.",
		"",
		"Reply with one JSON object and no other text:",
		`{"target":"loop_fault|pack_pr|project_policy|userspace_skill|project_ticket","evidence":"one|two_or_more","change":"edit|extend|add|restructure|n/a","reason":"<one sentence>"}`,
		"",
		"Answer change `n/a` exactly when the target is `loop_fault` or `project_ticket`.",
	}
	if !loopProject {
		lines = append(lines,
			"This retro is outside the loop's own project, so `pack_pr` is not an answer here. A fix to",
			"a loop skill or a loop runbook is a `loop_fault`.")
	}
	lines = append(lines, "", "## The learning", "", l.Statement, "")
	if l.Extends != "" {
		lines = append(lines, "It extends the held learning "+l.Extends+", and counts its cases.", "")
	}
	lines = append(lines, "## Its cases", "")
	for _, c := range l.Cases {
		text, ok := texts[c]
		if !ok {
			text = "This retro's record does not hold the text of this case."
		}
		lines = append(lines, "### "+string(c), "", strings.TrimSpace(text), "")
	}
	lines = append(lines, "## The rubric", "", rubric)
	return strings.Join(lines, "\n")
}

// parseReply narrows one judge's reply to an answer. It unwraps Claude
// Code's JSON envelope, reads the last JSON object, and checks it with
// ParseAnswer under the judge's own name.
func parseReply(judge, raw string) (Answer, error) {
	var env struct {
		Result *string `json:"result"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &env) == nil && env.Result != nil {
		raw = *env.Result
	}
	obj, ok := lastObject(raw)
	if !ok {
		return Answer{}, fmt.Errorf("%s returned no JSON object", judge)
	}
	var a Answer
	if err := json.Unmarshal(obj, &a); err != nil {
		return Answer{}, fmt.Errorf("%s returned text that is not a JSON answer", judge)
	}
	a.Judge, a.Reason = judge, strings.TrimSpace(a.Reason)
	if a.Reason == "" {
		return Answer{}, fmt.Errorf("%s gave no reason, and the reason is what Jev compares", judge)
	}
	named, err := json.Marshal(a)
	if err != nil {
		return Answer{}, err
	}
	return ParseAnswer(named)
}

// lastObject returns the last JSON object in s that decodes.
func lastObject(s string) (json.RawMessage, bool) {
	for i := strings.LastIndex(s, "{"); i >= 0; i = strings.LastIndex(s[:i], "{") {
		var obj json.RawMessage
		if json.NewDecoder(strings.NewReader(s[i:])).Decode(&obj) == nil {
			return obj, true
		}
	}
	return nil, false
}

// readLearnings reads the learnings file. It refuses a learning with no
// statement, no case or a case Parse refuses.
func readLearnings(path, cwd string) ([]Learning, *cli.Refusal) {
	usage := func(rule, hint string) *cli.Refusal { return &cli.Refusal{Code: cli.CodeUsage, Rule: rule, Hint: hint} }
	if path == "" {
		return nil, usage("no_file", "--file names the learnings file")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, usage("no_file", fmt.Sprintf("read the learnings file: %v", err))
	}
	var f struct {
		Learnings []Learning `json:"learnings"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, usage("bad_learnings", fmt.Sprintf("the learnings file is no JSON object of learnings: %v", err))
	}
	for i, l := range f.Learnings {
		switch {
		case strings.TrimSpace(l.Statement) == "":
			return nil, usage("no_statement", fmt.Sprintf("learning %d has no statement", i+1))
		case len(l.Cases) == 0:
			return nil, usage("no_case", fmt.Sprintf("learning %d names no case; name each case of the record that shows it", i+1))
		}
		for _, c := range l.Cases {
			if _, err := Parse(string(c)); err != nil {
				return nil, usage("bad_case", fmt.Sprintf("learning %d: %v", i+1, err))
			}
		}
	}
	return f.Learnings, nil
}

// readSkill reads one pack skill from the checkout, for either harness.
func readSkill(checkout, skill string) (string, *cli.Refusal) {
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		if raw, err := os.ReadFile(filepath.Join(checkout, root, skill, "SKILL.md")); err == nil {
			return string(raw), nil
		}
	}
	return "", &cli.Refusal{Code: cli.CodeUsage, Rule: "no_rubric", Hint: "this checkout holds no " + skill + " skill"}
}

// jevKey reads Jev's key from the checkout's .env, then from the process
// environment. The value is never printed.
func jevKey(checkout string) string {
	if raw, err := os.ReadFile(filepath.Join(checkout, brief.EnvFile)); err == nil {
		if env, err := secrets.ParseEnv(raw); err == nil && env[escalation.JevKeyVar] != "" {
			return env[escalation.JevKeyVar]
		}
	}
	return os.Getenv(escalation.JevKeyVar)
}

// redact hides each copy of key in text. An empty key hides nothing.
func redact(text, key string) string {
	if key == "" {
		return text
	}
	return strings.ReplaceAll(text, key, "[REDACTED]")
}

// writePanels writes the panel file to out, or to a new temporary file, at
// 0600.
func writePanels(panels []Panel, out, cwd string) (string, error) {
	text, err := json.MarshalIndent(map[string]any{"panels": panels}, "", "  ")
	if err != nil {
		return "", err
	}
	if out == "" {
		f, err := os.CreateTemp("", "loop-retro-panel-*.json")
		if err != nil {
			return "", err
		}
		out = f.Name()
		_ = f.Close()
	} else if !filepath.IsAbs(out) {
		out = filepath.Join(cwd, out)
	}
	return out, config.WriteFileAtomic(out, text, 0o600)
}
