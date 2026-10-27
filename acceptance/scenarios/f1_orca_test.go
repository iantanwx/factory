//go:build acceptance && orca

package scenarios

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/agenthosttest"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// The waits of F1 on real orca.
const (
	f1Poll = 15 * time.Second
	f1Wait = 30 * time.Minute
)

// F1 on real orca: a real implementer and a real code-reviewer take a
// ticket from claim to merge, in 2 sessions and with no coordinator.
func TestF1RealAgentsTakeATicketFromClaimToMerge(t *testing.T) {
	rh, err := acceptance.OrcaHost(agenthosttest.Declared(t, "TestF1RealAgentsTakeATicketFromClaimToMerge"))
	if err != nil {
		t.Fatal(err)
	}
	q, ok := rh.Agents.(agenthost.QuotaReader)
	if !ok {
		t.Fatal("the orca host reads no quota")
	}
	starts := &kinds{Host: rh.Agents, QuotaReader: q}
	rh.Agents = starts
	w := newWorld(t, acceptance.Options{Config: f1Config, Files: map[string]string{"check.sh": f1Check}, Real: rh})
	id := w.CreateTicket("Add a greeting",
		"Create `greeting.txt` holding the one line `hello, world`, so that `sh check.sh` passes.")

	run := w.Dispatch(machine.StageImplementer)
	if run.Item != id {
		t.Fatalf("the implementer took %s, want %s", run.Item, id)
	}
	waitFor(w, id, machine.InReview, machine.InProgress)
	head := w.Show(id).Fact(machine.FactHead)
	if head == "" || w.Git.RemoteSHA("loop/"+id) != head {
		t.Fatalf("the implementer's head %q is not the remote branch's %q", head, w.Git.RemoteSHA("loop/"+id))
	}

	w.CI(head, "success")
	w.React()
	if rev := w.Dispatch(machine.StageCodeReviewer); rev.Item != id {
		t.Fatalf("the code-reviewer took %s, want %s", rev.Item, id)
	}
	waitFor(w, id, machine.ReadyToMerge, machine.InReview)
	if got := w.Show(id).Fact(machine.FactReviewedHead); got != head {
		t.Fatalf("the code-reviewer passed %s, want the implementer's head %s", got, head)
	}

	w.React()
	if !slices.Contains(w.API.World.Posted(), head+" "+gt.Eligible+" success") {
		t.Fatalf("no eligibility at the reviewed head: %v", w.API.World.Posted())
	}
	w.HandOff()
	w.HandOff()
	w.React()
	wantStatus(w, id, machine.Done)
	if merges := w.API.World.Merges(); len(merges) != 1 || merges[0].SHA != head || merges[0].Status != 200 {
		t.Fatalf("merges = %+v, want one at the reviewed head %s", merges, head)
	}
	if got := starts.started(); !slices.Equal(got, []agenthost.Kind{agenthost.KindStage, agenthost.KindStage}) {
		t.Fatalf("the world started sessions of kinds %v, want 2 stage sessions and no coordinator", got)
	}
}

// waitFor ticks the scheduler each f1Poll until item reaches want, for up
// to f1Wait. A status other than want or from fails at once, because a
// reject or a red build would need a session that F1 does not declare.
func waitFor(w *acceptance.World, item, want, from string) {
	w.T.Helper()
	for end := time.Now().Add(f1Wait); time.Now().Before(end); time.Sleep(f1Poll) {
		w.Tick()
		switch got := w.Status(item); got {
		case want:
			return
		case from:
		default:
			w.T.Fatalf("%s is %s, want %s", item, got, want)
		}
	}
	w.T.Fatalf("%s did not reach %s in %s: it is %s", item, want, f1Wait, w.Status(item))
}

// kinds records the kind of each session start that reaches the host,
// whether the host accepts it or not.
type kinds struct {
	agenthost.Host
	agenthost.QuotaReader

	mu    sync.Mutex
	kinds []agenthost.Kind
}

func (k *kinds) Start(ctx context.Context, spec agenthost.StartSpec) (agenthost.Session, error) {
	k.mu.Lock()
	k.kinds = append(k.kinds, spec.Kind)
	k.mu.Unlock()
	return k.Host.Start(ctx, spec)
}

func (k *kinds) started() []agenthost.Kind {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.kinds)
}
