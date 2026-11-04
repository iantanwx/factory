package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

// Epic and document states (the epic diagram of the plan).
const (
	PRDNeeded    = "prd_needed"
	PRDDrafting  = "prd_drafting"
	PRDReview    = "prd_review"
	PRDAmending  = "prd_amending"
	SpecAmending = "spec_amending"
	PRDSignoff   = "prd_signoff"
	Architecture = "architecture"
	ADRReview    = "adr_review"
	SpecNeeded   = "spec_needed"
	SpecDrafting = "spec_drafting"
	SpecReview   = "spec_review"
	SpecSignoff  = "spec_signoff"
	SpecApproved = "spec_approved"
	Decomposing  = "decomposing"
	Execution    = "execution"
)

// The design stage of an epic whose PRD holds a UI story: the design room
// writes the record, the doc-reviewer reviews it, and the operator signs
// it off.
const (
	Design        = "design"
	DesignReview  = "design_review"
	DesignSignoff = "design_signoff"
)

// epicResume is where answer returns an epic. A state a stage polls
// returns to itself. A state a run holds returns to the queue it was
// claimed from. A review state returns to its author, except PRD review:
// after the second reject a brainstorm room revises the PRD, and the answer
// sends it back to review (AE7). A design in review returns to the design
// room, which revises the record. Ported from escalationTargetFor in
// scripts/sdlc/handoff.ts (alpha).
var epicResume = map[string]string{
	Backlog:       Backlog,
	PRDNeeded:     PRDNeeded,
	PRDDrafting:   PRDNeeded,
	PRDReview:     PRDReview,
	PRDAmending:   PRDAmending,
	PRDSignoff:    PRDSignoff,
	Architecture:  Architecture,
	ADRReview:     Architecture,
	Design:        Design,
	DesignReview:  Design,
	DesignSignoff: DesignSignoff,
	SpecNeeded:    SpecNeeded,
	SpecDrafting:  SpecNeeded,
	SpecReview:    SpecNeeded,
	SpecSignoff:   SpecSignoff,
	SpecAmending:  SpecAmending,
	SpecApproved:  SpecApproved,
	Decomposing:   SpecApproved,
	Execution:     Execution,
}

func epicResumeFor(status string) (string, string) { return epicResume[status], "" }

// setEpicEntry records where unblock sends a blocked epic.
func setEpicEntry(state string) setter {
	return func(c *change) *cli.Refusal { c.setFact(FactEpicEntry, state); return nil }
}

// amendingFor is where a person's unanswered comment sends a document in
// review or in sign-off: the reviser answers it, and no stage writes the
// document meanwhile. A reviewer writes nothing, and a sign-off waits on
// a person, so the reviser is the one writer in each. Drafting states are
// left out: their author is the writer, and answers the comment in the
// draft. Ported from AMENDABLE_STATES in scripts/sdlc/amend-states.ts
// (alpha).
var amendingFor = map[string]string{
	PRDReview:   PRDAmending,
	PRDSignoff:  PRDAmending,
	SpecReview:  SpecAmending,
	SpecSignoff: SpecAmending,
}

