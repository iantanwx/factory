package machine

import "github.com/iantanwx/loop/internal/beadsx"

// Decision states. scripts/sdlc/decision-states.ts (alpha) names them
// Decision Pending and Needs Human.
const (
	DecisionPending = "pending"
	NeedsHuman      = "needs_human"
)

// decisionTable ports DECISION_MOVES (scripts/sdlc/decision-states.ts:95,
// alpha) unchanged: each move, where it starts, where it lands, and the
// claim its record makes. Only answer writes done, and done means a person
// answered. settle leaves the person named, and moot names nobody.
func decisionTable() *Table {
	states := []State{
		{Name: DecisionPending, Category: beadsx.CategoryFrozen},
		{Name: NeedsHuman, Category: beadsx.CategoryActive},
		{Name: Done, Category: beadsx.CategoryDone, Terminal: true},
		{Name: Canceled, Category: beadsx.CategoryDone, Terminal: true},
	}
	rows := []Row{
		{Verb: VerbFile, From: []string{Initial}, To: DecisionPending, Command: "decision file", Says: "Filed."},
		{Verb: VerbFire, From: []string{DecisionPending}, To: NeedsHuman, Command: "decision fire", Says: "The trigger fired."},
		{Verb: VerbDefer, From: []string{NeedsHuman}, To: DecisionPending, Command: "decision defer", Says: "The trigger did not fire."},
		{Verb: VerbAnswer, From: []string{NeedsHuman}, To: Done, Command: "decision settle", Says: "Answered.",
			set: answerFacts, Facts: []string{FactAnswer}},
		{Verb: "drop", From: []string{NeedsHuman}, To: Canceled, Command: "decision settle", Says: "Dropped."},
		{Verb: "moot", From: []string{DecisionPending, NeedsHuman}, To: Canceled, Command: "decision moot",
			Says: "It stopped being a question."},
	}
	return &Table{Kind: KindDecision, BeadType: "decision", States: states, Rows: rows}
}
