package machine

import "github.com/iantanwx/loop/internal/beadsx"

// Bug states. A raw report becomes a shaped ticket, a duplicate or an
// escalation, behind the optional reproducer stage: a project without
// the stage shapes a report straight from reported.
const (
	Reported    = "reported"
	Reproducing = "reproducing"
	Shaped      = "shaped"
	Duplicate   = "duplicate"
)

func bugResume(string) (string, string) { return Reported, "" }

func bugTable() *Table {
	states := []State{
		{Name: Reported, Category: beadsx.CategoryActive},
		{Name: Reproducing, Category: beadsx.CategoryWIP},
		{Name: Escalated, Category: beadsx.CategoryFrozen},
		{Name: Shaped, Category: beadsx.CategoryDone, Terminal: true},
		{Name: Duplicate, Category: beadsx.CategoryDone, Terminal: true},
		{Name: Canceled, Category: beadsx.CategoryDone, Terminal: true},
	}
	open := []string{Reported, Reproducing}
	rows := []Row{
		{Verb: "file", From: []string{Initial}, To: Reported},
		{Verb: "claim", From: []string{Reported}, To: Reproducing, Stage: StageReproducer, when: stageIs(StageReproducer),
			guard: claimGuard, BumpsEpoch: true, Lease: LeaseTake, Facts: []string{FactOwner, FactStage}},
		{Verb: "shape", From: open, To: Shaped, Lease: LeaseDrop, set: needCopy("ticket", FactTicket), Facts: []string{FactTicket}},
		{Verb: "duplicate", From: open, To: Duplicate, Lease: LeaseDrop, set: needCopy("of", FactDuplicateOf),
			Facts: []string{FactDuplicateOf}},
		{Verb: "escalate", From: open, To: Escalated, Lease: LeaseDrop, set: escalateTo(bugResume), Facts: []string{FactResume, FactEscalation}},
		{Verb: "answer", From: []string{Escalated}, To: ToResume, ResumeTo: []string{Reported}, set: answerResume,
			Facts: []string{FactResume, FactEscalation, FactAnswer, FactAmendment}},
		{Verb: "cancel", From: []string{Escalated}, To: Canceled, Command: "work cancel"},
		{Verb: "note", From: []string{Reported, Reproducing, Escalated}, To: ToSame, Command: "work note", set: notePut,
			Facts: []string{FactNote}},
	}
	pairs := [][2]string{{Reproducing, Reported}}
	rows = append(rows, releaseRows("release", false, pairs, nil)...)
	rows = append(rows, releaseRows("reclaim", true, pairs, nil)...)
	return &Table{Kind: KindBug, BeadType: "bug", States: states, Rows: rows}
}
