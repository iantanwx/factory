package machine

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/iantanwx/loop/internal/cli"
)

// Fact names. Each is stored in bead metadata under "loop." + name.
const (
	FactEntry        = "entry"         // todo: new, rework or resume
	FactResume       = "resume"        // the state answer returns to
	FactResumeEntry  = "resume_entry"  // the entry answer writes with a todo resume
	FactReviewedHead = "reviewed_head" // the head a review passed at
	FactRejectedHead = "rejected_head" // the head the last review reject named
	FactHead         = "head"          // the PR head the server last saw
	FactRejects      = "rejects"       // review rejects of a ticket
	FactModelTier    = "model_tier"    // escalated after two rejects
	FactPRDRejects   = "prd_rejects"   // review rejects of an epic's PRD
	FactOwner        = "owner"         // the run that holds the lease
	FactStage        = "stage"         // the stage of that lease
	FactNote         = "note"          // the last note
	FactMergedSHA    = "merged_sha"    // the merge commit
	FactTicket       = "ticket"        // the ticket a bug became
	FactDuplicateOf  = "duplicate_of"  // the item a bug duplicates
	FactEpic         = "epic"          // a child's epic
	FactEscalation   = "escalation"    // the escalation that sent the item to a person
	FactAnswer       = "answer"        // the last answer a person gave through the inbox
	FactAmendment    = "amendment"     // an answer that amends the task; it enters the inputs (R10)
	FactDocPR        = "doc_pr"        // the PR number of the epic's current document
	FactAmendFrom    = "amend_from"    // the state a person's comment found the document in
	FactAmendHead    = "amend_head"    // the document PR's head when that comment moved it
	FactPRDPath      = "prd_path"      // the PRD's path in the repository
	FactPRDMerged    = "prd_merged"    // the merge commit of the epic's PRD: the operator's sign-off
	FactADR          = "adr"           // the path of the ADR that replaced the PRD of an architecture epic, on the base
	FactEpicEntry    = "epic_entry"    // prd_needed or architecture: where unblock sends a blocked epic
	FactCreateKey    = "create_key"    // the natural key work entry created the item under
	FactStories      = "stories"       // the PRD story labels a ticket cites, comma-separated (R45)
	FactRequest      = "request"       // the operator's request a PRD-less ticket cites (R48)
	FactReview       = "review"        // the body of the last review verdict
	FactReaders      = "readers"       // the doc readers the last review verdict named, comma-separated
	FactTitle        = "title"         // the title work edit wrote; it replaces the bead's title
	FactBody         = "body"          // the body work edit wrote; it replaces the bead's description
	FactBug          = "bug"           // the bug a shaped ticket came from
	FactAsks         = "asks"          // the asks of a bug's reporter
	FactQuestion     = "question"      // a reproducer's ask of the reporter, kept per round as question_<n> (AskFact)
	FactAskedBy      = "asked_by"      // the actor that asked it
	// FactSupersededBy is the checked evidence that closed a ticket with no
	// work left: "commit <sha>", "done <item>" or "adr <path>".
	FactSupersededBy    = "superseded_by"
	FactSupersedeReason = "supersede_reason" // how that evidence answers the ticket
	FactCancelReason    = "cancel_reason"    // why work cancel closed the item
	FactPendingBlock    = "pending_block"    // the blocker of the latest work block, written before its edge
	FactBuiltBy         = "built_by"         // the ticket or epic a waiting bug waits for
	FactPriority        = "priority"         // the priority work shape gave the ticket: 1 to 4
	FactWhy             = "why"              // the reason of a decision's latest fire or defer
	FactDeferReason     = "defer_reason"     // the --reason of the inbox defer that the latest fire ended
	FactCloseNote       = "close_note"       // why a decision was dropped or became moot
	FactSpecPath        = "spec_path"        // the spec's path in the repository, set when the spec merges
	FactSpecMerged      = "spec_merged"      // the merge commit of the epic's spec
	FactADRMerged       = "adr_merged"       // the first-parent commit that added the epic's ADR
	FactADRNeeded       = "adr_needed"       // the PRD review's list of requirements that pass the ADR test
	FactADRSent         = "adr_sent"         // the operator's words that sent the epic to the ADR stage
	FactADRPR           = "adr_pr"           // the number of the ADR pull request under review
	FactADRHead         = "adr_head"         // that pull request's head when adr_opened read it
	FactShape           = "shape"            // the body shape a ticket was checked against: ticket or bug
	FactUIStories       = "ui_stories"       // the labels of the PRD's UI: yes stories at its merge, comma-separated
	FactDesignPath      = "design_path"      // the merged design record's path
	FactDesignMerged    = "design_merged"    // the merge commit of the design pull request
	FactDesignSkipped   = "design_skipped"   // the operator's words that skipped the design stage
	FactScreens         = "screens"          // the state ids a ticket's **Screens:** line cites, comma-separated, in line order
	// FactQAHead (bug.go) on a ticket: the head of the latest QA pass
	FactQACarriedFrom = "qa_carried_from" // the head whose QA pass base_merged carried
	FactSimplifyDue   = "simplify_due"    // "true" when the next implementer hand-off enters the simplify run
	FactSimplifyFrom  = "simplify_from"   // the implementer's head that the latest simplify round started from
)

