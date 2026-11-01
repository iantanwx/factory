package machine

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

// Ticket states (the ticket diagram of the plan).
const (
	Backlog      = "backlog"
	Todo         = "todo"
	InProgress   = "in_progress"
	InReview     = "in_review"
	InQA         = "in_qa"
	Repair       = "repair"
	ReadyToMerge = "ready_to_merge"
	Queued       = "queued"
	Done         = "done"
	Escalated    = "escalated"
	Canceled     = "canceled"
)

// prOpenStates hold an open pull request, so an escalation from one
// resumes as rework.
var prOpenStates = []string{InReview, Repair, InQA, ReadyToMerge, Queued}

// ticketResume is where answer returns a ticket. A ticket escalated from
// backlog returns to backlog, where it waits on its blockers again, and
// no run starts. Every other ticket returns to todo, and the entry says
// why the next run starts: rework when a PR existed, resume otherwise.
func ticketResume(status string) (string, string) {
	switch {
	case status == Backlog:
		return Backlog, ""
	case slices.Contains(prOpenStates, status):
		return Todo, EntryRework
	}
	return Todo, EntryResume
}

func reviewReject(c *change) *cli.Refusal {
	n := count(c, FactRejects) + 1
	c.setFact(FactRejects, fmt.Sprint(n))
	if n >= 2 {
		c.setFact(FactModelTier, ModelTierEscalated)
	}
	c.setFact(FactEntry, EntryRework)
	c.unsetFact(FactReviewedHead)
	return nil
}

// reviewedHeadGuard lets the merge interface take only the head a review
// passed (AE1), and a QA pass name only that head.
func reviewedHeadGuard(c *change) *cli.Refusal {
	if r := c.needParam("head"); r != nil {
		return r
	}
	if c.param("head") != c.fact(FactReviewedHead) {
		return &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "head_not_reviewed", Item: c.item.ID,
			CurrentStatus: c.item.Status, AllowedNext: c.table.AllowedNext(c.item.Status),
			Hint: fmt.Sprintf("head %s has no review pass; the pass is at %q", c.param("head"), c.fact(FactReviewedHead))}
	}
	return nil
}

// unheldGuard refuses a server move while a run holds the lease: that run
// settles the ticket itself.
func unheldGuard(c *change) *cli.Refusal {
	if c.item.Owner == "" {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeLeaseHeld, Rule: "lease_held", Item: c.item.ID, CurrentStatus: c.item.Status,
		Hint: fmt.Sprintf("%s belongs to run %s until that run settles", c.item.ID, c.item.Owner)}
}

// qaDueGuard refuses to send a ticket back to QA when QA already passed
// the head its review passed.
func qaDueGuard(c *change) *cli.Refusal {
	if c.fact(FactQAHead) != c.fact(FactReviewedHead) {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "qa_passed", Item: c.item.ID,
		CurrentStatus: c.item.Status, AllowedNext: c.table.AllowedNext(c.item.Status),
		Hint: fmt.Sprintf("QA already passed %s at %s", c.item.ID, c.fact(FactQAHead))}
}

// baseMergedGuard refuses a merge of the base into any head but the one
// the review passed at.
func baseMergedGuard(c *change) *cli.Refusal {
	if r := c.needParam("from"); r != nil {
		return r
	}
	if c.param("from") != c.fact(FactReviewedHead) {
		return &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "head_not_reviewed", Item: c.item.ID,
			CurrentStatus: c.item.Status, AllowedNext: c.table.AllowedNext(c.item.Status),
			Hint: fmt.Sprintf("the base merged into %s, but the pass is at %q", c.param("from"), c.fact(FactReviewedHead))}
	}
	return nil
}

// endBlock clears pending_block when it names the blocker that work
// unblock --by removed, because that call ends the block on it.
func endBlock(c *change) *cli.Refusal {
	if by := c.param("by"); by != "" && c.fact(FactPendingBlock) == by {
		c.unsetFact(FactPendingBlock)
	}
	return nil
}

