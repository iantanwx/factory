package machine

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

// Epic and document states (the epic diagram of the plan).
const (
	PRDReview    = "prd_review"
	PRDAmending  = "prd_amending"
	PRDSignoff   = "prd_signoff"
	Architecture = "architecture"
	SpecNeeded   = "spec_needed"
	SpecDrafting = "spec_drafting"
	SpecReview   = "spec_review"
	SpecSignoff  = "spec_signoff"
	SpecApproved = "spec_approved"
	Decomposing  = "decomposing"
	Execution    = "execution"
)

// epicResume is where answer returns an epic. A state a stage polls
// returns to itself. A state a run holds returns to the queue it was
// claimed from. A review state returns to its author, except PRD review:
// after the second reject an intake room revises the PRD, and the answer
// sends it back to review (AE7). Ported from escalationTargetFor in
// scripts/sdlc/handoff.ts (alpha).
var epicResume = map[string]string{
	PRDReview:    PRDReview,
	PRDAmending:  PRDAmending,
	PRDSignoff:   PRDSignoff,
	Architecture: Architecture,
	SpecNeeded:   SpecNeeded,
	SpecDrafting: SpecNeeded,
	SpecReview:   SpecNeeded,
	SpecSignoff:  SpecSignoff,
	SpecApproved: SpecApproved,
	Decomposing:  SpecApproved,
	Execution:    Execution,
}

func epicResumeFor(status string) (string, string) { return epicResume[status], "" }

// prdReject is the first reject (the amender revises once) or the second
// (a person decides, and no unattended stage revises again: AE7).
func firstPRDReject(c *change) bool  { return count(c, FactPRDRejects) == 0 }
func secondPRDReject(c *change) bool { return count(c, FactPRDRejects) > 0 }

func bumpPRDRejects(c *change) *cli.Refusal {
	c.setFact(FactPRDRejects, fmt.Sprint(count(c, FactPRDRejects)+1))
	return nil
}

// secondRejectSays is what the history, and so the inbox, records for the
// second PRD reject: a person revises the PRD in an intake room (AE7).
const secondRejectSays = "The doc-reviewer rejected the PRD a second time, so no unattended stage revises it again. " +
	"Open an intake room on this epic, and revise the PRD there with the operator. The answer returns it to review."

// mark stores a parameter as a fact, or "true" when the caller sent none:
// the fact records that the event happened.
func mark(fact, param string) setter {
	return func(c *change) *cli.Refusal {
		v := c.param(param)
		if v == "" {
			v = "true"
		}
		c.setFact(fact, v)
		return nil
	}
}

// specClaim refuses a spec author on an epic whose PRD never merged and
// whose ADR never landed (R46): the spec stage starts only after the
// operator's sign-off.
func specClaim(r Row) Row {
	next := r.guard
	r.guard = func(c *change) *cli.Refusal {
		if c.fact(FactPRDMerged) == "" && c.fact(FactADR) == "" {
			return &cli.Refusal{Code: cli.CodePRDRequired, Rule: "no_signed_off_document", Item: c.item.ID, CurrentStatus: c.item.Status,
				Hint: fmt.Sprintf("%s has no merged PRD and no landed ADR; the spec starts after the operator merges one", c.item.ID)}
		}
		return next(c)
	}
	return r
}

// childrenTerminal refuses to close an epic while a child is open. A
// child names its epic in the fact "epic".
func childrenTerminal(c *change) *cli.Refusal {
	bc, err := c.m.cfg.Beads(c.item.Project)
	if err != nil {
		return internalRefusal(err)
	}
	kids, err := bc.List(c.ctx, beadsx.Filter{Metadata: map[string]string{factPrefix + FactEpic: c.item.ID}})
	if err != nil {
		return beadsRefusal(c.item.ID, err)
	}
	var open []string
	for _, k := range kids {
		t, ok := kindOfType(k.Type)
		if !ok || !t.Terminal(k.Status) {
			open = append(open, k.ID+" ("+k.Status+")")
		}
	}
	if len(open) > 0 {
		return &cli.Refusal{Code: cli.CodeDependencyOpen, Rule: "children_open", Item: c.item.ID, CurrentStatus: c.item.Status,
			Hint: fmt.Sprintf("%s closes only when every child is terminal; open: %s", c.item.ID, strings.Join(open, ", "))}
	}
	return nil
}

