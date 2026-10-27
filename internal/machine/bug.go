package machine

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

// Bug states. A raw report becomes a shaped ticket, a duplicate or an
// escalation, behind the optional reproducer stage: a project without
// the stage shapes a report straight from reported. A report waits in
// asked while its reporter answers the reproducer's question, and in
// awaiting_build while a ticket or an epic builds what it asks for.
const (
	Reported      = "reported"
	Reproducing   = "reproducing"
	Asked         = "asked"
	AwaitingBuild = "awaiting_build"
	Shaped        = "shaped"
	Duplicate     = "duplicate"
)

// AskLimit is the asks of its reporter one report may hold
// (scripts/sdlc/bugs-report-ask.ts:41, alpha). Two asks that did not find
// the fact show that only a person can find it, so the third ask refuses,
// and the reproducer escalates at level 5.
const AskLimit = 2

// AskFact names the fact of one round of asks: question_<n> holds the
// reproducer's nth ask and answer_<n> the reporter's answer to it. Every
// round stays on the report, as each ask and reply stays on the issue
// thread in alpha, so a later brief holds each question beside its answer.
func AskFact(name string, n int) string { return name + "_" + strconv.Itoa(n) }

// askFacts lists the facts of every round, for the table's schema.
func askFacts(name string) []string {
	out := make([]string, 0, AskLimit)
	for n := 1; n <= AskLimit; n++ {
		out = append(out, AskFact(name, n))
	}
	return out
}

// askPut stores the reproducer's question as the next round and counts
// it. The reporter's answer keeps the count, so a report asks at most
// AskLimit times.
func askPut(c *change) *cli.Refusal {
	if r := c.needParam("body"); r != nil {
		return r
	}
	n := count(c, FactAsks)
	if n >= AskLimit {
		return &cli.Refusal{Code: cli.CodeAskLimit, Rule: "ask_limit", Item: c.item.ID, CurrentStatus: c.item.Status,
			AllowedNext: slices.DeleteFunc(c.table.AllowedNext(c.item.Status), func(v string) bool { return v == c.row.Verb }),
			Hint:        fmt.Sprintf("%s already asked its reporter %d times; escalate the missing fact at level 5 with loop escalation raise", c.item.ID, n)}
	}
	c.setFact(FactAsks, strconv.Itoa(n+1))
	c.setFact(AskFact(FactQuestion, n+1), c.param("body"))
	c.setFact(FactAskedBy, c.req.Actor.Name)
	return nil
}

// answerAsk stores the reporter's answer beside the question of its
// round, and the report returns to the reproducer. An answer to an ask
// amends no task, so it never enters the inputs.
func answerAsk(c *change) *cli.Refusal {
	if a := c.param("answer"); a != "" {
		c.setFact(AskFact(FactAnswer, count(c, FactAsks)), a)
		c.setFact(FactAnswer, a)
	}
	c.unsetFact(FactAskedBy)
	return nil
}

func bugResume(string) (string, string) { return Reported, "" }

// buildLanded closes a waiting report as a duplicate of the build that
// answered it (D9).
func buildLanded(c *change) *cli.Refusal {
	c.setFact(FactDuplicateOf, c.fact(FactBuiltBy))
	return nil
}

// buildCanceled returns a waiting report to the reproducer: the build that
// would answer it never lands.
func buildCanceled(c *change) *cli.Refusal {
	c.unsetFact(FactBuiltBy)
	return nil
}

func bugTable() *Table {
	states := []State{
		{Name: Reported, Category: beadsx.CategoryActive},
		{Name: Reproducing, Category: beadsx.CategoryWIP},
		{Name: Asked, Category: beadsx.CategoryFrozen},
		{Name: AwaitingBuild, Category: beadsx.CategoryFrozen},
		{Name: Escalated, Category: beadsx.CategoryFrozen},
		{Name: Shaped, Category: beadsx.CategoryDone, Terminal: true},
		{Name: Duplicate, Category: beadsx.CategoryDone, Terminal: true},
		{Name: Canceled, Category: beadsx.CategoryDone, Terminal: true},
	}
	open := []string{Reported, Reproducing}
	// A waiting report has no lease, and the coordinator may still route
	// it by hand.
	routable := []string{Reported, Reproducing, AwaitingBuild}
	rows := []Row{
		{Verb: VerbFile, From: []string{Initial}, To: Reported},
		{Verb: "claim", From: []string{Reported}, To: Reproducing, Stage: StageReproducer, when: stageIs(StageReproducer),
			guard: claimGuard, BumpsEpoch: true, Lease: LeaseTake, Facts: []string{FactOwner, FactStage}},
		{Verb: "shape", From: routable, To: Shaped, Command: "work shape", Lease: LeaseDrop, set: needCopy("ticket", FactTicket),
			Facts: []string{FactTicket}},
		{Verb: "duplicate", From: routable, To: Duplicate, Command: "work duplicate", Lease: LeaseDrop, set: needCopy("of", FactDuplicateOf),
			Facts: []string{FactDuplicateOf}},
		// The reproducer routes a report that a ticket or an epic in flight
		// will answer. Dependency-advance reads the build each tick and
		// moves the report when the build ends.
		{Verb: VerbAwaitBuild, From: open, To: AwaitingBuild, Command: "work duplicate", Lease: LeaseDrop, set: needCopy("of", FactBuiltBy),
			Facts: []string{FactBuiltBy}},
		{Verb: VerbBuildLanded, From: []string{AwaitingBuild}, To: Duplicate, set: buildLanded, Facts: []string{FactDuplicateOf}},
		{Verb: VerbBuildCanceled, From: []string{AwaitingBuild}, To: Reported, set: buildCanceled, Facts: []string{FactBuiltBy}},
		{Verb: VerbEscalate, From: open, To: Escalated, Lease: LeaseDrop, set: escalateTo(bugResume), Facts: []string{FactResume, FactEscalation}},
		{Verb: VerbAnswer, From: []string{Escalated}, To: ToResume, ResumeTo: []string{Reported}, set: answerResume,
			Facts: []string{FactResume, FactEscalation, FactAnswer, FactAmendment}},
		// The reproducer asks the reporter for a fact only the reporter
		// holds. The inbox relays the question, and the answer returns the
		// report to the reproducer.
		{Verb: "ask", From: []string{Reproducing}, To: Asked, Command: "work ask", Lease: LeaseDrop, set: askPut,
			Facts: append([]string{FactAsks, FactAskedBy}, askFacts(FactQuestion)...)},
		{Verb: VerbAnswer, From: []string{Asked}, To: Reported, set: answerAsk,
			Facts: append([]string{FactAskedBy, FactAnswer}, askFacts(FactAnswer)...)},
		// The reproducer cancels a report whose symptom is in the loop, after
		// it reports the loop fault. Released to reported, the report
		// would only run again.
		{Verb: "cancel", From: []string{Reproducing, Escalated}, To: Canceled, Command: "work cancel", Lease: LeaseDrop,
			set: needCopy("reason", FactCancelReason), Facts: []string{FactCancelReason}},
		{Verb: VerbNote, From: []string{Reported, Reproducing, Asked, AwaitingBuild, Escalated}, To: ToSame, Command: "work note", set: notePut,
			Facts: []string{FactNote}},
		editRow([]string{Reported, Reproducing, Asked, AwaitingBuild, Escalated}),
	}
	pairs := [][2]string{{Reproducing, Reported}}
	rows = append(rows, releaseRows("release", false, pairs, nil)...)
	rows = append(rows, releaseRows("reclaim", true, pairs, nil)...)
	return &Table{Kind: KindBug, BeadType: "bug", States: states, Rows: rows}
}
