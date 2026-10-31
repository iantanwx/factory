package retro

import (
	"slices"

	"github.com/iantanwx/loop/internal/escalation"
)

// RouteContext is what the server knows that the panel does not.
// LoopProject is true when the retro's project is the loop's own.
// Thresholds are the server's, never the panel's.
type RouteContext struct {
	LoopProject bool
	Thresholds  escalation.Thresholds
}

// Decide routes one panel. It applies the route table of the retro spec in
// order, and the first rule that holds decides. A held learning never
// escalates, and no carve-out skips the thresholds.
func Decide(p Panel, c RouteContext) Route {
	judges := map[string]bool{}
	for _, a := range p.Answers {
		judges[a.Judge] = true
	}
	if len(judges) < escalation.JudgesRequired {
		return Escalate{Reason: ReasonJudges}
	}
	if slices.ContainsFunc(p.Answers, func(a Answer) bool { return a.Evidence == One }) {
		return Hold{}
	}
	if p.Reading == nil || escalation.Unusable(p.Reading) != "" {
		return Escalate{Reason: ReasonNoReading}
	}
	t := p.Answers[0].Target
	if slices.ContainsFunc(p.Answers, func(a Answer) bool { return a.Target != t }) {
		return Escalate{Reason: ReasonTargets}
	}
	if targets[t].loopProjectOnly && !c.LoopProject {
		return Escalate{Reason: ReasonPackOutside}
	}
	if targets[t].sizeApplies && slices.ContainsFunc(p.Answers, func(a Answer) bool { return !a.Change.Lands() }) {
		return Escalate{Reason: ReasonSize}
	}
	if escalation.Agreement(p.Reading) < c.Thresholds.Convergence || p.Reading.Confidence < c.Thresholds.Confidence {
		return Escalate{Reason: ReasonThreshold}
	}
	return Land{Target: t}
}