// Evidence kinds of work supersede. Each names what already answers a
// ticket: a commit on the base branch, a done item, or an ADR on the base.
const (
	EvidenceCommit = "commit"
	EvidenceDone   = "done"
	EvidenceADR    = "adr"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// supersedeGuard refuses the close while another run holds the ticket's
// lease: that run works the ticket now, and a close from outside would
// race it. The run that holds the lease may close its own ticket.
func supersedeGuard(c *change) *cli.Refusal {
	if c.item.Owner == "" || c.item.Owner == c.req.Actor.RunID {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeLeaseHeld, Rule: "lease_held", Item: c.item.ID, CurrentStatus: c.item.Status,
		Hint: fmt.Sprintf("%s belongs to run %s until that run settles; only that run closes it", c.item.ID, c.item.Owner)}
}

// supersede checks the shape of the evidence, and that a done item is
// done, then records the evidence and the reason as facts, so the closed
// ticket shows why it closed. The machine holds no git, so the reads of
// GitHub and the base branch run after it, in Request.Before.
func supersede(c *change) *cli.Refusal {
	for _, p := range []string{"evidence", "ref", "reason"} {
		if r := c.needParam(p); r != nil {
			return r
		}
	}
	kind, ref := c.param("evidence"), c.param("ref")
	bad := func(hint string) *cli.Refusal {
		return &cli.Refusal{Code: cli.CodeUsage, Rule: "bad_evidence", Item: c.item.ID, Hint: hint}
	}
	switch kind {
	case EvidenceCommit:
		if !shaPattern.MatchString(ref) {
			return bad("--commit names a commit by 7 to 40 lowercase hexadecimal characters")
		}
	case EvidenceDone:
		if ref == c.item.ID {
			return bad(c.item.ID + " cannot answer itself")
		}
		it, r := c.m.Read(c.ctx, c.item.Project, ref)
		if r != nil {
			return r
		}
		if it.Status != Done {
			return &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "evidence_not_done", Item: c.item.ID,
				Hint: fmt.Sprintf("%s is in %s; only a done item answers another", ref, it.Status)}
		}
	case EvidenceADR:
		if !fs.ValidPath(ref) {
			return bad("--adr names a path inside the repository, such as docs/adr/0007-name.md")
		}
	default:
		return bad("name the evidence with exactly one of --commit, --done or --adr")
	}
	c.setFact(FactSupersededBy, kind+" "+ref)
	c.setFact(FactSupersedeReason, c.param("reason"))
	return nil
}

