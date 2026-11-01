package machine

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

const skipWords = "Ian: no design for this one, the screen is a copy of the settings page."

// C16: the operator's skip moves an epic in design to spec_needed, and
// design_skipped holds the words exactly. The planner can then claim it.
func TestC16SkipDesignMovesTheEpicAndKeepsTheWords(t *testing.T) {
	w := newWorld(t)
	ep := w.create(KindEpic, "create_prd", nil)
	w.force(ep, "design", map[string]string{"prd_merged": "m1", "ui_stories": uiLabels})
	w.must(ep, "skip_design", map[string]string{"request": skipWords})
	if it := w.show(ep); it.Status != "spec_needed" || it.Fact("design_skipped") != skipWords {
		t.Fatalf("after the skip: status %s, design_skipped %q", it.Status, it.Fact("design_skipped"))
	}
	if _, res := w.m.Claim(context.Background(), ClaimRequest{Project: prj, Item: ep, Stage: StagePlanner, RunID: "r-plan"}); res.Refusal != nil {
		t.Fatalf("the planner's claim after the skip: %+v", *res.Refusal)
	}
}

// C17: the skip refuses with no words, and from every state but design.
// Each refusal changes no status and no fact.
func TestC17SkipDesignRefusalsChangeNothing(t *testing.T) {
	w := newWorld(t)
	type refusal struct {
		from, request, code, rule string
	}
	cases := []refusal{{from: "design", code: cli.CodeUsage, rule: "missing_request"}}
	for _, from := range []string{"design_review", "design_signoff", "spec_needed", "prd_signoff"} {
		cases = append(cases, refusal{from: from, request: skipWords, code: cli.CodeTransitionNotAllowed, rule: "no_edge"})
	}
	for _, c := range cases {
		ep := w.create(KindEpic, "create_prd", nil)
		w.force(ep, c.from, map[string]string{"prd_merged": "m1", "ui_stories": uiLabels})
		before := w.show(ep)
		r := wantCode(t, w.apply(ep, "skip_design", map[string]string{"request": c.request}), c.code)
		if r.Rule != c.rule {
			t.Errorf("skip from %s: rule %q, want %q", c.from, r.Rule, c.rule)
		}
		after := w.show(ep)
		if after.Status != c.from || after.Seq != before.Seq || after.Fact("design_skipped") != "" || after.Fact("ui_stories") != uiLabels {
			t.Fatalf("a refused skip from %s changed the epic: %+v", c.from, after)
		}
	}
}