// Entry values of a todo ticket (R6).
const (
	EntryNew    = "new"
	EntryRework = "rework"
	EntryResume = "resume"
)

// ModelTierEscalated routes the next implementer run to the stronger model.
const ModelTierEscalated = "escalated"

type setter func(c *change) *cli.Refusal

// all runs each setter in order and stops at the first refusal.
func all(fns ...setter) setter {
	return func(c *change) *cli.Refusal {
		for _, f := range fns {
			if r := f(c); r != nil {
				return r
			}
		}
		return nil
	}
}

func setEntry(v string) setter { return setTo(FactEntry, v) }

func setTo(fact, v string) setter {
	return func(c *change) *cli.Refusal { c.setFact(fact, v); return nil }
}

func clearFacts(names ...string) setter {
	return func(c *change) *cli.Refusal { c.unsetFact(names...); return nil }
}

// copyParam stores an optional parameter as a fact.
func copyParam(param, fact string) setter {
	return func(c *change) *cli.Refusal {
		if v := c.param(param); v != "" {
			c.setFact(fact, v)
		}
		return nil
	}
}

// needCopy stores a required parameter as a fact.
func needCopy(param, fact string) setter {
	return func(c *change) *cli.Refusal {
		if r := c.needParam(param); r != nil {
			return r
		}
		c.setFact(fact, c.param(param))
		return nil
	}
}

// editTask stores the text work edit sent. Each field it sent replaces the
// one before. TaskText reads them.
func editTask(c *change) *cli.Refusal {
	if c.param("title") == "" && c.param("body") == "" {
		return &cli.Refusal{Code: cli.CodeUsage, Rule: "nothing_to_edit", Item: c.item.ID, Hint: "name the new text with --title, --body or both"}
	}
	if t := c.param("title"); t != "" {
		c.setFact(FactTitle, t)
	}
	if b := c.param("body"); b != "" {
		c.setFact(FactBody, b)
	}
	return nil
}

// editRow is the work edit row of a kind, from each state that is not
// terminal.
func editRow(from []string) Row {
	return Row{Verb: "edit", From: from, To: ToSame, Command: "work edit", set: editTask, Facts: []string{FactTitle, FactBody}}
}

// recordReview stores the body of a verdict as the review fact, or clears
// the fact of an earlier round when the verdict has no body. It stores the
// doc readers the verdict named the same way. The readers fact is left out
// of the rows' facts, since the epic table must not change (factorio-kt4y).
func recordReview(c *change) *cli.Refusal {
	if b := c.param("body"); b != "" {
		c.setFact(FactReview, b)
	} else {
		c.unsetFact(FactReview)
	}
	if r := c.param("readers"); r != "" {
		c.setFact(FactReaders, r)
	} else {
		c.unsetFact(FactReaders)
	}
	return nil
}

// recordRejectedHead stores the head a reject named, so review rounds
// lists it. reviewed_head stays the head of a pass only.
func recordRejectedHead(c *change) *cli.Refusal {
	if h := c.param("head"); h != "" {
		c.setFact(FactRejectedHead, h)
	} else {
		c.unsetFact(FactRejectedHead)
	}
	return nil
}

// withReviews makes every row that review submit or qa report performs
// carry the review fact, and every reject row the head it named. So the
// implementer reads a QA fail where it reads a review reject.
func withReviews(rows []Row) []Row {
	for i, r := range rows {
		if r.Command != "review submit" && r.Command != "qa report" {
			continue
		}
		record, facts := recordReview, []string{FactReview}
		if r.Verb == "review_reject" || r.Verb == VerbQAFail {
			record, facts = all(recordRejectedHead, recordReview), []string{FactRejectedHead, FactReview}
		}
		if r.set == nil {
			rows[i].set = record
		} else {
			rows[i].set = all(r.set, record)
		}
		rows[i].Facts = append(slices.Clone(r.Facts), facts...)
	}
	return rows
}

