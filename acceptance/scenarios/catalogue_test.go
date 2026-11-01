//go:build acceptance

package scenarios

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/machine"
)

// tableOnly names each ticket or epic row that no CLI verb, reactor path
// or scheduler path performs in v1, with the unit test that applies it.
// A key is "kind verb", "kind verb[selector]" for one row of a verb, or
// "kind verb from" for the row of a verb from one state.
// Every other row of the two tables must be applied end to end by a
// scenario of a full run.
var tableOnly = map[string]string{
	// factorio-4n0: no verb creates a PRD epic or a blocked epic until
	// `epic create --request` and `--blocked-by` land, so no epic reaches
	// backlog for `work unblock` to move.
	"epic create_prd": "TestEpicEntersByItsBlockers",
	"epic create_architecture[open_dependency]": "TestEpicEntersByItsBlockers",
	"epic unblock": "TestUnblockLandsOnTheRecordedEntry",
	// No epic reaches prd_needed until `epic create --request` lands, so no
	// unattended-brainstorm run claims one.
	"epic claim prd_needed":     "TestOnlyAnUnattendedBrainstormClaimsAPRDEpic",
	"epic release prd_drafting": "TestASettledUnattendedBrainstormReturnsTheEpicToPRDNeeded",
	"epic reclaim prd_drafting": "TestASettledUnattendedBrainstormReturnsTheEpicToPRDNeeded",
	// No scenario starts an unattended-brainstorm run yet, so no run submits the PRD of
	// the epic it holds. The key names the state, so a room's submit from
	// initial stays checked end to end.
	"epic prd_submit prd_drafting": "TestC10ARunSubmitsThePRDOfTheEpicItHolds",
	// No project runs the simplify stage until it is on by default (spec
	// factorio-1r94, Work entry 4), so no scenario starts a simplify run.
	// That entry's scenario applies these rows end to end.
	"ticket claim in_simplify":   "TestC3NewTicketEntersInSimplify",
	"ticket simplified":          "TestSimplifyRunHandsTheTicketToReview",
	"ticket release in_simplify": "TestC7EveryOtherEndPassesTheTicketThrough",
	"ticket reclaim in_simplify": "TestC7EveryOtherEndPassesTheTicketThrough",
	// factorio-nhb5: no scenario project lists stages.qa until the stage is
	// on by default, so no review pass lands on in_qa and no QA run claims
	// a ticket. No CLI verb performs qa_pass or qa_fail until `loop qa
	// report` lands, and no sweep applies qa_off or qa_due until the sweep
	// ticket lands.
	"ticket review_pass[qa_on]": "TestC5AReviewPassLandsOnInQAWithTheStageOn",
	"ticket claim in_qa":        "TestC6OnlyTheQAStageClaimsFromInQA",
	"ticket release in_qa":      "TestAQARunWithNoReportLeavesTheTicketInQA",
	"ticket reclaim in_qa":      "TestAQARunWithNoReportLeavesTheTicketInQA",
	"ticket qa_pass":            "TestC9AQAPassLandsOnReadyToMergeAtTheReviewedHead",
	"ticket qa_fail":            "TestC10AQAFailIsAReject",
	"ticket qa_off":             "TestEveryEdgeAppliesAndEveryOtherPairRefuses",
	"ticket qa_due":             "TestEveryEdgeAppliesAndEveryOtherPairRefuses",
}

// rowName names one row: its kind, verb and selector, and its edge.
func rowName(k machine.Kind, r machine.Row) string {
	v := r.Verb
	if r.When != "" {
		v += "[" + r.When + "]"
	}
	return fmt.Sprintf("%s %s %s -> %s", k, v, strings.Join(r.From, ","), r.To)
}

// covers reports whether an applied transition performed row r.
func covers(r machine.Row, t acceptance.Transition) bool {
	if t.Verb != r.Verb || !slices.Contains(r.From, t.From) {
		return false
	}
	switch r.To {
	case machine.ToSame:
		return t.To == t.From
	case machine.ToResume:
		return slices.Contains(r.ResumeTo, t.To)
	}
	return t.To == r.To
}

// checkCatalogue holds the run to the row catalogue. A row of the ticket
// or epic table that is in neither tableOnly nor the applied history of a
// full run fails it, and so does a stage kind no scenario started. A run
// with -run checks the table's names only.
func checkCatalogue() int {
	code := 0
	full := flag.Lookup("test.run").Value.String() == ""
	var rows, applied int
	for _, tb := range machine.Tables() {
		if tb.Kind != machine.KindTicket && tb.Kind != machine.KindEpic {
			continue
		}
		for _, r := range tb.Rows {
			rows++
			name := rowName(tb.Kind, r)
			key := string(tb.Kind) + " " + r.Verb
			if _, ok := tableOnly[key]; ok {
				continue
			}
			if _, ok := tableOnly[key+"["+r.When+"]"]; ok && r.When != "" {
				continue
			}
			if _, ok := tableOnly[key+" "+strings.Join(r.From, ",")]; ok {
				continue
			}
			hit := slices.ContainsFunc(observed, func(t acceptance.Transition) bool {
				return t.Kind == string(tb.Kind) && covers(r, t)
			})
			if hit {
				applied++
			} else if full {
				fmt.Fprintf(os.Stderr, "catalogue: no scenario applied %s\n", name)
				code = 1
			}
		}
	}
	if full {
		for _, s := range checkStages() {
			fmt.Fprintf(os.Stderr, "catalogue: no scenario started a %s\n", s)
			code = 1
		}
		fmt.Printf("catalogue: %d of %d ticket and epic rows applied end to end, %d table-only\n", applied, rows, len(tableOnly))
	}
	return code
}
