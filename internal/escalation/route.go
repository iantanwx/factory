// Package escalation scores and routes escalations (KTD22). A run raises one
// with its own score against the triage rubric. A score the rubric routes
// back to the loop is refused with a triaged note, and the item keeps its
// state. Any other score waits for the triage panel: two judges and Jev,
// which `loop escalation judge` runs inside the triage run's own process.
// `loop escalation route` submits that panel. A route to a person moves the
// item to escalated, puts it in the inbox and rings the coordinator.
//
// The rules port scripts/sdlc/triage-route.ts and scripts/sdlc/convergence.ts
// (alpha), and skills/sdlc-triage/SKILL.md holds them in prose.
package escalation

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/machine"
)

// Route is where one finding goes.
type Route string

// The two routes. Nothing else is a route.
const (
	RouteLoop   Route = "loop"
	RoutePerson Route = "person"
)

// Level is one level of the rubric's Score.
type Level struct {
	Ordinal int    `json:"ordinal"`
	Name    string `json:"name"`
	Answers string `json:"answers"`
	Route   Route  `json:"route"`
}

// Levels is the rubric's Score in order, ported from TRIAGE_LEVELS
// (scripts/sdlc/triage-route.ts, alpha). The pack gate holds the
// rubric's prose to these cells.
var Levels = []Level{
	{1, "computed", "A command in this repository", RouteLoop},
	{2, "measured", "A read-only command against the world outside", RouteLoop},
	{3, "derived", "This repository's own documents and rules", RouteLoop},
	{4, "preference", "The owner's taste, among answers that all fit", RoutePerson},
	{5, "owner-only", "A fact the owner holds and this repository does not", RoutePerson},
}

// The rubric's Choice. Outside sends a finding to a person at any level.
const (
	Inside  = "inside"
	Outside = "outside"
)

// LevelFor returns the level a name or an ordinal names.
func LevelFor(raw string) (Level, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	for _, l := range Levels {
		if l.Name == s || strconv.Itoa(l.Ordinal) == s {
			return l, true
		}
	}
	return Level{}, false
}

func levelNames() string {
	parts := make([]string, 0, len(Levels))
	for _, l := range Levels {
		parts = append(parts, fmt.Sprintf("%d %s", l.Ordinal, l.Name))
	}
	return strings.Join(parts, ", ")
}

// Triage is one escalation's answer to the rubric's two questions.
type Triage struct {
	Level  Level  `json:"level"`
	Choice string `json:"choice"`
}

// ParseTriage reads a score and a choice. The score has no default. The
// choice defaults to inside, the strict answer: a forgotten flag refuses
// more escalations, not fewer.
func ParseTriage(score, choice string) (Triage, error) {
	l, ok := LevelFor(score)
	if !ok {
		return Triage{}, fmt.Errorf("%q is no level of the rubric; --score takes one of %s", score, levelNames())
	}
	switch c := strings.ToLower(strings.TrimSpace(choice)); c {
	case "":
		return Triage{Level: l, Choice: Inside}, nil
	case Inside, Outside:
		return Triage{Level: l, Choice: c}, nil
	}
	return Triage{}, fmt.Errorf("%q is no answer to the rubric's Choice; --choice takes inside or outside", choice)
}

// Route is where the triage sends the finding. The Choice overrides the
// level, never the other way.
func (t Triage) Route() Route {
	if t.Choice == Outside {
		return RoutePerson
	}
	return t.Level.Route
}

// RefusalHint is what a run reads when the rubric sends its finding back
// to the loop. It names the routes back in, and the two escapes (refusalFor
// in scripts/sdlc/triage-route.ts, alpha). It names the close of the
// item's kind only: an epic has no supersede, and a ticket no reasoned
// cancel outside escalated (F-55).
func RefusalHint(t Triage, kind machine.Kind) string {
	l := t.Level
	closes := map[machine.Kind]string{
		machine.KindTicket: "When the base branch, a done item or an ADR already answers the ticket, close it with `loop work supersede`. ",
		machine.KindEpic: "When an ADR or a ruling gives the epic's work to another item before its execution, " +
			"close it with `loop work cancel` and a reason. ",
	}
	return fmt.Sprintf("level %d %s routes to the loop, not to a person: %s answers the finding. "+
		"The item keeps its state and your lease, and no person was told; your reason is recorded on the item as a triaged note. "+
		"Level 1 or 2: run the command that answers the finding, and quote its output in what you write next. "+
		"Level 3: apply the derived answer, and name the rule and the document it rests on. "+
		"%sRaise again only when the level is wrong (read that level's \"Not this level when\" paragraph in the sdlc-triage skill), "+
		"or when one answer sends a message, spends money, publishes, deploys or changes a vendor or production system: then pass --choice outside.",
		l.Ordinal, l.Name, strings.ToLower(l.Answers), closes[kind])
}

