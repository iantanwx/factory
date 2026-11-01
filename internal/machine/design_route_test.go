package machine

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

const uiLabels = "US-01,US-03"

// prdInSignoff makes a PRD epic in prd_signoff, with facts.
func (w *world) prdInSignoff(facts map[string]string) string {
	w.t.Helper()
	ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
	w.must(ep, "review_pass", map[string]string{"head": shaA})
	if facts != nil {
		w.force(ep, PRDSignoff, facts)
	}
	return ep
}

func (w *world) prdMerge(ep, uiStories string) {
	w.t.Helper()
	mustOK(w.t, w.m.MergeObserved(context.Background(), MergeEvent{Project: prj, Item: ep, Base: "main", Into: "main", SHA: "m1",
		UIStories: uiStories}))
}

// C12: the PRD merge of a UI epic lands on design, with ui_stories holding
// each UI: yes label and prd_merged set.
func TestC12PRDMergeOfAUIEpicLandsOnDesign(t *testing.T) {
	w := newWorld(t)
	ep := w.prdInSignoff(nil)
	w.prdMerge(ep, uiLabels)
	if it := w.show(ep); it.Status != Design || it.Fact(FactUIStories) != uiLabels || it.Fact(FactPRDMerged) != "m1" {
		t.Fatalf("after the PRD merge: %s with ui_stories=%q prd_merged=%q, want design with %s and m1",
			it.Status, it.Fact(FactUIStories), it.Fact(FactPRDMerged), uiLabels)
	}
}

// C12: a UI epic whose PRD review asked for an ADR goes to architecture
// first, and keeps its UI stories for the ADR merge.
func TestC12PRDMergeOfAUIEpicThatNeedsAnADRKeepsItsUIStories(t *testing.T) {
	w := newWorld(t)
	ep := w.prdInSignoff(map[string]string{FactADRNeeded: adrNeededWords})
	w.prdMerge(ep, uiLabels)
	if it := w.show(ep); it.Status != Architecture || it.Fact(FactUIStories) != uiLabels {
		t.Fatalf("after the PRD merge: %s with ui_stories=%q, want architecture with %s", it.Status, it.Fact(FactUIStories), uiLabels)
	}
}

// C14: a PRD merge with no UI story lands on spec_needed, as today, and
// writes no ui_stories.
func TestC14PRDMergeWithNoUIStoryLandsOnSpecNeeded(t *testing.T) {
	w := newWorld(t)
	ep := w.prdInSignoff(nil)
	w.prdMerge(ep, "")
	it := w.show(ep)
	if _, ok := it.Facts[FactUIStories]; it.Status != SpecNeeded || ok {
		t.Fatalf("after the PRD merge: %s with facts %v, want spec_needed and no ui_stories", it.Status, it.Facts)
	}
	w.claim(ep, StagePlanner, "r-spec")
}

// C13: an ADR that lands for a UI epic with no design outcome moves it to
// design, from architecture and from adr_review. An epic that already
// holds design_merged or design_skipped goes on to spec_needed.
func TestC13ADRLandingRoutesAUIEpicByItsDesignOutcome(t *testing.T) {
	for _, c := range []struct {
		name  string
		facts map[string]string
		want  string
	}{
		{"no design outcome", nil, Design},
		{"design merged", map[string]string{FactDesignMerged: "d1"}, SpecNeeded},
		{"design skipped", map[string]string{FactDesignSkipped: "The operator: skip it"}, SpecNeeded},
	} {
		for _, from := range []string{Architecture, ADRReview} {
			t.Run(c.name+" from "+from, func(t *testing.T) {
				w := newWorld(t)
				ep := w.create(KindEpic, "create_prd", nil)
				facts := map[string]string{FactPRDMerged: "m1", FactUIStories: uiLabels}
				for k, v := range c.facts {
					facts[k] = v
				}
				w.force(ep, Architecture, facts)
				if from == ADRReview {
					w.must(ep, VerbADROpened, map[string]string{"pr": "12", "head": shaA})
					w.claim(ep, StageDocReviewer, "r-adr")
				}
				w.must(ep, VerbADRLanded, map[string]string{"path": "docs/adr/0001-x.md", "added": "a1"})
				if it := w.show(ep); it.Status != c.want || it.Owner != "" || it.Fact(FactADR) != "docs/adr/0001-x.md" {
					t.Fatalf("after the ADR landed: %s owner %q adr=%q, want %s with no owner", it.Status, it.Owner, it.Fact(FactADR), c.want)
				}
			})
		}
	}
}

