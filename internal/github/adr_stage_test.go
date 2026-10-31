package github

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/machine"
)

// prdEpicIn creates a PRD epic and moves it to status with facts.
func (e *env) prdEpicIn(status string, facts map[string]string) string {
	e.t.Helper()
	id, res := e.m.Create(context.Background(), machine.CreateRequest{Project: testProject, Kind: machine.KindEpic, Title: "Widgets",
		Verb: machine.VerbCreatePRD, Actor: machine.Server("test")})
	mustOK(e.t, res)
	e.put(id, status, facts)
	return id
}

// put moves an item to status with facts, as a writer outside the server.
func (e *env) put(id, status string, facts map[string]string) {
	e.t.Helper()
	set := map[string]string{}
	for k, v := range facts {
		set["loop."+k] = v
	}
	if _, err := e.bx.Update(context.Background(), id, beadsx.UpdateSpec{IfStatus: e.status(id).Status, Status: status, SetMetadata: set}); err != nil {
		e.t.Fatal(err)
	}
}

func adrPath(n int) string { return fmt.Sprintf("docs/adr/%04d-x.md", n) }

// C11: an ADR whose Ticket line names a PRD epic in architecture moves it
// to spec_needed, and records the highest such ADR and the commit that
// added it. That holds for an epic that joined no ADR yet, and for one
// that the operator returned to architecture after it joined ADR 0007.
func TestC11ADRNamingAPRDEpicMovesIt(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	r := e.reactor()
	back := e.prdEpicIn(machine.SpecReview, map[string]string{machine.FactPRDMerged: "m1", machine.FactADR: adrPath(7)})
	e.land(r, map[string]string{adrPath(7): adrText(back)})
	e.put(back, machine.Architecture, nil)
	fresh := e.prdEpicIn(machine.Architecture, map[string]string{machine.FactPRDMerged: "m1"})
	e.land(r, map[string]string{adrPath(8): adrText(fresh), adrPath(9): adrText(back), adrPath(10): adrText(fresh)})
	added := gitT(t, e.checkout, "rev-parse", "HEAD")
	for id, want := range map[string]string{fresh: adrPath(10), back: adrPath(9)} {
		it := e.status(id)
		if it.Status != machine.SpecNeeded || it.Fact(machine.FactADR) != want || it.Fact(machine.FactADRMerged) != added {
			t.Errorf("epic %s: %s with adr=%q adr_merged=%q, want spec_needed with %s and %s", id, it.Status,
				it.Fact(machine.FactADR), it.Fact(machine.FactADRMerged), want, added)
		}
	}
}

// C12: an ADR whose Ticket line names another epic, or none, or that names
// the epic only in its prose, leaves a PRD epic in architecture. The ADR
// the epic already joined does not move it either.
func TestC12ADRNamingAnotherEpicMovesNothing(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	r := e.reactor()
	fresh := e.prdEpicIn(machine.Architecture, map[string]string{machine.FactPRDMerged: "m1"})
	back := e.prdEpicIn(machine.Architecture, map[string]string{machine.FactPRDMerged: "m1", machine.FactADR: adrPath(7)})
	e.land(r, map[string]string{
		adrPath(7):  adrText(back),
		adrPath(8):  adrText("tk-zz"),
		adrPath(9):  adrText("none"),
		adrPath(10): adrText("tk-yy", fresh, back),
	})
	for id, want := range map[string]string{fresh: "", back: adrPath(7)} {
		if it := e.status(id); it.Status != machine.Architecture || it.Fact(machine.FactADR) != want {
			t.Errorf("epic %s: %s with adr=%q, want architecture with adr=%q", id, it.Status, it.Fact(machine.FactADR), want)
		}
	}
}