// AmendableStates lists the states amendingFor reads, in state order. The
// GitHub reactor reads a person's comments on the document PR of each
// epic in one of them.
func AmendableStates() []string {
	var out []string
	for _, s := range epicStateOrder() {
		if _, ok := amendingFor[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

// unheld refuses to move a document a run holds, as claimGuard refuses
// a claim: the run's next write must find the state it claimed. The
// comment waits, and the first tick after the run ends reads it again.
func unheld(c *change) *cli.Refusal {
	if c.item.Owner != "" {
		return &cli.Refusal{Code: cli.CodeLeaseHeld, Rule: "lease_held", Item: c.item.ID, CurrentStatus: c.item.Status,
			Hint: fmt.Sprintf("%s belongs to run %s until that run settles", c.item.ID, c.item.Owner)}
	}
	return nil
}

// personComment records where the comment found the document and the
// head it found it at, so the amended row can send it back.
func personComment(c *change) *cli.Refusal {
	if r := c.needParam("head"); r != nil {
		return r
	}
	c.setFact(FactAmendFrom, c.item.Status)
	c.setFact(FactAmendHead, c.param("head"))
	return nil
}

// amendedUnchanged holds when the reviser answered a comment on a signed
// off document and pushed nothing: the document returns to sign-off, and
// costs no review round. A revision, or a reply to a reject, returns to
// review. Workbench in scripts/sdlc/amend-states.ts (alpha) says why the
// queue, not the agent, names the state.
func amendedUnchanged(signoff string) func(c *change) bool {
	return func(c *change) bool {
		return c.fact(FactAmendFrom) == signoff && c.param("head") != "" && c.param("head") == c.fact(FactAmendHead)
	}
}

var clearAmend = clearFacts(FactAmendFrom, FactAmendHead)

// prdReject is the first reject (the reviser revises once) or the second,
// the cap (AE7). The first cap waits for the triage panel, which may
// return the PRD to the reviser once. After that return, the cap reaches a
// person directly. Ported from fixer-author-gap.ts (alpha, TIC-263).
func firstPRDReject(c *change) bool { return count(c, FactPRDRejects) == 0 }
func prdCap(c *change) bool         { return count(c, FactPRDRejects) > 0 && c.fact(FactCapReturned) == "" }
func returnedPRDReject(c *change) bool {
	return count(c, FactPRDRejects) > 0 && c.fact(FactCapReturned) != ""
}

// WhenPRDCap selects the row of the PRD reject cap that waits for the
// triage panel. The server's Applied hook opens its escalation.
const WhenPRDCap = "second_reject"

func bumpPRDRejects(c *change) *cli.Refusal {
	c.setFact(FactPRDRejects, fmt.Sprint(count(c, FactPRDRejects)+1))
	return nil
}

// secondRejectSays is what the history, and so the inbox, records for the
// second PRD reject: a person revises the PRD in a brainstorm room (AE7).
const secondRejectSays = "The doc-reviewer rejected the PRD a second time, so no unattended stage revises it again. " + brainstormRoomSays

const brainstormRoomSays = "Open a brainstorm room on this epic, and revise the PRD there with the operator. The answer returns it to review."

// returnedRejectSays is secondRejectSays for a PRD that the triage panel
// already returned to the reviser once: that cap reaches a person with no
// panel.
const returnedRejectSays = "The doc-reviewer rejected the PRD a second time, after the loop already returned it to the reviser once. " +
	"So no unattended stage revises it again. " + brainstormRoomSays

// capReturn is the triage panel's loop route of the PRD reject cap: the
// reviser revises the PRD from the reject's findings, once for each epic.
// The reject count starts again, so the next reject is the first of a new
// count, and the cap after it reaches a person.
func capReturn(c *change) *cli.Refusal {
	if r := c.needParam("escalation"); r != nil {
		return r
	}
	if c.fact(FactCapReturned) != "" || c.fact(FactResume) != PRDReview {
		return &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "cap_not_returnable", Item: c.item.ID, CurrentStatus: c.item.Status,
			AllowedNext: c.table.AllowedNext(c.item.Status),
			Hint:        fmt.Sprintf("%s holds no PRD reject cap that the loop may return; the loop returns a capped PRD once", c.item.ID)}
	}
	c.setFact(FactCapReturned, c.param("escalation"))
	c.unsetFact(FactPRDRejects, FactResume, FactResumeEntry, FactEscalation)
	return nil
}

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

// specClaim refuses a planner on an epic whose PRD never merged and
// whose ADR never landed (R46): the spec stage starts only after the
// operator's sign-off. It refuses an epic with a UI story that has no
// design outcome too: answer and adr_misjoined can also land an epic on
// spec_needed, and its spec starts only after its design (A8).
func specClaim(r Row) Row {
	next := r.guard
	r.guard = func(c *change) *cli.Refusal {
		if c.fact(FactPRDMerged) == "" && c.fact(FactADR) == "" {
			return &cli.Refusal{Code: cli.CodePRDRequired, Rule: "no_signed_off_document", Item: c.item.ID, CurrentStatus: c.item.Status,
				Hint: fmt.Sprintf("%s has no merged PRD and no landed ADR; the spec starts after the operator merges one", c.item.ID)}
		}
		if designDue(c) {
			return &cli.Refusal{Code: cli.CodePRDRequired, Rule: "design_required", Item: c.item.ID, CurrentStatus: c.item.Status,
				Hint: fmt.Sprintf("%s holds the UI stories %s, and neither a merged design record nor the operator's skip; "+
					"the spec starts after one", c.item.ID, c.fact(FactUIStories))}
		}
		return next(c)
	}
	return r
}

// designDue holds when the epic's PRD holds a UI story, and the epic holds
// no design outcome yet: its next document is the design record.
func designDue(c *change) bool {
	return c.fact(FactUIStories) != "" && c.fact(FactDesignMerged) == "" && c.fact(FactDesignSkipped) == ""
}

// Acceptance is one entry of an epic's accepted_differences fact: the
// operator's words that accept how a shipped screen differs from one state
// of the merged design record.
type Acceptance struct {
	State string `json:"state"`           // a state id of the record at design_path, S<n>.<m>
	Words string `json:"words"`           // the operator's words, exactly as given
	At    string `json:"at"`              // the time of the write
	Share string `json:"share,omitempty"` // the accepted pixel share, such as 1.20%, or "" for none
}

// designRecorded refuses an acceptance on an epic whose design record
// never merged, since no state exists to accept a difference from.
func designRecorded(c *change) *cli.Refusal {
	if c.fact(FactDesignMerged) != "" && c.fact(FactDesignPath) != "" {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeShapeRefused, Rule: "no_design_record", Item: c.item.ID, CurrentStatus: c.item.Status,
		Hint: fmt.Sprintf("%s holds no merged design record, so it has no state to accept a difference from", c.item.ID)}
}

// acceptDifference appends one Acceptance to accepted_differences, and
// keeps each earlier one. The server checked the state against the record.
func acceptDifference(c *change) *cli.Refusal {
	for _, p := range []string{"state", "request", "at"} {
		if r := c.needParam(p); r != nil {
			return r
		}
	}
	var list []Acceptance
	if raw := c.fact(FactAcceptedDifferences); raw != "" {
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return cli.InternalRefusal(fmt.Errorf("machine: %s holds no list in %s: %w", c.item.ID, FactAcceptedDifferences, err))
		}
	}
	raw, err := json.Marshal(append(list, Acceptance{State: c.param("state"), Words: c.param("request"), At: c.param("at"), Share: c.param("share")}))
	if err != nil {
		return cli.InternalRefusal(err)
	}
	c.setFact(FactAcceptedDifferences, string(raw))
	return nil
}

// uiRoute holds when the PRD merge carries a UI story.
func uiRoute(c *change) bool { return c.param("ui_stories") != "" }

// prdMerged records the PRD's merge commit and its UI stories, once, at
// the PRD merge.
var prdMerged = all(mark(FactPRDMerged, "sha"), copyParam("ui_stories", FactUIStories))

// adrJoined records the ADR that landed and the commit that added it.
var adrJoined = all(needCopy("path", FactADR), needCopy("added", FactADRMerged))

// adrRequested holds when the PRD review found a requirement that needs
// an ADR, or the operator sent the epic to the ADR stage: the PRD merge
// then lands on architecture, not on spec_needed.
func adrRequested(c *change) bool { return c.fact(FactADRNeeded) != "" || c.fact(FactADRSent) != "" }

// adrNeeded stores the requirements a PRD pass found to pass the ADR
// test, or clears the list of an earlier pass when the pass names none.
func adrNeeded(c *change) *cli.Refusal {
	if v := c.param("adr_needed"); v != "" {
		c.setFact(FactADRNeeded, v)
	} else {
		c.unsetFact(FactADRNeeded)
	}
	return nil
}

// adrOff clears both reasons for the ADR stage at the operator's word.
func adrOff(c *change) *cli.Refusal {
	if r := c.needParam("request"); r != nil {
		return r
	}
	c.unsetFact(FactADRNeeded, FactADRSent)
	return nil
}

// adrIs refuses adr_misjoined unless the epic's adr fact is still the
// path the caller read: an epic that a later write joined to another ADR
// keeps it.
func adrIs(c *change) *cli.Refusal {
	if r := c.needParam("path"); r != nil {
		return r
	}
	if got := c.fact(FactADR); got != c.param("path") {
		return &cli.Refusal{Code: cli.CodeStatusChanged, Rule: "adr_changed", Item: c.item.ID, CurrentStatus: c.item.Status,
			Hint: fmt.Sprintf("%s records adr=%q, not %q", c.item.ID, got, c.param("path"))}
	}
	return nil
}

// headNamed refuses a verdict that names no head.
func headNamed(c *change) *cli.Refusal { return c.needParam("head") }

// childrenTerminal refuses to close an epic while a child is open. A
// child names its epic in the fact "epic".
func childrenTerminal(c *change) *cli.Refusal {
	bc, err := c.m.cfg.Beads(c.item.Project)
	if err != nil {
		return cli.InternalRefusal(err)
	}
	_, open, err := children(c.ctx, bc, c.item.ID)
	if err != nil {
		return beadsRefusal(c.item.ID, err)
	}
	if len(open) > 0 {
		named := make([]string, len(open))
		for i, k := range open {
			named[i] = k.ID + " (" + k.Status + ")"
		}
		return &cli.Refusal{Code: cli.CodeDependencyOpen, Rule: "children_open", Item: c.item.ID, CurrentStatus: c.item.Status,
			Hint: fmt.Sprintf("%s closes only when every child is terminal; open: %s", c.item.ID, strings.Join(named, ", "))}
	}
	return nil
}

// children counts an epic's children and lists each open one.
func children(ctx context.Context, bc beadsx.Client, epic string) (int, []beadsx.Issue, error) {
	kids, err := bc.List(ctx, beadsx.Filter{Metadata: map[string]string{factPrefix + FactEpic: epic}})
	if err != nil {
		return 0, nil, err
	}
	var open []beadsx.Issue
	for _, k := range kids {
		t, ok := kindOfType(k.Type)
		if !ok || !t.Terminal(k.Status) {
			open = append(open, k)
		}
	}
	return len(kids), open, nil
}

// OpenChildren lists the ids of the epic's children whose status is not
// terminal, by the rule that refuses complete. The inbox's architecture
// entry reads it.
func (m *Machine) OpenChildren(ctx context.Context, project, epic string) ([]string, *cli.Refusal) {
	bc, err := m.cfg.Beads(project)
	if err != nil {
		return nil, cli.InternalRefusal(err)
	}
	_, open, err := children(ctx, bc, epic)
	if err != nil {
		return nil, beadsRefusal(epic, err)
	}
	ids := make([]string, len(open))
	for i, k := range open {
		ids[i] = k.ID
	}
	return ids, nil
}

// CompleteEpics applies complete to each epic of the project in execution
// that has children, every one of them terminal. A child's terminal write
// and its epic's complete write are two writes, so a crash or a beads
// fault between them leaves the epic in execution. The start-up reconcile
// and each dependency-advance tick run this sweep, so such an epic closes.
// An epic another writer moved meanwhile is left alone.
func (m *Machine) CompleteEpics(ctx context.Context, project string) error {
	epics, ref := m.List(ctx, project, ListFilter{Kind: KindEpic, Status: Execution})
	if ref != nil {
		return fmt.Errorf("machine: list the epics in execution of %s: %s: %s", project, ref.Code, ref.Hint)
	}
	if len(epics) == 0 {
		return nil
	}
	bc, err := m.cfg.Beads(project)
	if err != nil {
		return err
	}
	var errs []error
	for _, ep := range epics {
		n, open, err := children(ctx, bc, ep.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("machine: read the children of %s: %w", ep.ID, err))
			continue
		}
		if n == 0 || len(open) > 0 {
			continue
		}
		res := m.Apply(ctx, Request{Project: project, Item: ep.ID, Verb: "complete", ExpectedStatus: Execution, Actor: Server("machine")})
		switch r := res.Refusal; {
		case r == nil:
			m.log.InfoContext(ctx, "completed an epic whose children are all terminal", "project", project, "item", ep.ID)
		case r.Code == cli.CodeStatusChanged || r.Code == cli.CodeTransitionNotAllowed || r.Code == cli.CodeDependencyOpen:
			// Another writer moved the epic or opened a child. Nothing waits.
		default:
			errs = append(errs, fmt.Errorf("machine: complete %s: %s: %s", ep.ID, r.Code, r.Hint))
		}
	}
	return errors.Join(errs...)
}

