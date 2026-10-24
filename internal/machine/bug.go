package machine

import (
	"fmt"
	"strconv"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

// Bug states. A raw report becomes a shaped ticket, a duplicate or an
// escalation, behind the optional reproducer stage: a project without
// the stage shapes a report straight from reported. A report waits in
// asked while its reporter answers the reproducer's question.
const (
	Reported    = "reported"
	Reproducing = "reproducing"
	Asked       = "asked"
	Shaped      = "shaped"
	Duplicate   = "duplicate"
)

// AskLimit is the asks of its reporter one report may hold
// (scripts/sdlc/bugs-report-ask.ts:41, alpha). Two asks that did not find
// the fact show that only a person can find it, so the third ask refuses,
// and the reproducer escalates at level 5.
const AskLimit = 2

// askPut stores the reproducer's question and counts it. The reporter's
// answer keeps the count, so a report asks at most AskLimit times.
func askPut(c *change) *cli.Refusal {
	if r := c.needParam("body"); r != nil {
		return r
	}
	n := count(c, FactAsks)
	if n >= AskLimit {
		return &cli.Refusal{Code: cli.CodeAskLimit, Rule: "ask_limit", Item: c.item.ID, CurrentStatus: c.item.Status,
			AllowedNext: c.table.AllowedNext(c.item.Status),
			Hint:        fmt.Sprintf("%s already asked its reporter %d times; escalate the missing fact at level 5 with loop escalation raise", c.item.ID, n)}
	}
	c.setFact(FactAsks, strconv.Itoa(n+1))
	c.setFact(FactQuestion, c.param("body"))
	c.setFact(FactAskedBy, c.req.Actor.Name)
	return nil
}

// answerAsk stores the reporter's answer, and the report returns to the
// reproducer.
func answerAsk(c *change) *cli.Refusal {
	c.unsetFact(FactQuestion, FactAskedBy)
	return answerFacts(c)
}

func bugResume(string) (string, string) { return Reported, "" }

func bugTable() *Table {
	states := []State{
		{Name: Reported, Category: beadsx.CategoryActive},
		{Name: Reproducing, Category: beadsx.CategoryWIP},
		{Name: Asked, Category: beadsx.CategoryFrozen},
		{Name: Escalated, Category: beadsx.CategoryFrozen},
		{Name: Shaped, Category: beadsx.CategoryDone, Terminal: true},
		{Name: Duplicate, Category: beadsx.CategoryDone, Terminal: true},
		{Name: Canceled, Category: beadsx.CategoryDone, Terminal: true},
	}
	open := []string{Reported, Reproducing}
	rows := []Row{
		{Verb: VerbFile, From: []string{Initial}, To: Reported},
		{Verb: "claim", From: []string{Reported}, To: Reproducing, Stage: StageReproducer, when: stageIs(StageReproducer),
			guard: claimGuard, BumpsEpoch: true, Lease: LeaseTake, Facts: []string{FactOwner, FactStage}},
		{Verb: "shape", From: open, To: Shaped, Command: "work shape", Lease: LeaseDrop, set: needCopy("ticket", FactTicket),
			Facts: []string{FactTicket}},
		{Verb: "duplicate", From: open, To: Duplicate, Command: "work duplicate", Lease: LeaseDrop, set: needCopy("of", FactDuplicateOf),
			Facts: []string{FactDuplicateOf}},
		{Verb: VerbEscalate, From: open, To: Escalated, Lease: LeaseDrop, set: escalateTo(bugResume), Facts: []string{FactResume, FactEscalation}},
		{Verb: VerbAnswer, From: []string{Escalated}, To: ToResume, ResumeTo: []string{Reported}, set: answerResume,
			Facts: []string{FactResume, FactEscalation, FactAnswer, FactAmendment}},
		// The reproducer asks the reporter for a fact only the reporter
		// holds. The inbox relays the question, and the answer returns the
		// report to the reproducer.
		{Verb: "ask", From: []string{Reproducing}, To: Asked, Command: "work ask", Lease: LeaseDrop, set: askPut,
			Facts: []string{FactAsks, FactQuestion, FactAskedBy}},
		{Verb: VerbAnswer, From: []string{Asked}, To: Reported, set: answerAsk,
			Facts: []string{FactQuestion, FactAskedBy, FactAnswer, FactAmendment}},
		// The reproducer cancels a report whose symptom is in the loop, after
		// it reports the loop fault. Released to reported, the report
		// would only run again.
		{Verb: "cancel", From: []string{Reproducing, Escalated}, To: Canceled, Command: "work cancel", Lease: LeaseDrop},
		{Verb: VerbNote, From: []string{Reported, Reproducing, Asked, Escalated}, To: ToSame, Command: "work note", set: notePut,
			Facts: []string{FactNote}},
		editRow([]string{Reported, Reproducing, Asked, Escalated}),
	}
	pairs := [][2]string{{Reproducing, Reported}}
	rows = append(rows, releaseRows("release", false, pairs, nil)...)
	rows = append(rows, releaseRows("reclaim", true, pairs, nil)...)
	return &Table{Kind: KindBug, BeadType: "bug", States: states, Rows: rows}
}