func notePut(c *change) *cli.Refusal {
	if r := c.needParam("body"); r != nil {
		return r
	}
	c.setFact(FactNote, c.param("body"))
	return nil
}

// stageIs selects the claim row of one stage.
func stageIs(stage string) func(c *change) bool {
	return func(c *change) bool { return c.param("stage") == stage }
}

func paramIs(name, value string) func(c *change) bool {
	return func(c *change) bool { return c.param(name) == value }
}

func factIs(name, value string) func(c *change) bool {
	return func(c *change) bool { return c.fact(name) == value }
}

// claimGuard refuses a claim on a held lease, then asks admission.
func claimGuard(c *change) *cli.Refusal {
	if r := c.needParam("run"); r != nil {
		return r
	}
	if c.item.Owner != "" {
		return &cli.Refusal{Code: cli.CodeLeaseHeld, Rule: "lease_held", Item: c.item.ID, CurrentStatus: c.item.Status,
			Hint: fmt.Sprintf("%s belongs to run %s until that run settles", c.item.ID, c.item.Owner)}
	}
	if g := c.m.cfg.Claims; g != nil {
		return g.AdmitClaim(c.ctx, ClaimCheck{
			Project: c.item.Project, Item: c.item.ID, Kind: c.item.Kind, Status: c.item.Status,
			Stage: c.param("stage"), RunID: c.param("run"), Facts: cloneSet(c.item.Facts),
		})
	}
	return nil
}

// escalateTo stores the resume target in the escalate write itself (R12).
func escalateTo(resume func(status string) (target, entry string)) setter {
	return func(c *change) *cli.Refusal {
		target, entry := resume(c.item.Status)
		c.setFact(FactResume, target)
		if e := c.param("escalation"); e != "" {
			c.setFact(FactEscalation, e)
		}
		if entry != "" {
			c.setFact(FactResumeEntry, entry)
		} else {
			c.unsetFact(FactResumeEntry)
		}
		return nil
	}
}

// answerResume lands on exactly the stored resume target.
func answerResume(c *change) *cli.Refusal {
	target := c.fact(FactResume)
	if !slices.Contains(c.row.ResumeTo, target) {
		return &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "no_resume_target", Item: c.item.ID,
			CurrentStatus: c.item.Status, AllowedNext: c.table.AllowedNext(c.item.Status),
			Hint: fmt.Sprintf("%s stores resume target %q, which answer cannot land on", c.item.ID, target)}
	}
	c.to = target
	if e := c.fact(FactResumeEntry); e != "" {
		c.setFact(FactEntry, e)
	}
	c.unsetFact(FactResume, FactResumeEntry, FactEscalation)
	return answerFacts(c)
}

// answerFacts stores a person's answer, and an amendment of the task that
// enters its retry identity (R10).
func answerFacts(c *change) *cli.Refusal {
	if a := c.param("answer"); a != "" {
		c.setFact(FactAnswer, a)
	}
	if a := c.param("amendment"); a != "" {
		c.setFact(FactAmendment, a)
	}
	return nil
}

// count reads a counter fact. A missing or broken value is 0.
func count(c *change, name string) int {
	n, err := strconv.Atoi(c.fact(name))
	if err != nil {
		return 0
	}
	return n
}

// release returns an in-flight state to the queue it came from, the way
// RESUME_TARGETS did (scripts/sdlc/handoff.ts:55, alpha). A stage that
// claims without a move gets a row that keeps the state.
func releaseRows(verb string, bump bool, pairs [][2]string, extra map[string]setter) []Row {
	var out []Row
	for _, p := range pairs {
		r := Row{Verb: verb, From: []string{p[0]}, To: p[1], Lease: LeaseDrop, BumpsEpoch: bump}
		if p[0] == p[1] {
			r.To = ToSame
		}
		if s, ok := extra[p[0]]; ok {
			r.set = s
			r.Facts = []string{FactEntry}
		}
		out = append(out, r)
	}
	return out
}

func except(states []string, drop ...string) []string {
	return slices.DeleteFunc(slices.Clone(states), func(s string) bool { return slices.Contains(drop, s) })
}