func epicTable() *Table {
	states := []State{
		{Name: Backlog, Category: beadsx.CategoryFrozen},
		{Name: PRDNeeded, Category: beadsx.CategoryActive},
		{Name: PRDDrafting, Category: beadsx.CategoryWIP},
		{Name: PRDReview, Category: beadsx.CategoryActive},
		{Name: PRDAmending, Category: beadsx.CategoryActive},
		{Name: PRDSignoff, Category: beadsx.CategoryActive},
		{Name: SpecAmending, Category: beadsx.CategoryActive},
		{Name: Architecture, Category: beadsx.CategoryActive},
		{Name: ADRReview, Category: beadsx.CategoryActive},
		{Name: Design, Category: beadsx.CategoryActive},
		{Name: DesignReview, Category: beadsx.CategoryActive},
		{Name: DesignSignoff, Category: beadsx.CategoryActive},
		{Name: SpecNeeded, Category: beadsx.CategoryActive},
		{Name: SpecDrafting, Category: beadsx.CategoryWIP},
		{Name: SpecReview, Category: beadsx.CategoryActive},
		{Name: SpecSignoff, Category: beadsx.CategoryActive},
		{Name: SpecApproved, Category: beadsx.CategoryActive},
		{Name: Decomposing, Category: beadsx.CategoryWIP},
		{Name: Execution, Category: beadsx.CategoryWIP},
		{Name: Escalated, Category: beadsx.CategoryFrozen},
		{Name: Done, Category: beadsx.CategoryDone, Terminal: true},
		{Name: Canceled, Category: beadsx.CategoryDone, Terminal: true},
	}
	live := []string{Backlog, PRDNeeded, PRDDrafting, PRDReview, PRDAmending, PRDSignoff, Architecture, ADRReview, Design, DesignReview,
		DesignSignoff, SpecNeeded, SpecDrafting, SpecReview, SpecSignoff, SpecAmending, SpecApproved, Decomposing, Execution, Escalated}
	claim := func(from, to, stage string) Row {
		return Row{Verb: "claim", From: []string{from}, To: to, Stage: stage, when: stageIs(stage),
			guard: claimGuard, BumpsEpoch: true, Lease: LeaseTake, Facts: []string{FactOwner, FactStage}}
	}
	rows := []Row{
		{Verb: VerbPRDSubmit, From: []string{Initial}, To: PRDReview, Command: "prd submit",
			set: copyParam("pr", FactDocPR), Facts: []string{FactDocPR}},
		// An unattended-brainstorm run's epic exists before its PRD, so this row records
		// the path that a room's create records.
		{Verb: VerbPRDSubmit, From: []string{PRDDrafting}, To: PRDReview, Command: "prd submit", Lease: LeaseDrop,
			set: all(needCopy("pr", FactDocPR), needCopy("path", FactPRDPath)), Facts: []string{FactDocPR, FactPRDPath}},
		{Verb: VerbCreatePRD, From: []string{Initial}, To: PRDNeeded, When: "no_open_dependency", when: paramIs("blocked", "")},
		{Verb: VerbCreatePRD, From: []string{Initial}, To: Backlog, When: "open_dependency", when: paramIs("blocked", "true"),
			set: setEpicEntry(PRDNeeded), Facts: []string{FactEpicEntry}},
		{Verb: VerbCreateArchitecture, From: []string{Initial}, To: Architecture, When: "no_open_dependency", when: paramIs("blocked", "")},
		{Verb: VerbCreateArchitecture, From: []string{Initial}, To: Backlog, When: "open_dependency", when: paramIs("blocked", "true"),
			set: setEpicEntry(Architecture), Facts: []string{FactEpicEntry}},
		// An epic with no epic_entry matches neither row, so unblock never
		// lands it on a guessed state.
		{Verb: VerbUnblock, From: []string{Backlog}, To: PRDNeeded, Command: "work unblock",
			When: "prd_entry", when: factIs(FactEpicEntry, PRDNeeded)},
		{Verb: VerbUnblock, From: []string{Backlog}, To: Architecture, Command: "work unblock",
			When: "architecture_entry", when: factIs(FactEpicEntry, Architecture)},

		claim(PRDNeeded, PRDDrafting, StageUnattendedBrainstorm),
		claim(PRDReview, ToSame, StageDocReviewer),
		claim(PRDAmending, ToSame, StageReviser),
		claim(ADRReview, ToSame, StageDocReviewer),
		claim(DesignReview, ToSame, StageDocReviewer),
		specClaim(claim(SpecNeeded, SpecDrafting, StagePlanner)),
		claim(SpecReview, ToSame, StageDocReviewer),
		claim(SpecAmending, ToSame, StageReviser),
		claim(SpecApproved, Decomposing, StageDecomposer),

		// A pass pins the head the reviewer read: the sign-off entry names
		// it, and the server merges only that head (factorio-8qm). A pass
		// with no head pins nothing, so it is refused (factorio-tf6).
		// The pass also writes the requirements that pass the ADR test, and
		// a pass that names none clears the list of an earlier pass.
		{Verb: "review_pass", From: []string{PRDReview}, To: PRDSignoff, Command: "review submit", Lease: LeaseDrop,
			set: all(needCopy("head", FactReviewedHead), adrNeeded), Facts: []string{FactReviewedHead, FactADRNeeded}},
		{Verb: "review_reject", From: []string{PRDReview}, To: PRDAmending, Command: "review submit", Lease: LeaseDrop,
			When: "first_reject", when: firstPRDReject, set: bumpPRDRejects, Facts: []string{FactPRDRejects}},
		{Verb: "review_reject", From: []string{PRDReview}, To: Escalated, Command: "review submit",
			When: WhenPRDCap, when: prdCap, Lease: LeaseDrop, Says: secondRejectSays,
			set: all(bumpPRDRejects, escalateTo(epicResumeFor)), Facts: []string{FactPRDRejects, FactResume, FactEscalation}},
		{Verb: "review_reject", From: []string{PRDReview}, To: Escalated, Command: "review submit",
			When: "returned_reject", when: returnedPRDReject, Lease: LeaseDrop, Says: returnedRejectSays,
			set: all(bumpPRDRejects, escalateTo(epicResumeFor)), Facts: []string{FactPRDRejects, FactResume, FactEscalation}},
		{Verb: VerbCapReturn, From: []string{Escalated}, To: PRDAmending, set: capReturn,
			Facts: []string{FactCapReturned, FactPRDRejects, FactResume, FactResumeEntry, FactEscalation}},
		{Verb: "prd_amended", From: []string{PRDAmending}, To: PRDSignoff, Lease: LeaseDrop,
			When: "unchanged", when: amendedUnchanged(PRDSignoff), set: clearAmend, Facts: []string{FactAmendFrom, FactAmendHead}},
		{Verb: "prd_amended", From: []string{PRDAmending}, To: PRDReview, Lease: LeaseDrop,
			set: clearAmend, Facts: []string{FactAmendFrom, FactAmendHead}},
		{Verb: "spec_amended", From: []string{SpecAmending}, To: SpecSignoff, Lease: LeaseDrop,
			When: "unchanged", when: amendedUnchanged(SpecSignoff), set: clearAmend, Facts: []string{FactAmendFrom, FactAmendHead}},
		{Verb: "spec_amended", From: []string{SpecAmending}, To: SpecReview, Lease: LeaseDrop,
			set: clearAmend, Facts: []string{FactAmendFrom, FactAmendHead}},
		// The PRD merge routes the epic: to the ADR stage when it asks for an
		// ADR, then to the design stage when the PRD holds a UI story.
		{Verb: "doc_merged", From: []string{PRDSignoff}, To: Architecture, When: "adr_requested", when: adrRequested,
			set: prdMerged, Facts: []string{FactPRDMerged, FactUIStories}},
		{Verb: "doc_merged", From: []string{PRDSignoff}, To: Design, When: "ui_stories", when: uiRoute,
			set: prdMerged, Facts: []string{FactPRDMerged, FactUIStories}},
		{Verb: "doc_merged", From: []string{PRDSignoff}, To: SpecNeeded, set: prdMerged, Facts: []string{FactPRDMerged, FactUIStories}},
		// The operator sends a PRD epic to the ADR stage. Before the PRD
		// merges, the send only marks the epic, and the merge routes it. In
		// the spec stage it moves the epic at once, and doc_pr keeps the
		// open spec pull request for the next spec round.
		{Verb: VerbADRSend, From: []string{PRDNeeded, PRDDrafting, PRDReview, PRDAmending, PRDSignoff}, To: ToSame, Command: "epic adr",
			set: needCopy("request", FactADRSent), Facts: []string{FactADRSent}},
		{Verb: VerbADRSend, From: []string{SpecNeeded, SpecReview, SpecSignoff, SpecAmending}, To: Architecture, Command: "epic adr",
			guard: unheld, set: needCopy("request", FactADRSent), Facts: []string{FactADRSent}},
		// The operator overrules the PRD review and the send at sign-off, so
		// the PRD merge lands the epic on spec_needed.
		{Verb: VerbADROff, From: []string{PRDSignoff}, To: ToSame, Command: "epic adr",
			set: adrOff, Facts: []string{FactADRNeeded, FactADRSent}},
		// The operator skips the design stage of an epic in design with no
		// open design pull request: the server reads the pull request, and
		// the epic goes to its spec with no record.
		{Verb: VerbSkipDesign, From: []string{Design}, To: SpecNeeded, Command: "epic skip-design",
			set: needCopy("request", FactDesignSkipped), Facts: []string{FactDesignSkipped}},
		// The operator accepts how a shipped screen differs from one state
		// of the merged design record. The server checks the state against
		// the record, and the epic and the record stay as they are.
		{Verb: VerbAcceptDifference, From: []string{Execution}, To: ToSame, Command: "epic accept-difference",
			guard: designRecorded, set: acceptDifference, Facts: []string{FactAcceptedDifferences}},
		// adr_merged is the first-parent commit that added the ADR, which the
		// base audit passes (ADR 0012). The tick's head is not always it. An
		// epic with a UI story and no design outcome designs next. One that
		// returns to architecture after its design goes on to its spec.
		{Verb: VerbADRLanded, From: []string{Architecture}, To: Design, When: "design_due", when: designDue,
			set: adrJoined, Facts: []string{FactADR, FactADRMerged}},
		{Verb: VerbADRLanded, From: []string{Architecture}, To: SpecNeeded, set: adrJoined, Facts: []string{FactADR, FactADRMerged}},
		// A merge that outran the ADR review ends it, as an outran document
		// merge does, and revokes the review lease.
		{Verb: VerbADRLanded, From: []string{ADRReview}, To: Design, When: "design_due", when: designDue, BumpsEpoch: true, Lease: LeaseDrop,
			set: adrJoined, Facts: []string{FactADR, FactADRMerged}},
		{Verb: VerbADRLanded, From: []string{ADRReview}, To: SpecNeeded, BumpsEpoch: true, Lease: LeaseDrop,
			set: adrJoined, Facts: []string{FactADR, FactADRMerged}},
		// The doc-reviewer reviews each ADR pull request of an epic in
		// architecture once at each head. The review is advisory: a verdict
		// returns the epic to architecture, and the merge stays the
		// operator's. Each verdict names its head, so the reactor opens no
		// second review at it.
		{Verb: VerbADROpened, From: []string{Architecture}, To: ADRReview,
			set: all(needCopy("pr", FactADRPR), needCopy("head", FactADRHead)), Facts: []string{FactADRPR, FactADRHead}},
		{Verb: "review_pass", From: []string{ADRReview}, To: Architecture, Command: "review submit", Lease: LeaseDrop,
			set: needCopy("head", FactReviewedHead), Facts: []string{FactReviewedHead}},
		{Verb: "review_reject", From: []string{ADRReview}, To: Architecture, Command: "review submit", Lease: LeaseDrop,
			guard: headNamed},
		// The ADR pull request closed with no merge, so nothing is left to
		// review.
		{Verb: VerbADRWithdrawn, From: []string{ADRReview}, To: Architecture, BumpsEpoch: true, Lease: LeaseDrop},
		// An epic that an ADR named only in prose returns to wait for its
		// own ADR (F-10). The server alone performs it.
		{Verb: VerbADRMisjoined, From: []string{SpecNeeded, Design}, To: Architecture, guard: adrIs, set: clearFacts(FactADR, FactADRMerged),
			Facts: []string{FactADR, FactADRMerged}},
		{Verb: "pr_open", From: []string{SpecDrafting}, To: SpecReview, Lease: LeaseDrop,
			set: copyParam("pr", FactDocPR), Facts: []string{FactDocPR}},
		{Verb: "review_pass", From: []string{SpecReview}, To: SpecSignoff, Command: "review submit", Lease: LeaseDrop,
			set: needCopy("head", FactReviewedHead), Facts: []string{FactReviewedHead}},
		{Verb: "review_reject", From: []string{SpecReview}, To: SpecNeeded, Command: "review submit", Lease: LeaseDrop},
		{Verb: "doc_merged", From: []string{SpecSignoff}, To: SpecApproved,
			set: all(copyParam("spec_path", FactSpecPath), copyParam("sha", FactSpecMerged)), Facts: []string{FactSpecPath, FactSpecMerged}},
		// The design room opens the design pull request, and the
		// doc-reviewer reviews it once at each head. A reject returns the
		// epic to the room, which revises the record on the same pull
		// request. Only the operator's approval at sign-off merges it.
		{Verb: "pr_open", From: []string{Design}, To: DesignReview, set: needCopy("pr", FactDocPR), Facts: []string{FactDocPR}},
		{Verb: "review_pass", From: []string{DesignReview}, To: DesignSignoff, Command: "review submit", Lease: LeaseDrop,
			set: needCopy("head", FactReviewedHead), Facts: []string{FactReviewedHead}},
		{Verb: "review_reject", From: []string{DesignReview}, To: Design, Command: "review submit", Lease: LeaseDrop,
			guard: headNamed},
		{Verb: "doc_merged", From: []string{DesignSignoff}, To: SpecNeeded,
			set: all(copyParam("design_path", FactDesignPath), copyParam("sha", FactDesignMerged)), Facts: []string{FactDesignPath, FactDesignMerged}},
		{Verb: VerbDecomposed, From: []string{Decomposing}, To: Execution, Lease: LeaseDrop},
		{Verb: "complete", From: []string{Execution}, To: Done, guard: childrenTerminal},
		// An epic whose work moved elsewhere closes before it has children
		// (F-55). Decomposing cuts the children, so it cannot cancel.
		{Verb: "cancel", From: except(live, Decomposing, Execution), To: Canceled, Command: "work cancel", Lease: LeaseDrop,
			set: needCopy("reason", FactCancelReason), Facts: []string{FactCancelReason}},
		{Verb: VerbAnswer, From: []string{Escalated}, To: ToResume, ResumeTo: uniqueValues(epicResume),
			set: answerResume, Facts: []string{FactResume, FactEscalation, FactAnswer, FactAmendment}},

		// A document merged while still in review counts as sign-off and
		// revokes the review lease, as the outran rows of
		// scripts/sdlc/document-states.ts:141-145 (alpha) did. It lands on
		// sign-off and never past it.
		{Verb: "doc_merged", From: []string{PRDReview}, To: PRDSignoff, When: "outran", BumpsEpoch: true, Lease: LeaseDrop},
		{Verb: "doc_merged", From: []string{SpecReview}, To: SpecSignoff, When: "outran", BumpsEpoch: true, Lease: LeaseDrop},
		{Verb: "doc_merged", From: []string{DesignReview}, To: DesignSignoff, When: "outran", BumpsEpoch: true, Lease: LeaseDrop},
		{Verb: "doc_merged", From: []string{PRDAmending}, To: PRDSignoff, When: "outran", BumpsEpoch: true, Lease: LeaseDrop,
			set: clearAmend, Facts: []string{FactAmendFrom, FactAmendHead}},
		{Verb: "doc_merged", From: []string{SpecAmending}, To: SpecSignoff, When: "outran", BumpsEpoch: true, Lease: LeaseDrop,
			set: clearAmend, Facts: []string{FactAmendFrom, FactAmendHead}},

		// The document PR moved after the entry was raised. The server pins
		// the new head, and the sign-off entry is raised again at it, so
		// the operator approves only a head the entry named.
		{Verb: VerbSignoffHead, From: []string{PRDSignoff, SpecSignoff, DesignSignoff}, To: ToSame,
			set: needCopy("head", FactReviewedHead), Facts: []string{FactReviewedHead}},

		// A person's unanswered comment on the document PR (1.12).
		{Verb: VerbPersonComment, From: []string{PRDReview, PRDSignoff}, To: PRDAmending,
			guard: unheld, set: personComment, Facts: []string{FactAmendFrom, FactAmendHead}},
		{Verb: VerbPersonComment, From: []string{SpecReview, SpecSignoff}, To: SpecAmending,
			guard: unheld, set: personComment, Facts: []string{FactAmendFrom, FactAmendHead}},

		// A closed document PR escalates the epic and never cancels it.
		{Verb: VerbPRClosed, From: []string{PRDReview, PRDAmending, PRDSignoff, SpecReview, SpecSignoff, SpecAmending}, To: Escalated,
			Lease: LeaseDrop, set: escalateTo(epicResumeFor), Facts: []string{FactResume, FactEscalation}},
		// A closed design pull request leaves nothing to sign off, so the
		// answer returns the epic to the design room, which opens another.
		{Verb: VerbPRClosed, From: []string{DesignReview, DesignSignoff}, To: Escalated, Lease: LeaseDrop,
			set: escalateTo(func(string) (string, string) { return Design, "" }), Facts: []string{FactResume, FactEscalation}},
		{Verb: VerbEscalate, From: except(live, Escalated), To: Escalated, Lease: LeaseDrop,
			set: escalateTo(epicResumeFor), Facts: []string{FactResume, FactEscalation}},
		{Verb: VerbNote, From: live, To: ToSame, Command: "work note", set: notePut, Facts: []string{FactNote}},
		editRow(live),
	}
	rows = withReviews(rows)
	pairs := [][2]string{{PRDDrafting, PRDNeeded}, {SpecDrafting, SpecNeeded}, {Decomposing, SpecApproved},
		{PRDReview, PRDReview}, {PRDAmending, PRDAmending}, {ADRReview, ADRReview}, {DesignReview, DesignReview},
		{SpecReview, SpecReview}, {SpecAmending, SpecAmending}}
	rows = append(rows, releaseRows("release", false, pairs, nil)...)
	rows = append(rows, releaseRows("reclaim", true, pairs, nil)...)
	return &Table{Kind: KindEpic, BeadType: "epic", States: states, Rows: rows}
}

// uniqueValues lists the targets of m once each, in state order.
func uniqueValues(m map[string]string) []string {
	values := slices.Collect(maps.Values(m))
	var out []string
	for _, s := range epicStateOrder() {
		if slices.Contains(values, s) {
			out = append(out, s)
		}
	}
	return out
}

func epicStateOrder() []string {
	return []string{Backlog, PRDNeeded, PRDDrafting, PRDReview, PRDAmending, PRDSignoff, Architecture, ADRReview, Design, DesignReview,
		DesignSignoff, SpecNeeded, SpecDrafting, SpecReview, SpecSignoff, SpecAmending, SpecApproved, Decomposing, Execution}
}
