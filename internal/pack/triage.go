package pack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/escalation"
)

// The triage check (triage-rubric.ts, alpha) holds three sources of one
// scale to each other: the rubric's prose in the pack, the labelled
// escalation corpus, and the literal the server routes with. Each is
// prose or data that nothing else joins, so a level renamed in one is
// silently absent from the others. It checks:
//
//   - each level has a section that says when not to choose it
//   - the scale is ordered: one cut between the loop and a person
//   - the gate is a choice of two, and the route section names the one
//     that escalates
//   - each corpus finding scores a level and a gate the rubric defines,
//     and its route follows from them
//   - each level reaches at least one finding, and each case routes to a
//     person when one of its findings does
//   - the levels, the gate, the convergence scale and both thresholds
//     match the server's literal
//   - each fenced `loop escalation raise` in the pack passes --score
//
// It does not check whether a level is the right one for a finding. That
// is the judges' work, and the corpus records a person's answer to it.

// CorpusPath is the labelled escalation corpus, from the repository root.
const CorpusPath = "acceptance/corpus/escalations.json"

// RubricSource is the rubric's path in the pack source.
const RubricSource = "skills/" + escalation.RubricSkill + "/SKILL.md"

// rubricLevel is one row of the rubric's Score.
type rubricLevel struct {
	line    int
	ordinal int
	name    string
	answers string
	route   string
}

// rubricStep is one row of the convergence scale.
type rubricStep struct {
	line    int
	ordinal int
	name    string
	looks   string
}

type rubric struct {
	levels []rubricLevel
	// gate is the Choice's options, in the rubric's order.
	gate []string
	// escalates is the option that sends a finding to a person at any level.
	escalates string
	steps     []rubricStep
}

var escalatesLine = regexp.MustCompile("(?m)^- One judge answers `([a-z-]+)`\\.")

func parseRubric(text string) rubric {
	var r rubric
	for _, row := range Table(text, "level", "name", "what answers the finding", "route") {
		n, _ := strconv.Atoi(Bare(row.Cells[0]))
		r.levels = append(r.levels, rubricLevel{row.Line, n, Bare(row.Cells[1]), Bare(row.Cells[2]), Bare(row.Cells[3])})
	}
	for _, row := range Table(text, "option", "when to choose it") {
		r.gate = append(r.gate, Bare(row.Cells[0]))
	}
	if m := escalatesLine.FindStringSubmatch(text); m != nil {
		r.escalates = m[1]
	}
	for _, row := range Table(text, "level", "name", "what the two answers look like") {
		n, _ := strconv.Atoi(Bare(row.Cells[0]))
		r.steps = append(r.steps, rubricStep{row.Line, n, Bare(row.Cells[1]), Bare(row.Cells[2])})
	}
	return r
}

// routes are the two places a finding goes.
var routes = []string{string(escalation.RouteLoop), string(escalation.RoutePerson)}

