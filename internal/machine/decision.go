package machine

import (
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

// Decision states. scripts/sdlc/decision-states.ts (alpha) names them
// Decision Pending and Needs Human.
const (
	DecisionPending = "pending"
	NeedsHuman      = "needs_human"
)

// decisionTable ports DECISION_MOVES (scripts/sdlc/decision-states.ts:95,
// alpha) unchanged: each move, where it starts, where it lands, and the
// claim its record makes. Only answer writes done, and done means a person
// answered. settle leaves the person named, and moot names nobody. Each
// row also records its text (ADR 0005, D6): a fire or a defer its why, an
// answer the answer, and a drop or a moot its note.
func decisionTable() *Table {
	states := []State{
		{Name: DecisionPending, Category: beadsx.CategoryFrozen},
		{Name: NeedsHuman, Category: beadsx.CategoryActive},
		{Name: Done, Category: beadsx.CategoryDone, Terminal: true},
		{Name: Canceled, Category: beadsx.CategoryDone, Terminal: true},
	}
	rows := []Row{
		{Verb: VerbFile, From: []string{Initial}, To: DecisionPending, Command: "decision file", Says: "Filed."},
		{Verb: VerbFire, From: []string{DecisionPending}, To: NeedsHuman, Command: "decision fire", Says: "The trigger fired.",
			set: all(needCopy("why", FactWhy), deferReason), Facts: []string{FactWhy, FactDeferReason}},
		{Verb: VerbDefer, From: []string{NeedsHuman}, To: DecisionPending, Command: "decision defer", Says: "The trigger did not fire.",
			set: needCopy("why", FactWhy), Facts: []string{FactWhy}},
		{Verb: VerbAnswer, From: []string{NeedsHuman}, To: Done, Command: "decision settle", Says: "Answered.",
			set: all(needAnswer, answerFacts), Facts: []string{FactAnswer}},
		{Verb: "drop", From: []string{NeedsHuman}, To: Canceled, Command: "decision settle", Says: "Dropped.",
			set: needCopy("note", FactCloseNote), Facts: []string{FactCloseNote}},
		{Verb: "moot", From: []string{DecisionPending, NeedsHuman}, To: Canceled, Command: "decision moot",
			Says: "It stopped being a question.", set: needCopy("note", FactCloseNote), Facts: []string{FactCloseNote}},
	}
	return &Table{Kind: KindDecision, BeadType: "decision", States: states, Rows: rows}
}

// deferReason keeps the reason of the inbox defer that this fire ends, and
// clears the one of an earlier fire, so the fact always names the latest
// fire (spec Decision 19).
func deferReason(c *change) *cli.Refusal {
	if v := c.param("defer_reason"); v != "" {
		c.setFact(FactDeferReason, v)
	} else {
		c.unsetFact(FactDeferReason)
	}
	return nil
}

// needAnswer refuses a settle that records no answer (spec Decision 21).
func needAnswer(c *change) *cli.Refusal { return c.needParam("answer") }