// C13: an epic in architecture leaves only on an ADR that names it with a
// higher number than the one in its adr fact, and records the highest such
// number. An epic that stays moves on the first tick that sees a higher
// ADR. Each seed draws ADR numbers, Ticket lines and joined numbers.
func TestC13EpicLeavesArchitectureOnlyOnAnADRItHasNotJoined(t *testing.T) {
	for seed := range uint64(6) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, seed))
			e := newEnv(t)
			e.withWorld()
			r := e.reactor()
			epics, joined := make([]string, 4), make([]int, 4)
			for i := range epics {
				facts := map[string]string{machine.FactPRDMerged: "m1"}
				if joined[i] = rng.IntN(16); joined[i] > 0 {
					facts[machine.FactADR] = adrPath(joined[i])
				}
				epics[i] = e.prdEpicIn(machine.Architecture, facts)
			}
			files, highest := map[string]string{}, make([]int, len(epics))
			for _, n := range rng.Perm(20)[:12] {
				n++
				ticket := []string{"none", "tk-zz"}[rng.IntN(2)]
				if k := rng.IntN(len(epics) + 2); k < len(epics) {
					ticket = epics[k]
					if n > joined[k] {
						highest[k] = max(highest[k], n)
					}
				}
				files[adrPath(n)] = adrText(ticket)
			}
			e.land(r, files)
			later := map[string]string{}
			for i, id := range epics {
				it := e.status(id)
				if highest[i] > 0 {
					if it.Status != machine.SpecNeeded || it.Fact(machine.FactADR) != adrPath(highest[i]) {
						t.Fatalf("epic %d joined %d, and the base holds ADR %d for it: %s with adr=%q", i, joined[i], highest[i],
							it.Status, it.Fact(machine.FactADR))
					}
					continue
				}
				want := ""
				if joined[i] > 0 {
					want = adrPath(joined[i])
				}
				if it.Status != machine.Architecture || it.Fact(machine.FactADR) != want {
					t.Fatalf("epic %d joined %q, and no higher ADR names it: %s with adr=%q", i, want, it.Status, it.Fact(machine.FactADR))
				}
				later[id] = adrPath(21 + i)
			}
			for id, path := range later {
				e.land(r, map[string]string{path: adrText(id)})
				if it := e.status(id); it.Status != machine.SpecNeeded || it.Fact(machine.FactADR) != path {
					t.Fatalf("epic %s: the tick that saw %s left it %s with adr=%q", id, path, it.Status, it.Fact(machine.FactADR))
				}
			}
		})
	}
}

// C14: an epic created with --architecture writes on loop/<epic> while it
// waits for its first ADR.
func TestC14ArchitectureEpicKeepsItsBranch(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	id := e.architectureEpic("Move the store to Postgres")
	if got := BranchOf(e.status(id)); got != "loop/"+id {
		t.Fatalf("BranchOf a new architecture epic = %q, want loop/%s", got, id)
	}
}

// C17: a PRD epic in the ADR stage writes on loop/<epic>-adr, and on its
// spec branch once it leaves the stage. An epic in architecture with no
// PRD and no ADR, and every ticket, keep loop/<id>.
func TestC17BranchOfFollowsTheADRStage(t *testing.T) {
	for _, c := range []struct {
		kind   machine.Kind
		status string
		facts  map[string]string
		want   string
	}{
		{machine.KindEpic, machine.Architecture, map[string]string{machine.FactPRDMerged: "m1"}, "loop/tk-1-adr"},
		{machine.KindEpic, machine.Architecture, map[string]string{machine.FactADR: adrPath(7)}, "loop/tk-1-adr"},
		{machine.KindEpic, machine.Architecture, nil, "loop/tk-1"},
		{machine.KindEpic, machine.SpecNeeded, map[string]string{machine.FactPRDMerged: "m1"}, "loop/tk-1-spec"},
		{machine.KindEpic, machine.SpecReview, map[string]string{machine.FactADR: adrPath(7)}, "loop/tk-1-spec"},
		{machine.KindEpic, machine.PRDReview, nil, "loop/tk-1"},
		{machine.KindTicket, machine.InProgress, nil, "loop/tk-1"},
	} {
		it := machine.Item{ID: "tk-1", Kind: c.kind, Status: c.status, Facts: c.facts}
		if got := BranchOf(it); got != c.want {
			t.Errorf("BranchOf(%s in %s with %v) = %q, want %q", c.kind, c.status, c.facts, got, c.want)
		}
	}
}
