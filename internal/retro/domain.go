package retro

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/escalation"
)

// Target is where a learning lands, the rubric's Q2.
type Target string

const (
	LoopFault      Target = "loop_fault"
	PackPR         Target = "pack_pr"
	ProjectPolicy  Target = "project_policy"
	UserspaceSkill Target = "userspace_skill"
	ProjectTicket  Target = "project_ticket"
)

// targetRule is what the route reads of one target.
type targetRule struct {
	sizeApplies     bool // Q3 asks how large the change is
	loopProjectOnly bool // it lands only in the loop's own project
}

var targets = map[Target]targetRule{
	LoopFault:      {},
	PackPR:         {sizeApplies: true, loopProjectOnly: true},
	ProjectPolicy:  {sizeApplies: true},
	UserspaceSkill: {sizeApplies: true},
	ProjectTicket:  {},
}

// Evidence is how many cases a judge reads behind a learning, the rubric's
// Q1.
type Evidence string

const (
	One       Evidence = "one"
	TwoOrMore Evidence = "two_or_more"
)

// Change is how large a judge reads the change, the rubric's Q3. NA is the
// answer for a target Q3 does not apply to.
type Change string

const (
	Edit        Change = "edit"
	Extend      Change = "extend"
	Add         Change = "add"
	Restructure Change = "restructure"
	NA          Change = "n/a"
)

var changes = []Change{Edit, Extend, Add, Restructure, NA}

// Lands reports whether a change this size lands with no person.
func (c Change) Lands() bool { return c == Edit || c == Extend }

// Answer is one judge's answer to the three questions about one learning.
type Answer struct {
	Judge    string   `json:"judge"`
	Target   Target   `json:"target"`
	Evidence Evidence `json:"evidence"`
	Change   Change   `json:"change"`
	Reason   string   `json:"reason"`
}

// ParseAnswer reads one judge's answer. It refuses an unknown value, and a
// change other than n/a exactly where Q3 does not apply.
func ParseAnswer(raw []byte) (Answer, error) {
	var a Answer
	if err := json.Unmarshal(raw, &a); err != nil {
		return Answer{}, fmt.Errorf("retro: an answer is no JSON answer: %w", err)
	}
	rule, ok := targets[a.Target]
	switch {
	case strings.TrimSpace(a.Judge) == "":
		return Answer{}, errors.New("retro: an answer names no judge")
	case !ok:
		return Answer{}, fmt.Errorf("retro: judge %s named target %q, which is no target", a.Judge, a.Target)
	case a.Evidence != One && a.Evidence != TwoOrMore:
		return Answer{}, fmt.Errorf("retro: judge %s answered evidence %q, which is neither one nor two_or_more", a.Judge, a.Evidence)
	case !slices.Contains(changes, a.Change):
		return Answer{}, fmt.Errorf("retro: judge %s answered change %q, which is no change size", a.Judge, a.Change)
	case rule.sizeApplies && a.Change == NA:
		return Answer{}, fmt.Errorf("retro: judge %s answered n/a for %s, and Q3 applies to it", a.Judge, a.Target)
	case !rule.sizeApplies && a.Change != NA:
		return Answer{}, fmt.Errorf("retro: judge %s answered %s for %s, and Q3 takes only n/a there", a.Judge, a.Change, a.Target)
	}
	return a, nil
}

// Learning is what a stage, skill or policy must do differently, and the
// cases behind it. Extends names a held learning, L-<n>, whose cases this
// one counts again.
type Learning struct {
	Statement string    `json:"statement"`
	Cases     []CaseRef `json:"cases"`
	Extends   string    `json:"extends,omitempty"`
}

// Panel is one learning, both judges' answers and Jev's reading, or none.
type Panel struct {
	Learning Learning            `json:"learning"`
	Answers  []Answer            `json:"answers"`
	Reading  *escalation.Reading `json:"reading"`
}

// ParsePanels reads a panel file. It refuses a learning with no statement
// or no case, a case Parse refuses, and an answer ParseAnswer refuses. A
// file with no panel is a retro that found no learning.
func ParsePanels(raw []byte) ([]Panel, error) {
	var f struct {
		Panels []struct {
			Learning Learning            `json:"learning"`
			Answers  []json.RawMessage   `json:"answers"`
			Reading  *escalation.Reading `json:"reading"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("retro: the panel file is no JSON object of panels: %w", err)
	}
	out := make([]Panel, 0, len(f.Panels))
	for i, fp := range f.Panels {
		l := fp.Learning
		if strings.TrimSpace(l.Statement) == "" {
			return nil, fmt.Errorf("retro: panel %d: the learning has no statement", i+1)
		}
		if len(l.Cases) == 0 {
			return nil, fmt.Errorf("retro: panel %d: the learning names no case", i+1)
		}
		for _, c := range l.Cases {
			if _, err := Parse(string(c)); err != nil {
				return nil, fmt.Errorf("panel %d: %w", i+1, err)
			}
		}
		p := Panel{Learning: l, Reading: fp.Reading}
		for _, ra := range fp.Answers {
			a, err := ParseAnswer(ra)
			if err != nil {
				return nil, fmt.Errorf("panel %d: %w", i+1, err)
			}
			p.Answers = append(p.Answers, a)
		}
		out = append(out, p)
	}
	return out, nil
}

// Route is where one learning goes: Land, Hold or Escalate.
type Route interface{ route() }

// Land lands the learning at its target with no person.
type Land struct{ Target Target }

// Hold keeps the learning until a second case.
type Hold struct{}

// Escalate sends the learning to the operator.
type Escalate struct{ Reason Reason }

func (Land) route()     {}
func (Hold) route()     {}
func (Escalate) route() {}

// Reason is why a learning escalates.
type Reason string

const (
	ReasonJudges      Reason = "judges"
	ReasonNoReading   Reason = "no_reading"
	ReasonTargets     Reason = "targets"
	ReasonPackOutside Reason = "pack_outside"
	ReasonSize        Reason = "size"
	ReasonThreshold   Reason = "threshold"
)

// LearningStatus is a stored learning's state. It decides the route, so no
// field stores one.
type LearningStatus string

const (
	StatusHeld      LearningStatus = "held"      // waits for a second case
	StatusCounted   LearningStatus = "counted"   // a later learning extends it
	StatusLanded    LearningStatus = "landed"    // terminal
	StatusEscalated LearningStatus = "escalated" // waits for the operator
	StatusAnswered  LearningStatus = "answered"  // the operator answered
)

// Route derives the route of a learning in s, with the row's target and
// reason.
func (s LearningStatus) Route(t Target, r Reason) Route {
	switch s {
	case StatusLanded:
		return Land{Target: t}
	case StatusEscalated, StatusAnswered:
		return Escalate{Reason: r}
	}
	return Hold{}
}