// C13: an epic in design that an ADR named only in prose returns to
// architecture.
func TestC13MisjoinedEpicInDesignReturnsToArchitecture(t *testing.T) {
	w := newWorld(t)
	ep := w.create(KindEpic, "create_prd", nil)
	w.force(ep, Design, map[string]string{FactPRDMerged: "m1", FactUIStories: uiLabels, FactADR: "docs/adr/0001-x.md", FactADRMerged: "a1"})
	w.must(ep, VerbADRMisjoined, map[string]string{"path": "docs/adr/0001-x.md"})
	if it := w.show(ep); it.Status != Architecture || it.Fact(FactADR) != "" {
		t.Fatalf("after adr_misjoined: %s with adr=%q, want architecture with no adr", it.Status, it.Fact(FactADR))
	}
}

// C15: the planner's claim refuses an epic that holds ui_stories and no
// design outcome, with rule design_required. The record's merge or the
// operator's skip lets it claim.
func TestC15PlannerClaimRefusesAUIEpicWithNoDesignOutcome(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.create(KindEpic, "create_prd", nil)
	w.force(ep, SpecNeeded, map[string]string{FactPRDMerged: "m1", FactUIStories: uiLabels})
	_, res := w.m.Claim(ctx, ClaimRequest{Project: prj, Item: ep, Stage: StagePlanner, RunID: "r-spec"})
	if r := wantCode(t, res, cli.CodePRDRequired); r.Rule != "design_required" {
		t.Fatalf("rule = %q, want design_required", r.Rule)
	}
	if it := w.show(ep); it.Status != SpecNeeded || it.Owner != "" {
		t.Fatalf("a refused claim moved the epic: %s owner %q", it.Status, it.Owner)
	}
	for i, outcome := range []string{FactDesignMerged, FactDesignSkipped} {
		ok := w.create(KindEpic, "create_prd", nil)
		w.force(ok, SpecNeeded, map[string]string{FactPRDMerged: "m1", FactUIStories: uiLabels, outcome: "yes"})
		w.claim(ok, StagePlanner, fmt.Sprintf("r-spec-%d", i))
	}
}

// C15: for any sequence of table verbs from prd_signoff, an epic whose PRD
// merged with UI stories is in spec_drafting only if it holds
// design_merged or design_skipped. Each seed draws a sequence: each step
// applies one verb that the table allows from the epic's status.
func TestC15AUIEpicReachesTheSpecOnlyThroughItsRecordOrASkip(t *testing.T) {
	epic, _ := TableFor(KindEpic)
	stages := func(status string) []string {
		var out []string
		for _, r := range epic.Rows {
			if r.Verb == "claim" && slices.Contains(r.From, status) {
				out = append(out, r.Stage)
			}
		}
		return out
	}
	for seed := range uint64(16) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, seed))
			w := newWorld(t)
			ep := w.prdInSignoff(nil)
			ui := false
			for step := range 40 {
				it := w.show(ep)
				if epic.Terminal(it.Status) {
					return
				}
				// A cancel ends the sequence, so it is left out.
				verbs := slices.DeleteFunc(epic.AllowedNext(it.Status), func(v string) bool { return v == "cancel" })
				verb := verbs[rng.IntN(len(verbs))]
				params := map[string]string{"sha": "m1", "ui_stories": uiLabels, "path": "docs/adr/0001-x.md", "added": "a1",
					"request": "The operator: go on", "head": shaA, "pr": "9", "run": fmt.Sprintf("r-%d", step), "body": "b"}
				if verb == "claim" {
					s := stages(it.Status)
					params["stage"] = s[rng.IntN(len(s))]
				}
				res := w.apply(ep, verb, params)
				if res.Refusal == nil && it.Status == PRDSignoff && verb == "doc_merged" {
					ui = true
				}
				now := w.show(ep)
				if ui && now.Status == SpecDrafting && now.Fact(FactDesignMerged) == "" && now.Fact(FactDesignSkipped) == "" {
					t.Fatalf("step %d: %s from %s put a UI epic in spec_drafting with no design outcome: %v", step, verb, it.Status, now.Facts)
				}
			}
		})
	}
}
