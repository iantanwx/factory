//go:build acceptance

package scenarios

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// The stages, rooms and the coordinator the server starts an agent for.
// Every scenario's dispatch and room open checks the runbook its brief
// names (acceptance.World.CheckRunbook). This scenario starts the kinds
// no other scenario reaches, and checks that intake starts no unattended
// run.
func TestStageKindsTheOtherScenariosMiss(t *testing.T) {
	w := newWorld(t, acceptance.Options{Config: acceptance.DefaultConfig + "  intake: {}\n"})

	// Intake runs in a room, and the pack has no unattended intake runbook.
	if st, ref := w.TryDispatch("intake"); st != nil || ref == nil || ref.Code != cli.CodeUsage {
		t.Fatalf("an intake dispatch = %+v, %+v", st, ref)
	}

	// The reproducer takes a raw bug report. No CLI verb files one in v1,
	// so the report is filed through the machine, as a tracker adapter
	// would.
	bug, res := w.Srv.Machine.Create(context.Background(), machine.CreateRequest{Project: acceptance.Project,
		Kind: machine.KindBug, Title: "The list page shows no widgets", Verb: "file", Actor: machine.Server("acceptance")})
	if res.Refusal != nil {
		t.Fatalf("file a bug: %+v", res.Refusal)
	}
	run := w.Dispatch(machine.StageReproducer)
	if run.Item != bug {
		t.Fatalf("the reproducer took %s, want %s", run.Item, bug)
	}
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))

	// A decision room serves a filed decision.
	d := w.Must(w.Coordinator("decision", "file", "--project", acceptance.Project, "--title", "Which colour do widgets take?",
		"--trigger", "the first widget ships"))
	openRoom(w, "decision", d.Str("decision"), "the operator wants to settle the colour")
}

// checkStages fails the run unless every stage, room and the coordinator
// started at least once. It runs after a full run only.
func checkStages() []string {
	want := []string{"coordinator", "implementer", "code-reviewer", "doc-reviewer", "amender", "spec-author",
		"decomposer", "fixer", "reproducer", "intake-room", "decision-room", "review-room"}
	got := acceptance.Started()
	var missing []string
	for _, s := range want {
		if got[s] == 0 {
			missing = append(missing, s)
		}
	}
	return missing
}
