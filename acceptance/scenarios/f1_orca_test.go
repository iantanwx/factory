//go:build acceptance && orca

package scenarios

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/agenthosttest"
)

// F1 on real orca: a real implementer, simplify run and code-reviewer take
// a ticket from claim to merge, in 3 sessions and with no coordinator.
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
	id := w.CreateTicket(f1Title, f1Body)

	f1Path(w, id, f1Wait, func(acceptance.Run) {})
	if got := starts.started(); !slices.Equal(got, []agenthost.Kind{agenthost.KindStage, agenthost.KindStage, agenthost.KindStage}) {
		t.Fatalf("the world started sessions of kinds %v, want 3 stage sessions and no coordinator", got)
	}
}

// waitFor waits for item to reach want on real orca, for up to f1Wait.
func waitFor(w *acceptance.World, item, want, from string) {
	w.T.Helper()
	waitWithin(w, item, want, from, f1Wait)
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