// TriagedNote is the note a finding routed to the loop leaves on its item.
// The marker carries the level and the choice, so an audit can count a
// mis-score without parsing English (triagedComment, alpha).
func TriagedNote(t Triage, reason string) string {
	return fmt.Sprintf("<!-- sdlc:triaged level=%q choice=%q route=%q ordinal=\"%d\" -->\n"+
		"Not escalated. The rubric routes this to the loop. Scored level %d, %s, with the Choice %s.\n\n%s",
		t.Level.Name, t.Choice, t.Route(), t.Level.Ordinal, t.Level.Ordinal, t.Level.Name, t.Choice, reason)
}

// JudgeAnswer is one judge's answer about one finding. Level is the
// rubric's level name.
type JudgeAnswer struct {
	Judge  string `json:"judge"`
	Level  string `json:"level"`
	Choice string `json:"choice"`
	Reason string `json:"reason"`
}

// Reading is what Jev answered about the pair of judges. Probabilities is
// keyed by the convergence level's ordinal, "0" to "3".
type Reading struct {
	Probabilities map[string]float64 `json:"probabilities"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
}

// Panel is one finding, both judges' answers and Jev's reading, or none.
type Panel struct {
	Finding string        `json:"finding"`
	Judges  []JudgeAnswer `json:"judges"`
	Reading *Reading      `json:"reading"`
}

// ConvergenceStep is one level of the scale Jev answers about the judges.
type ConvergenceStep struct {
	Ordinal int
	Name    string
	Looks   string
}

// ConvergenceSteps is the convergence scale (CONVERGENCE_STEPS,
// scripts/sdlc/convergence-scale.ts, alpha).
var ConvergenceSteps = []ConvergenceStep{
	{0, "contradict", "The two judges name different kinds of answer, or they answer the gate differently"},
	{1, "diverge", "The two judges name one kind of answer, and each reason rests on different evidence"},
	{2, "align", "The two judges name one kind of answer, and both reasons rest on the same evidence"},
	{3, "identical", "The two reasons name the same evidence, and neither one names evidence the other missed"},
}

// resolvingStep is the lowest level that means the two judges agree.
const resolvingStep = 2

// JudgesRequired is how many judges must answer one finding.
const JudgesRequired = 2

// Thresholds gate a panel that every carve-out let past.
type Thresholds struct {
	Convergence float64 `json:"convergence"`
	Confidence  float64 `json:"confidence"`
}

// DefaultThresholds are the values of the rubric's table ("The two
// thresholds a person may change", skills/sdlc-triage/SKILL.md, alpha). The
// server routes with these. `escalation judge` refuses a rubric whose table
// says otherwise, so a changed number is a visible drift.
var DefaultThresholds = Thresholds{Convergence: 0.85, Confidence: 0.85}

var thresholdRow = regexp.MustCompile("(?m)^\\|\\s*`(convergence|confidence)`\\s*\\|\\s*([0-9.]+)\\s*\\|")

// ThresholdsIn reads both thresholds out of the rubric's table. A missing
// row is an error, never a default.
func ThresholdsIn(rubric string) (Thresholds, error) {
	got := map[string]float64{}
	for _, m := range thresholdRow.FindAllStringSubmatch(rubric, -1) {
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil || v < 0 || v > 1 {
			return Thresholds{}, fmt.Errorf("the rubric's %s threshold %q is no fraction between 0 and 1", m[1], m[2])
		}
		got[m[1]] = v
	}
	for _, n := range []string{"convergence", "confidence"} {
		if _, ok := got[n]; !ok {
			return Thresholds{}, fmt.Errorf("the rubric's threshold table has no %s row", n)
		}
	}
	return Thresholds{Convergence: got["convergence"], Confidence: got["confidence"]}, nil
}

// Decision is where one panel sends its finding, and the evidence.
type Decision struct {
	Route       Route    `json:"route"`
	Because     []string `json:"because,omitempty"`
	Convergence float64  `json:"convergence,omitempty"`
	Confidence  float64  `json:"confidence,omitempty"`
	Score       float64  `json:"score,omitempty"`
}

// Agreement is the share of Jev's belief that the two judges agree.
func Agreement(r *Reading) float64 {
	sum := 0.0
	for k, p := range r.Probabilities {
		if n, err := strconv.Atoi(k); err == nil && n >= resolvingStep {
			sum += p
		}
	}
	return sum
}

// Unusable says why a reading cannot be acted on, or "".
func Unusable(r *Reading) string {
	if len(r.Probabilities) != len(ConvergenceSteps) {
		return fmt.Sprintf("Jev scored against %d levels, and this scale holds %d", len(r.Probabilities), len(ConvergenceSteps))
	}
	total := 0.0
	for _, s := range ConvergenceSteps {
		p, ok := r.Probabilities[strconv.Itoa(s.Ordinal)]
		if !ok {
			return fmt.Sprintf("Jev's answer carries no probability for level %d", s.Ordinal)
		}
		total += p
	}
	if math.Abs(total-1) > 0.01 {
		return fmt.Sprintf("Jev's probabilities add to %.3f, and a distribution adds to 1", total)
	}
	if r.Confidence < 0 || r.Confidence > 1 || math.IsNaN(r.Confidence) {
		return fmt.Sprintf("Jev's confidence reads %v, and a confidence is 0 to 1", r.Confidence)
	}
	return ""
}

// Decide routes one panel: every carve-out first, and the thresholds only
// after all of them pass (decide in scripts/sdlc/convergence.ts, alpha).
func Decide(p Panel, th Thresholds) Decision {
	var because []string
	names := map[string]bool{}
	for _, j := range p.Judges {
		names[j.Judge] = true
	}
	if len(p.Judges) != JudgesRequired || len(names) != JudgesRequired {
		because = append(because, fmt.Sprintf("%d judge(s) answered out of %d, and one judge twice is not two judges", len(names), JudgesRequired))
	}
	for _, j := range p.Judges {
		if j.Choice == Outside {
			because = append(because, fmt.Sprintf("%s answered `outside`, so one answer acts outside this repository", j.Judge))
		}
		if l, ok := LevelFor(j.Level); !ok || l.Route == RoutePerson {
			because = append(because, fmt.Sprintf("%s scored level %s, which routes to a person", j.Judge, j.Level))
		}
	}
	if p.Reading == nil {
		because = append(because, "Jev scored no convergence, so nothing measured whether the two judges agree")
	} else if fault := Unusable(p.Reading); fault != "" {
		because = append(because, fault)
	}
	if len(because) > 0 {
		return Decision{Route: RoutePerson, Because: because}
	}
	conv := Agreement(p.Reading)
	if agreed(p.Judges) {
		return Decision{Route: RouteLoop, Convergence: conv, Confidence: p.Reading.Confidence, Score: p.Reading.Score}
	}
	if conv < th.Convergence {
		because = append(because, fmt.Sprintf("Jev puts %.2f of its belief on the two judges agreeing, under the threshold %.2f", conv, th.Convergence))
	}
	if p.Reading.Confidence < th.Confidence {
		because = append(because, fmt.Sprintf("Jev's confidence reads %.2f, under the threshold %.2f", p.Reading.Confidence, th.Confidence))
	}
	if len(because) > 0 {
		return Decision{Route: RoutePerson, Because: because}
	}
	return Decision{Route: RouteLoop, Convergence: conv, Confidence: p.Reading.Confidence, Score: p.Reading.Score}
}

// agreed reports whether both judges gave one answer: one level and one
// gate answer.
func agreed(js []JudgeAnswer) bool {
	if len(js) != JudgesRequired {
		return false
	}
	a, b := js[0], js[1]
	la, _ := LevelFor(a.Level)
	lb, _ := LevelFor(b.Level)
	return la.Ordinal == lb.Ordinal && a.Choice == b.Choice
}

// DecideAll routes an escalation: it reaches a person when one of its
// findings does.
func DecideAll(ps []Panel, th Thresholds) Decision {
	out := Decision{Route: RouteLoop}
	for i, p := range ps {
		d := Decide(p, th)
		if d.Route == RoutePerson {
			out.Route = RoutePerson
			for _, b := range d.Because {
				out.Because = append(out.Because, fmt.Sprintf("finding %d: %s", i+1, b))
			}
		}
	}
	return out
}

// EncodePanels writes the panel file `escalation route` reads.
func EncodePanels(ps []Panel) (string, error) {
	raw, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw) + "\n", nil
}

// ParsePanels reads a panel file: one panel object, or an array with one
// panel for each finding. It refuses an answer the rubric cannot read.
func ParsePanels(raw string) ([]Panel, error) {
	raw = strings.TrimSpace(raw)
	var ps []Panel
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &ps); err != nil {
			return nil, fmt.Errorf("the panel file is not a JSON array of panels: %w", err)
		}
	} else {
		var p Panel
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, fmt.Errorf("the panel file is not a JSON panel: %w", err)
		}
		ps = []Panel{p}
	}
	if len(ps) == 0 {
		return nil, errors.New("the panel file holds no panel")
	}
	for i, p := range ps {
		if strings.TrimSpace(p.Finding) == "" {
			return nil, fmt.Errorf("panel %d names no finding", i+1)
		}
		for _, j := range p.Judges {
			if _, ok := LevelFor(j.Level); !ok {
				return nil, fmt.Errorf("panel %d: judge %s scored %q, which is no level of the rubric", i+1, j.Judge, j.Level)
			}
			if j.Choice != Inside && j.Choice != Outside {
				return nil, fmt.Errorf("panel %d: judge %s answered the gate %q, which is no option", i+1, j.Judge, j.Choice)
			}
		}
	}
	return ps, nil
}
