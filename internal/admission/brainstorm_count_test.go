package admission

import (
	"context"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// C7: the brainstorm count. Over one epic in each epic state, only the states
// an agent moves on hold a slot: prd_needed, prd_drafting, prd_review,
// prd_amending, spec_needed, spec_drafting and spec_review. backlog,
// architecture and adr_review, the design states, sign-off, escalated,
// canceled and every state past spec review hold none.
func TestBrainstormCountHoldsOnlyTheStatesAnAgentMovesOn(t *testing.T) {
	states := []string{"backlog", "prd_needed", "prd_drafting", "prd_review", "prd_amending", "prd_signoff", "architecture",
		"adr_review", "design", "design_review", "design_signoff", "spec_needed", "spec_drafting", "spec_review", "spec_signoff", "spec_amending", "spec_approved",
		"decomposing", "execution", "escalated", "done", "canceled"}
	tbl, _ := machine.TableFor(machine.KindEpic)
	var table []string
	for _, s := range tbl.States {
		table = append(table, s.Name)
	}
	if !slices.Equal(slices.Sorted(slices.Values(table)), slices.Sorted(slices.Values(states))) {
		t.Fatalf("the epic states are %v, the test covers %v", table, states)
	}
	w := newWorld(t)
	for _, s := range states {
		w.put(machine.KindEpic, s, "epic")
	}
	ctx := context.Background()
	w.setCaps(map[string]int{config.StageBrainstorm: 7})
	wantRefusal(t, w.a.BrainstormCap(ctx, prj), cli.CodeCapFull)
	w.setCaps(map[string]int{config.StageBrainstorm: 8})
	if ref := w.a.BrainstormCap(ctx, prj); ref != nil {
		t.Fatalf("7 counted epics under a cap of 8: %+v", ref)
	}
}
