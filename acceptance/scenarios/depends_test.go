//go:build acceptance

package scenarios

import (
	"context"
	"fmt"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// The composed server holds a ticket whose `## Depends on` names a pull
// request that is open on GitHub: the scheduler starts no run of it, and
// admission refuses it with DEPENDENCY_OPEN. Once the pull request closes,
// the implementer takes the ticket.
func TestDependsOnAnOpenPullRequestHoldsTheTicketUntilItCloses(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	dep := w.API.World.Add(&gt.PR{Head: "feat/ledger"})
	id := createTicket(w, "Ticket P", fmt.Sprintf("Create `p.txt` holding the line `p`.\n\n## Depends on\n\n- #%d\n", dep.Number))

	if st, ref := w.TryDispatch(machine.StageImplementer); st != nil || ref != nil {
		t.Fatalf("dispatch with #%d open = %+v, %+v; want no run", dep.Number, st, ref)
	}
	_, ref := w.Srv.Admission.Admit(context.Background(), admission.Request{
		Project: acceptance.Project, Stage: machine.StageImplementer, Item: id, RunID: "probe"})
	if ref == nil || ref.Code != cli.CodeDependencyOpen {
		t.Fatalf("admit with #%d open = %+v, want %s", dep.Number, ref, cli.CodeDependencyOpen)
	}
	wantStatus(w, id, machine.Todo)

	w.ClosePR(dep)
	run := w.Dispatch(machine.StageImplementer)
	if run.Item != id {
		t.Fatalf("the implementer took %s after #%d closed, want %s", run.Item, dep.Number, id)
	}
}
