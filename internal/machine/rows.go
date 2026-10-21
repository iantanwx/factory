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
	FactEscalation   = "escalation"    // the escalation that sent the item to a person (U12)
	FactAnswer       = "answer"        // the last answer a person gave through the inbox (U12)
	FactAmendment    = "amendment"     // an answer that amends the task; it enters the inputs (R10)
	FactDocPR        = "doc_pr"        // the PR number of the epic's current document (U13)
	FactPRDPath      = "prd_path"      // the PRD's path in the repository (U13)
	FactPRDMerged    = "prd_merged"    // the merge commit of the epic's PRD: the operator's sign-off (U13)
	FactADR          = "adr"           // the ADR that replaced the PRD of an architecture epic landed (U13)
	FactCreateKey    = "create_key"    // the natural key work entry created the item under (U13)
	FactStories      = "stories"       // the PRD story labels a ticket cites, comma-separated (R45)
	FactRequest      = "request"       // the operator's request a PRD-less ticket cites (R48)
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

func setEntry(v string) setter {
	return func(c *change) *cli.Refusal { c.setFact(FactEntry, v); return nil }
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

// claimGuard refuses a claim on a held lease, then asks admission (U6).
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
// enters its retry identity (R10, U12).
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