func ticketTable() *Table {
	states := []State{
		{Name: Backlog, Category: beadsx.CategoryFrozen},
		{Name: Todo, Category: beadsx.CategoryActive},
		{Name: InProgress, Category: beadsx.CategoryWIP},
		{Name: InReview, Category: beadsx.CategoryActive},
		{Name: InQA, Category: beadsx.CategoryActive},
		{Name: Repair, Category: beadsx.CategoryActive},
		{Name: ReadyToMerge, Category: beadsx.CategoryActive},
		{Name: Queued, Category: beadsx.CategoryWIP},
		{Name: Escalated, Category: beadsx.CategoryFrozen},
		{Name: Done, Category: beadsx.CategoryDone, Terminal: true},
		{Name: Canceled, Category: beadsx.CategoryDone, Terminal: true},
	}
	live := []string{Backlog, Todo, InProgress, InReview, Repair, InQA, ReadyToMerge, Queued, Escalated}
	rows := []Row{
		{Verb: VerbCreate, From: []string{Initial}, To: Todo, When: "no_open_dependency",
			when: paramIs("blocked", ""), set: setEntry(EntryNew), Facts: []string{FactEntry}},
		{Verb: VerbCreate, From: []string{Initial}, To: Backlog, When: "open_dependency",
			when: paramIs("blocked", "true")},
		// work block writes pending_block in its move, before its edge, so a
		// crash between the two leaves the ticket held (spec factorio-9wh
		// Decision 4).
		{Verb: "block", From: []string{Todo, Backlog}, To: Backlog, Command: "work block",
			set: needCopy("by", FactPendingBlock), Facts: []string{FactPendingBlock}},
		{Verb: VerbUnblock, From: []string{Backlog}, To: Todo, When: "no_open_blocker", Command: "work unblock",
			when: paramIs("open", ""), set: all(setEntry(EntryNew), clearFacts(FactPendingBlock)),
			Facts: []string{FactEntry, FactPendingBlock}},
		{Verb: VerbUnblock, From: []string{Backlog}, To: ToSame, When: "open_blocker", Command: "work unblock",
			when: paramIs("open", "true"), set: endBlock, Facts: []string{FactPendingBlock}},

		{Verb: "claim", From: []string{Todo}, To: InProgress, Stage: StageImplementer, when: stageIs(StageImplementer),
			guard: claimGuard, BumpsEpoch: true, Lease: LeaseTake, Facts: []string{FactOwner, FactStage}},
		{Verb: "claim", From: []string{Repair}, To: ToSame, Stage: StageImplementer, when: stageIs(StageImplementer),
			guard: claimGuard, BumpsEpoch: true, Lease: LeaseTake, Facts: []string{FactOwner, FactStage}},
		{Verb: "claim", From: []string{InReview}, To: ToSame, Stage: StageCodeReviewer, when: stageIs(StageCodeReviewer),
			guard: claimGuard, BumpsEpoch: true, Lease: LeaseTake, Facts: []string{FactOwner, FactStage}},
		{Verb: "claim", From: []string{InQA}, To: ToSame, Stage: StageQA, when: stageIs(StageQA),
			guard: claimGuard, BumpsEpoch: true, Lease: LeaseTake, Facts: []string{FactOwner, FactStage}},

		{Verb: "pr_open", From: []string{InProgress}, To: InReview, Lease: LeaseDrop,
			set: copyParam("head", FactHead), Facts: []string{FactHead}},
		{Verb: VerbCIRed, From: []string{InReview}, To: Repair},
		{Verb: "fix_pushed", From: []string{Repair}, To: InReview, Lease: LeaseDrop,
			set: copyParam("head", FactHead), Facts: []string{FactHead}},
		// review submit passes qa=on when the project runs the QA stage
		// (ADR 0014 decision 1). A pass with no qa parameter skips QA.
		{Verb: "review_pass", From: []string{InReview}, To: InQA, When: "qa_on", Command: "review submit", Lease: LeaseDrop,
			when: paramIs("qa", "on"), set: needCopy("head", FactReviewedHead), Facts: []string{FactReviewedHead}},
		{Verb: "review_pass", From: []string{InReview}, To: ReadyToMerge, When: "qa_off", Command: "review submit", Lease: LeaseDrop,
			set: needCopy("head", FactReviewedHead), Facts: []string{FactReviewedHead}},
		{Verb: "review_reject", From: []string{InReview}, To: Todo, Command: "review submit", Lease: LeaseDrop,
			set: reviewReject, Facts: []string{FactRejects, FactModelTier, FactEntry, FactReviewedHead}},
		{Verb: VerbQAPass, From: []string{InQA}, To: ReadyToMerge, Command: "qa report", Lease: LeaseDrop, guard: reviewedHeadGuard,
			set: all(needCopy("head", FactQAHead), clearFacts(FactQACarriedFrom)), Facts: []string{FactQAHead, FactQACarriedFrom}},
		// A QA fail is a reject (ADR 0014 decision 2): the next head goes
		// through the simplify run, review and QA again.
		{Verb: VerbQAFail, From: []string{InQA}, To: Todo, Command: "qa report", Lease: LeaseDrop,
			set:   all(reviewReject, setTo(FactSimplifyDue, "true"), clearFacts(FactQAHead, FactSimplifyFrom)),
			Facts: []string{FactRejects, FactModelTier, FactEntry, FactReviewedHead, FactSimplifyDue, FactQAHead, FactSimplifyFrom}},
		{Verb: VerbQAOff, From: []string{InQA}, To: ReadyToMerge, guard: unheldGuard},
		{Verb: VerbQADue, From: []string{ReadyToMerge, Queued}, To: InQA, guard: qaDueGuard},
		{Verb: "head_changed", From: []string{ReadyToMerge, Queued}, To: InReview,
			set: all(clearFacts(FactReviewedHead), copyParam("head", FactHead)), Facts: []string{FactReviewedHead, FactHead}},
		{Verb: VerbEnqueue, From: []string{ReadyToMerge}, To: Queued, guard: reviewedHeadGuard},
		// The server merged a moved base into the reviewed head with no
		// conflict and no shared file (F-66). The merge commit takes the
		// review pass, so no new review round starts.
		{Verb: VerbBaseMerged, From: []string{Queued}, To: ToSame, guard: baseMergedGuard,
			set: all(needCopy("head", FactReviewedHead), copyParam("head", FactHead)), Facts: []string{FactReviewedHead, FactHead}},
		{Verb: "dequeue_conflict", From: []string{Queued}, To: Todo,
			set: all(setEntry(EntryRework), clearFacts(FactReviewedHead)), Facts: []string{FactEntry, FactReviewedHead}},
		{Verb: "dequeue_red", From: []string{Queued}, To: Repair,
			set: clearFacts(FactReviewedHead), Facts: []string{FactReviewedHead}},
		{Verb: "merged", From: live, To: Done, BumpsEpoch: true, Lease: LeaseDrop,
			set: copyParam("sha", FactMergedSHA), Facts: []string{FactMergedSHA}},
		{Verb: VerbPRClosed, From: prOpenStates, To: Escalated, Lease: LeaseDrop,
			set: escalateTo(ticketResume), Facts: []string{FactResume, FactResumeEntry, FactEscalation}},
		{Verb: VerbEscalate, From: except(live, Escalated), To: Escalated, Lease: LeaseDrop,
			set: escalateTo(ticketResume), Facts: []string{FactResume, FactResumeEntry, FactEscalation}},
		{Verb: VerbAnswer, From: []string{Escalated}, To: ToResume, ResumeTo: []string{Backlog, Todo},
			set: answerResume, Facts: []string{FactEntry, FactResume, FactResumeEntry, FactEscalation, FactAnswer, FactAmendment}},
		{Verb: "cancel", From: []string{Escalated}, To: Canceled, Command: "work cancel", Lease: LeaseDrop,
			set: needCopy("reason", FactCancelReason), Facts: []string{FactCancelReason}},
		// A ticket that the base, a done item or an ADR already answers has
		// no work left. A state that holds a pull request never closes so.
		{Verb: VerbSupersede, From: []string{Backlog, Todo, InProgress}, To: Canceled, Command: "work supersede",
			Lease: LeaseDrop, guard: supersedeGuard, set: supersede, Facts: []string{FactSupersededBy, FactSupersedeReason}},
		{Verb: VerbNote, From: live, To: ToSame, Command: "work note", set: notePut, Facts: []string{FactNote}},
		editRow(live),
	}
	rows = withReviews(rows)
	resume := map[string]setter{InProgress: setEntry(EntryResume)}
	pairs := [][2]string{{InProgress, Todo}, {Repair, Repair}, {InReview, InReview}, {InQA, InQA}}
	rows = append(rows, releaseRows("release", false, pairs, resume)...)
	rows = append(rows, releaseRows("reclaim", true, pairs, resume)...)
	return &Table{Kind: KindTicket, BeadType: "task", States: states, Rows: rows}
}
