package retro

import (
	"fmt"
	"testing"

	"github.com/iantanwx/loop/internal/escalation"
)

// sure is a reading that clears both default thresholds.
func sure() *escalation.Reading {
	return &escalation.Reading{Probabilities: map[string]float64{"0": 0, "1": 0.05, "2": 0.15, "3": 0.8}, Score: 2.75, Confidence: 0.95}
}

// edge is a reading whose agreement and confidence are given.
func edge(agree, confidence float64) *escalation.Reading {
	return &escalation.Reading{Probabilities: map[string]float64{"0": 0, "1": 1 - agree, "2": 0, "3": agree}, Score: 2, Confidence: confidence}
}

func ans(judge string, t Target, e Evidence, c Change) Answer {
	return Answer{Judge: judge, Target: t, Evidence: e, Change: c, Reason: judge + " read the record"}
}

// pair is two judges who answer one target and one size, with two or more
// cases.
func pair(t Target, c Change) []Answer {
	return []Answer{ans("fable-5", t, TwoOrMore, c), ans("opus-5", t, TwoOrMore, c)}
}

// sized is the change a judge names for t when the size reads as edit.
func sized(t Target) Change {
	if t == LoopFault || t == ProjectTicket {
		return NA
	}
	return Edit
}

var (
	loopHome  = RouteContext{LoopProject: true, Thresholds: escalation.DefaultThresholds}
	elsewhere = RouteContext{LoopProject: false, Thresholds: escalation.DefaultThresholds}
	allTarget = []Target{LoopFault, PackPR, ProjectPolicy, UserspaceSkill, ProjectTicket}
	sizedOnes = []Target{PackPR, ProjectPolicy, UserspaceSkill}
)

type decideRow struct {
	name    string
	answers []Answer
	reading *escalation.Reading
	ctx     RouteContext
	want    Route
}

func TestDecide(t *testing.T) {
	rows := []decideRow{
		// Rule 1: two distinct judges, or the operator.
		{"one judge", pair(ProjectPolicy, Edit)[:1], sure(), loopHome, Escalate{Reason: ReasonJudges}},
		{"one judge twice", []Answer{ans("fable-5", ProjectPolicy, TwoOrMore, Edit), ans("fable-5", ProjectPolicy, TwoOrMore, Edit)},
			sure(), loopHome, Escalate{Reason: ReasonJudges}},
		{"no judge", nil, sure(), loopHome, Escalate{Reason: ReasonJudges}},

		// C11: either judge answers one, so the learning waits, whatever else.
		{"C11 first judge one", []Answer{ans("fable-5", ProjectPolicy, One, Edit), ans("opus-5", ProjectPolicy, TwoOrMore, Edit)},
			sure(), loopHome, Hold{}},
		{"C11 second judge one", []Answer{ans("fable-5", LoopFault, TwoOrMore, NA), ans("opus-5", LoopFault, One, NA)},
			sure(), loopHome, Hold{}},
		{"C11 one with different targets, a large change, no reading, outside", []Answer{
			ans("fable-5", PackPR, One, Restructure), ans("opus-5", ProjectTicket, TwoOrMore, NA)},
			nil, elsewhere, Hold{}},
		{"C11 one under both thresholds", []Answer{ans("fable-5", UserspaceSkill, One, Add), ans("opus-5", UserspaceSkill, One, Add)},
			edge(0.1, 0.1), loopHome, Hold{}},

		// C16: no usable reading reaches the operator.
		{"C16 no reading", pair(ProjectPolicy, Edit), nil, loopHome, Escalate{Reason: ReasonNoReading}},
		{"C16 three levels", pair(ProjectPolicy, Edit),
			&escalation.Reading{Probabilities: map[string]float64{"0": 0.05, "1": 0.15, "2": 0.8}, Confidence: 0.95},
			loopHome, Escalate{Reason: ReasonNoReading}},
		{"C16 probabilities add to 0.9", pair(ProjectPolicy, Edit),
			&escalation.Reading{Probabilities: map[string]float64{"0": 0, "1": 0.05, "2": 0.05, "3": 0.8}, Confidence: 0.95},
			loopHome, Escalate{Reason: ReasonNoReading}},

		// C18: a pack PR lands only in the loop's own project.
		{"C18 pack PR outside", pair(PackPR, Edit), sure(), elsewhere, Escalate{Reason: ReasonPackOutside}},
		{"C18 pack PR outside with a large change", pair(PackPR, Restructure), sure(), elsewhere, Escalate{Reason: ReasonPackOutside}},
		{"C18 pack PR at home", pair(PackPR, Extend), sure(), loopHome, Land{Target: PackPR}},

		// C27: Q3 reads nothing for a loop fault or a project ticket.
		{"C27 loop fault lands", pair(LoopFault, NA), sure(), loopHome, Land{Target: LoopFault}},
		{"C27 project ticket lands", pair(ProjectTicket, NA), sure(), elsewhere, Land{Target: ProjectTicket}},

		// C15: the edge of each threshold, read from the context.
		{"C15 both at the edge", pair(ProjectPolicy, Edit), edge(0.85, 0.85), loopHome, Land{Target: ProjectPolicy}},
		{"C15 agreement under", pair(ProjectPolicy, Edit), edge(0.849, 0.85), loopHome, Escalate{Reason: ReasonThreshold}},
		{"C15 confidence under", pair(ProjectPolicy, Edit), edge(0.85, 0.849), loopHome, Escalate{Reason: ReasonThreshold}},
		{"C15 the context's threshold, not the default", pair(ProjectPolicy, Edit), edge(0.9, 0.9),
			RouteContext{LoopProject: true, Thresholds: escalation.Thresholds{Convergence: 0.95, Confidence: 0.5}},
			Escalate{Reason: ReasonThreshold}},
		{"C15 a lower context threshold lands", pair(ProjectPolicy, Edit), edge(0.6, 0.6),
			RouteContext{LoopProject: true, Thresholds: escalation.Thresholds{Convergence: 0.5, Confidence: 0.5}},
			Land{Target: ProjectPolicy}},
	}

	// C13: every ordered pair of distinct targets.
	for _, a := range allTarget {
		for _, b := range allTarget {
			if a == b {
				continue
			}
			rows = append(rows, decideRow{fmt.Sprintf("C13 %s then %s", a, b),
				[]Answer{ans("fable-5", a, TwoOrMore, sized(a)), ans("opus-5", b, TwoOrMore, sized(b))},
				sure(), loopHome, Escalate{Reason: ReasonTargets}})
		}
	}

	// C14: each size pair on each target Q3 applies to. add or restructure
	// from either judge escalates, and edit or extend from both lands.
	sizes := []Change{Edit, Extend, Add, Restructure}
	for _, tg := range sizedOnes {
		for _, a := range sizes {
			for _, b := range sizes {
				var want Route = Land{Target: tg}
				if a == Add || a == Restructure || b == Add || b == Restructure {
					want = Escalate{Reason: ReasonSize}
				}
				rows = append(rows, decideRow{fmt.Sprintf("C14 %s %s and %s", tg, a, b),
					[]Answer{ans("fable-5", tg, TwoOrMore, a), ans("opus-5", tg, TwoOrMore, b)},
					sure(), loopHome, want})
			}
		}
	}

	for _, r := range rows {
		p := Panel{Learning: Learning{Statement: "the implementer runs the fast gates", Cases: []CaseRef{"run:r-1", "run:r-2"}},
			Answers: r.answers, Reading: r.reading}
		if got := Decide(p, r.ctx); got != r.want {
			t.Errorf("%s: Decide = %#v, want %#v", r.name, got, r.want)
		}
	}
}