// checkRubric checks the rubric alone.
func checkRubric(file, text string, r rubric) []Finding {
	var out []Finding
	add := func(line int, kind, format string, args ...any) {
		out = append(out, Finding{File: file, Line: line, Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
	if len(r.levels) == 0 {
		add(0, "levels", "the rubric defines no level")
	}
	seen := map[string]bool{}
	for _, l := range r.levels {
		title := fmt.Sprintf("Level %d, %s", l.ordinal, l.name)
		body := Section(text, 3, func(t string) bool { return strings.TrimSpace(t) == title })
		switch {
		case body == "":
			add(l.line, "description", "level %s has no section ### %s", l.name, title)
		case !strings.Contains(body, "Not this level when"):
			add(l.line, "borderline", "level %s never says when not to choose it (\"Not this level when\")", l.name)
		}
		if !slices.Contains(routes, l.route) {
			add(l.line, "route", "level %s routes to %q, and a route is %s", l.name, l.route, strings.Join(routes, " or "))
		}
		if seen[l.name] {
			add(l.line, "duplicate", "two levels are named %s", l.name)
		}
		seen[l.name] = true
	}
	if first := slices.IndexFunc(r.levels, func(l rubricLevel) bool { return l.route == string(escalation.RoutePerson) }); first >= 0 {
		for _, l := range r.levels[first:] {
			if l.route != string(escalation.RoutePerson) {
				add(l.line, "ordered", "level %s routes to %s above a level that routes to a person; a score needs one cut", l.name, l.route)
			}
		}
	}
	if len(r.gate) != 2 {
		add(0, "gate", "the gate has %d options, and a choice needs 2", len(r.gate))
	}
	if !slices.Contains(r.gate, r.escalates) {
		add(0, "escalates", "the route section escalates on %q, and the gate offers %s", r.escalates, strings.Join(r.gate, ", "))
	}
	return out
}

type corpusFinding struct {
	ID      string `json:"id"`
	Level   string `json:"level"`
	Gate    string `json:"gate"`
	Route   string `json:"route"`
	Finding string `json:"finding"`
}

type corpusCase struct {
	ID       string          `json:"id"`
	Stage    string          `json:"stage"`
	Route    string          `json:"route"`
	Findings []corpusFinding `json:"findings"`
}

// checkCorpus holds the corpus to the rubric: each score resolves, each
// route follows, each level is used and each case composes.
func checkCorpus(r rubric, raw []byte) []Finding {
	var out []Finding
	add := func(id, kind, format string, args ...any) {
		out = append(out, Finding{File: CorpusPath, Line: lineOf(raw, id), Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c struct {
		About string       `json:"about"`
		Cases []corpusCase `json:"cases"`
	}
	if err := dec.Decode(&c); err != nil {
		return []Finding{{File: CorpusPath, Kind: "corpus", Detail: err.Error()}}
	}
	route := map[string]string{}
	for _, l := range r.levels {
		route[l.name] = l.route
	}
	used := map[string]bool{}
	for _, cs := range c.Cases {
		if len(cs.Findings) == 0 {
			add(cs.ID, "case", "%s is a case with no scored finding", cs.ID)
			continue
		}
		composed := string(escalation.RouteLoop)
		for _, f := range cs.Findings {
			if f.Route == string(escalation.RoutePerson) {
				composed = f.Route
			}
			lr, ok := route[f.Level]
			if !ok {
				add(f.ID, "level", "%s scores %q, which the rubric lacks", f.ID, f.Level)
				continue
			}
			used[f.Level] = true
			if !slices.Contains(r.gate, f.Gate) {
				add(f.ID, "gate", "%s answers the gate %q, which the gate lacks", f.ID, f.Gate)
				continue
			}
			want := lr
			if f.Gate == r.escalates {
				want = string(escalation.RoutePerson)
			}
			if f.Route != want {
				add(f.ID, "follows", "%s routes to %q, and %s with %s gives %q", f.ID, f.Route, f.Level, f.Gate, want)
			}
		}
		if cs.Route != composed {
			add(cs.ID, "composes", "%s routes to %q, and its findings compose to %q", cs.ID, cs.Route, composed)
		}
	}
	for _, l := range r.levels {
		if !used[l.name] {
			out = append(out, Finding{File: CorpusPath, Kind: "unused", Detail: fmt.Sprintf("no finding scores %q, so no case tests it", l.name)})
		}
	}
	return out
}

// lineOf returns the line of the first JSON string that holds id, or 0.
func lineOf(raw []byte, id string) int {
	i := bytes.Index(raw, []byte(strconv.Quote(id)))
	if i < 0 {
		return 0
	}
	return bytes.Count(raw[:i], []byte("\n")) + 1
}

// checkServer holds the rubric to the literal the server routes with
// (checkCode and checkScale, alpha). A cell the rubric re-words and the
// server does not would route by a scale nobody wrote.
func checkServer(file, text string, r rubric) []Finding {
	var out []Finding
	add := func(line int, format string, args ...any) {
		out = append(out, Finding{File: file, Line: line, Kind: "server", Detail: fmt.Sprintf(format, args...)})
	}
	for _, s := range escalation.Levels {
		i := slices.IndexFunc(r.levels, func(l rubricLevel) bool { return l.name == s.Name })
		if i < 0 {
			add(0, "the rubric defines no level %q, and the server routes with it", s.Name)
			continue
		}
		l := r.levels[i]
		if l.ordinal != s.Ordinal || l.answers != s.Answers || l.route != string(s.Route) {
			add(l.line, "level %s reads %d, %q, %s; the server holds %d, %q, %s",
				l.name, l.ordinal, l.answers, l.route, s.Ordinal, s.Answers, s.Route)
		}
	}
	for _, l := range r.levels {
		if _, ok := escalation.LevelFor(l.name); !ok {
			add(l.line, "the rubric defines %q, and the server holds no such level", l.name)
		}
	}
	if want := []string{escalation.Inside, escalation.Outside}; !slices.Equal(r.gate, want) {
		add(0, "the gate reads %s, and the server reads %s", strings.Join(r.gate, ", "), strings.Join(want, ", "))
	}
	if r.escalates != escalation.Outside {
		add(0, "the route section escalates on %q, and the server on %q", r.escalates, escalation.Outside)
	}
	if len(r.steps) != len(escalation.ConvergenceSteps) {
		add(0, "the convergence scale holds %d levels, and the server %d", len(r.steps), len(escalation.ConvergenceSteps))
	}
	for _, s := range escalation.ConvergenceSteps {
		i := slices.IndexFunc(r.steps, func(st rubricStep) bool { return st.ordinal == s.Ordinal })
		if i < 0 {
			continue
		}
		if st := r.steps[i]; st.name != s.Name || st.looks != s.Looks {
			add(st.line, "convergence level %d reads %s, %q; the server holds %s, %q", s.Ordinal, st.name, st.looks, s.Name, s.Looks)
		}
	}
	th, err := escalation.ThresholdsIn(text)
	switch {
	case err != nil:
		add(0, "%v", err)
	case th != escalation.DefaultThresholds:
		add(0, "the thresholds read %+v, and the server routes with %+v; change both together", th, escalation.DefaultThresholds)
	}
	return out
}

var (
	raiseCmd = regexp.MustCompile(`\bloop\s+escalation\s+raise\b`)
	scoreArg = regexp.MustCompile(`(?:^|\s)--score\b`)
)

// checkReach reports each fenced `loop escalation raise` with no
// --score. A fenced line is a command to copy, and the server refuses an
// unscored raise. A mention in prose is no instruction, so it passes.
func checkReach(src fs.FS) ([]Finding, error) {
	files, err := markdown(src)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, f := range files {
		raw, err := fs.ReadFile(src, f.source)
		if err != nil {
			return nil, err
		}
		code, _ := split(string(raw))
		for _, s := range code {
			if s.fenced && raiseCmd.MatchString(s.text) && !scoreArg.MatchString(s.text) {
				out = append(out, Finding{File: f.source, Line: s.line, Kind: "unscored",
					Detail: "`loop escalation raise` passes no --score, and the server refuses an unscored escalation"})
			}
		}
	}
	return out, nil
}

// CheckTriage holds the pack's rubric, the corpus and the server's literal
// to one scale.
func CheckTriage(src fs.FS, corpus []byte) ([]Finding, error) {
	raw, err := fs.ReadFile(src, RubricSource)
	if err != nil {
		return nil, fmt.Errorf("pack: read the rubric: %w", err)
	}
	text := string(raw)
	r := parseRubric(text)
	out := checkRubric(RubricSource, text, r)
	out = append(out, checkCorpus(r, corpus)...)
	out = append(out, checkServer(RubricSource, text, r)...)
	reach, err := checkReach(src)
	if err != nil {
		return nil, err
	}
	return append(out, reach...), nil
}