func epicTable() *Table {
	states := []State{
		{Name: PRDReview, Category: beadsx.CategoryActive},
		{Name: PRDAmending, Category: beadsx.CategoryActive},
		{Name: PRDSignoff, Category: beadsx.CategoryActive},
		{Name: Architecture, Category: beadsx.CategoryActive},
		{Name: SpecNeeded, Category: beadsx.CategoryActive},
		{Name: SpecDrafting, Category: beadsx.CategoryWIP},
		{Name: SpecReview, Category: beadsx.CategoryActive},
		{Name: SpecSignoff, Category: beadsx.CategoryActive},
		{Name: SpecApproved, Category: beadsx.CategoryActive},
		{Name: Decomposing, Category: beadsx.CategoryWIP},
		{Name: Execution, Category: beadsx.CategoryWIP},
		{Name: Escalated, Category: beadsx.CategoryFrozen},
		{Name: Done, Category: beadsx.CategoryDone, Terminal: true},
	}
	live := []string{PRDReview, PRDAmending, PRDSignoff, Architecture, SpecNeeded, SpecDrafting, SpecReview,
		SpecSignoff, SpecApproved, Decomposing, Execution, Escalated}
	claim := func(from, to, stage string) Row {
		return Row{Verb: "claim", From: []string{from}, To: to, Stage: stage, when: stageIs(stage),
			guard: claimGuard, BumpsEpoch: true, Lease: LeaseTake, Facts: []string{FactOwner, FactStage}}
	}
	rows := []Row{
		{Verb: "prd_submit", From: []string{Initial}, To: PRDReview, Command: "prd submit",
			set: copyParam("pr", FactDocPR), Facts: []string{FactDocPR}},
		{Verb: "create_architecture", From: []string{Initial}, To: Architecture},

		claim(PRDReview, ToSame, StageDocReviewer),
		claim(PRDAmending, ToSame, StageAmender),
		specClaim(claim(SpecNeeded, SpecDrafting, StageSpecAuthor)),
		claim(SpecReview, ToSame, StageDocReviewer),
		claim(SpecApproved, Decomposing, StageDecomposer),

		{Verb: "review_pass", From: []string{PRDReview}, To: PRDSignoff, Command: "review submit", Lease: LeaseDrop},
		{Verb: "review_reject", From: []string{PRDReview}, To: PRDAmending, Command: "review submit", Lease: LeaseDrop,
			When: "first_reject", when: firstPRDReject, set: bumpPRDRejects, Facts: []string{FactPRDRejects}},
		{Verb: "review_reject", From: []string{PRDReview}, To: Escalated, Command: "review submit",
			When: "second_reject", when: secondPRDReject, Lease: LeaseDrop, Says: secondRejectSays,
			set: all(bumpPRDRejects, escalateTo(epicResumeFor)), Facts: []string{FactPRDRejects, FactResume, FactEscalation}},
		{Verb: "prd_amended", From: []string{PRDAmending}, To: PRDReview, Lease: LeaseDrop},
		{Verb: "doc_merged", From: []string{PRDSignoff}, To: SpecNeeded, set: mark(FactPRDMerged, "sha"), Facts: []string{FactPRDMerged}},
		{Verb: "adr_landed", From: []string{Architecture}, To: SpecNeeded, set: mark(FactADR, "sha"), Facts: []string{FactADR}},
		{Verb: "pr_open", From: []string{SpecDrafting}, To: SpecReview, Lease: LeaseDrop,
			set: copyParam("pr", FactDocPR), Facts: []string{FactDocPR}},
		{Verb: "review_pass", From: []string{SpecReview}, To: SpecSignoff, Command: "review submit", Lease: LeaseDrop},
		{Verb: "review_reject", From: []string{SpecReview}, To: SpecNeeded, Command: "review submit", Lease: LeaseDrop},
		{Verb: "doc_merged", From: []string{SpecSignoff}, To: SpecApproved},
		{Verb: "decomposed", From: []string{Decomposing}, To: Execution, Lease: LeaseDrop},
		{Verb: "complete", From: []string{Execution}, To: Done, guard: childrenTerminal},
		{Verb: "answer", From: []string{Escalated}, To: ToResume, ResumeTo: uniqueValues(epicResume),
			set: answerResume, Facts: []string{FactResume, FactEscalation, FactAnswer, FactAmendment}},

		// A document merged while still in review counts as sign-off and
		// revokes the review lease, as the outran rows of
		// scripts/sdlc/document-states.ts:141-145 (alpha) did. It lands on
		// sign-off and never past it.
		{Verb: "doc_merged", From: []string{PRDReview}, To: PRDSignoff, When: "outran", BumpsEpoch: true, Lease: LeaseDrop},
		{Verb: "doc_merged", From: []string{SpecReview}, To: SpecSignoff, When: "outran", BumpsEpoch: true, Lease: LeaseDrop},

		// A closed document PR escalates the epic and never cancels it.
		{Verb: "pr_closed", From: []string{PRDReview, PRDAmending, PRDSignoff, SpecReview, SpecSignoff}, To: Escalated,
			Lease: LeaseDrop, set: escalateTo(epicResumeFor), Facts: []string{FactResume, FactEscalation}},
		{Verb: "escalate", From: except(live, Escalated), To: Escalated, Lease: LeaseDrop,
			set: escalateTo(epicResumeFor), Facts: []string{FactResume, FactEscalation}},
		{Verb: "note", From: live, To: ToSame, Command: "work note", set: notePut, Facts: []string{FactNote}},
	}
	pairs := [][2]string{{SpecDrafting, SpecNeeded}, {Decomposing, SpecApproved},
		{PRDReview, PRDReview}, {PRDAmending, PRDAmending}, {SpecReview, SpecReview}}
	rows = append(rows, releaseRows("release", false, pairs, nil)...)
	rows = append(rows, releaseRows("reclaim", true, pairs, nil)...)
	return &Table{Kind: KindEpic, BeadType: "epic", States: states, Rows: rows}
}

// uniqueValues lists the targets of m once each, in state order.
func uniqueValues(m map[string]string) []string {
	var out []string
	for _, s := range epicStateOrder() {
		if slices.Contains(slices.Collect(maps.Values(m)), s) {
			out = append(out, s)
		}
	}
	return out
}

func epicStateOrder() []string {
	return []string{PRDReview, PRDAmending, PRDSignoff, Architecture, SpecNeeded, SpecDrafting, SpecReview,
		SpecSignoff, SpecApproved, Decomposing, Execution}
}
