package machine

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/shape"
)

// Owed is one line that a code verdict on a ticket owes, as
// `review checklist` lists it. A stop condition also names each operator
// answer that a waived line may cite.
type Owed struct {
	shape.Owed
	Answers []string `json:"answers,omitempty"`
}

// stopFiredSays is what the history, and so the inbox, records for a code
// review that found a stop condition fired.
const stopFiredSays = "The code-reviewer found a stop condition of this ticket fired, and only a person waives one. " +
	"The review fact names the condition and the evidence. An answer that waives it lets the next review mark it waived."

// checklistOf returns the lines a code verdict on it owes. ok is false for
// an item that owes no checklist: an epic's document, or a ticket with no
// shape fact, which was never checked at its creation.
func checklistOf(it Item) (owed []shape.Owed, ok bool) {
	s, ok := shape.ByName(it.Fact(FactShape))
	if it.Kind != KindTicket || !ok {
		return nil, false
	}
	return shape.Checklist(s, it.Body), true
}

// firedStops returns each stop condition of it that review marks fail.
func firedStops(it Item, review string) []string {
	owed, _ := checklistOf(it)
	var out []string
	for _, c := range shape.Checks(review) {
		if c.Result == shape.Fail && isStop(c.ID) && slices.ContainsFunc(owed, func(o shape.Owed) bool { return o.ID == c.ID }) &&
			!slices.Contains(out, c.ID) {
			out = append(out, c.ID)
		}
	}
	return out
}

func isStop(id string) bool { return strings.HasPrefix(id, "stop") }

// stopFired selects the reject row that sends the ticket to a person.
func stopFired(c *change) bool { return len(firedStops(c.item, c.param("body"))) > 0 }

func recordStops(c *change) *cli.Refusal {
	c.setFact(FactStopsFired, strings.Join(firedStops(c.item, c.param("body")), ","))
	return nil
}

// answerName is how a checklist line names one operator answer.
func answerName(t Transition) string { return fmt.Sprintf("answer-%d", t.Seq) }

// standingAnswers names each operator answer on the item that a waived
// stop line may cite: each answer recorded after the latest code review
// that found stop fired. A newer such review supersedes every answer
// before it.
func standingAnswers(hist []Transition, stop string) []string {
	var out []string
	for _, t := range hist {
		switch {
		case slices.Contains(strings.Split(t.Fact(FactStopsFired), ","), stop):
			out = nil
		case t.Verb == VerbAnswer && t.Fact(FactAnswer) != "":
			out = append(out, answerName(t))
		}
	}
	return out
}

// checklistHeld refuses a code verdict on a shaped ticket whose review
// lacks a line with evidence for an owed id, marks waived a line that no
// standing operator answer waives, or passes with a fail or an extra
// finding (alpha review-checklist.ts, review-waiver.ts).
func (m *Machine) checklistHeld(ctx context.Context, it Item, verdict, review string) *cli.Refusal {
	owed, ok := checklistOf(it)
	if !ok {
		return nil
	}
	var checks []shape.Check
	held := map[string]bool{}
	for _, c := range shape.Checks(review) {
		if c.Evidence != "" && (strings.HasPrefix(c.ID, "X") || slices.ContainsFunc(owed, func(o shape.Owed) bool { return o.ID == c.ID })) {
			checks = append(checks, c)
			held[c.ID] = true
		}
	}
	var missing []string
	for _, o := range owed {
		if !held[o.ID] {
			missing = append(missing, o.ID)
		}
	}
	if len(missing) > 0 {
		return &cli.Refusal{Code: cli.CodeShapeRefused, Rule: "checklist_incomplete",
			Hint: fmt.Sprintf("the review holds no checklist line for %s; write one line for each id that `loop review checklist` names, "+
				"such as \"- %s pass: <evidence>\", and submit again", strings.Join(missing, ", "), missing[0])}
	}
	var unwaived []string
	var hist []Transition
	for _, c := range checks {
		if c.Result != shape.Waived {
			continue
		}
		if !isStop(c.ID) {
			unwaived = append(unwaived, c.ID+" is no stop condition, and only a stop condition is waived")
			continue
		}
		if hist == nil {
			var err error
			if hist, err = m.History(ctx, it.Project, it.ID); err != nil {
				return cli.InternalRefusal(err)
			}
		}
		if !slices.ContainsFunc(standingAnswers(hist, c.ID), func(a string) bool {
			return regexp.MustCompile(`\b` + a + `\b`).MatchString(c.Evidence)
		}) {
			unwaived = append(unwaived, c.ID+" names no standing operator answer")
		}
	}
	if len(unwaived) > 0 {
		return &cli.Refusal{Code: cli.CodeShapeRefused, Rule: "checklist_waiver",
			Hint: strings.Join(unwaived, "; ") + "; a waived line names an answer that `loop review checklist` lists for its stop condition. " +
				"Mark it pass or fail, and submit again"}
	}
	if verdict != "review_pass" {
		return nil
	}
	var fails []string
	for _, c := range checks {
		if c.Result == shape.Fail || strings.HasPrefix(c.ID, "X") {
			fails = append(fails, c.ID)
		}
	}
	if len(fails) > 0 {
		return &cli.Refusal{Code: cli.CodeShapeRefused, Rule: "checklist_fail",
			Hint: fmt.Sprintf("a pass holds no fail and no extra finding, and %s holds one; submit a reject. "+
				"A reject whose stop condition fails goes to a person", strings.Join(fails, ", "))}
	}
	return nil
}

// reviewChecklist is `review checklist`: each line a code verdict on the
// item owes. An item that owes no checklist answers NOTHING.
func (m *Machine) reviewChecklist(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	it, r := m.Read(ctx, prj, call.Inv.Arg("item"))
	if r != nil {
		return r.Result()
	}
	owed, _ := checklistOf(it)
	var hist []Transition
	if slices.ContainsFunc(owed, func(o shape.Owed) bool { return isStop(o.ID) }) {
		var err error
		if hist, err = m.History(ctx, prj, it.ID); err != nil {
			return cli.Internal(err)
		}
	}
	out := make([]Owed, 0, len(owed))
	for _, o := range owed {
		e := Owed{Owed: o}
		if isStop(o.ID) {
			e.Answers = standingAnswers(hist, o.ID)
		}
		out = append(out, e)
	}
	return cli.List(out)
}
