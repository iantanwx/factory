package overwatch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// starvedWorld is the stalls world with beads edges, and the server's
// admission over its store and items. Its one open pull request is #41 of
// factorio-c, a canceled ticket.
type starvedWorld struct {
	*stallsWorld
	deps map[string][]beadsx.Dependency // item -> its edges
	adm  *admission.Admission
}

func newStarvedWorld(t *testing.T) *starvedWorld {
	t.Helper()
	w := &starvedWorld{stallsWorld: newStallsWorld(t), deps: map[string][]beadsx.Dependency{}}
	adm, err := admission.New(admission.Config{
		Store: w.st,
		Host:  hostOf{w.stallsWorld},
		Beads: func(string) (beadsx.Client, error) { return bodiesOf{w: w.fakeWorld}, nil },
		Base:  noBase{},
		Pulls: openPulls{{Number: 41, Branch: "loop/factorio-c"}},
		Project: func(ctx context.Context, project string) (*config.ProjectSnapshot, error) {
			return w.ProjectConfig(ctx, project)
		},
		Release: "test",
		Now:     func() time.Time { return w.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	adm.Attach(itemsOf{w.fakeWorld})
	w.adm = adm
	return w
}

func (w *starvedWorld) Dependencies(_ context.Context, _, item string) ([]beadsx.Dependency, error) {
	return w.deps[item], nil
}
func (w *starvedWorld) WorkQuery() WorkQuery { return w.adm }

// ticket puts one ticket of epic in status, with body.
func (w *starvedWorld) ticket(id, epic, status, body string) {
	w.add("factorio", machine.Item{ID: id, Kind: machine.KindTicket, Title: "ticket " + id, Status: status, Updated: at(8, 0),
		Facts: map[string]string{machine.FactEpic: epic}}, body)
}

// lease gives item's lease to a live run of stage.
func (w *starvedWorld) lease(t *testing.T, item, run, stage string) {
	t.Helper()
	w.run(t, run, stage, item, at(9, 3), time.Time{})
	w.exec(t, `INSERT INTO leases (project, item, epoch, owner_run_id, granted_at) VALUES ('factorio', ?, 1, ?, ?)`,
		item, run, at(9, 3).UnixMilli())
}

// hostOf serves the stalls world's host config to admission.
type hostOf struct{ w *stallsWorld }

func (h hostOf) Load(ctx context.Context) (*config.HostConfig, error) { return h.w.Host(ctx) }

// bodiesOf serves each item's body to admission as its bead's description.
type bodiesOf struct {
	beadsx.Client
	w *fakeWorld
}

func (b bodiesOf) Get(_ context.Context, id string) (beadsx.Issue, error) {
	for _, it := range b.w.items["factorio"] {
		if it.ID == id {
			return beadsx.Issue{ID: id, Title: it.Title, Description: b.w.bodies["factorio"][id], Status: it.Status}, nil
		}
	}
	return beadsx.Issue{}, beadsx.ErrNotFound
}

// noBase is a base branch that holds no file.
type noBase struct{}

func (noBase) Files(context.Context, string, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

type openPulls []admission.PullHead

func (p openPulls) OpenPulls(context.Context, string) ([]admission.PullHead, error) { return p, nil }

// itemsOf serves the fake world's items to admission, which claims none.
type itemsOf struct{ w *fakeWorld }

func (i itemsOf) Show(ctx context.Context, project, item string) (machine.Item, *cli.Refusal) {
	items, _ := i.w.Items(ctx, project, machine.ListFilter{})
	for _, it := range items {
		if it.ID == item {
			return it, nil
		}
	}
	return machine.Item{}, &cli.Refusal{Code: cli.CodeNotFound, Item: item}
}
func (i itemsOf) List(ctx context.Context, project string, f machine.ListFilter) ([]machine.Item, *cli.Refusal) {
	items, err := i.w.Items(ctx, project, f)
	if err != nil {
		return nil, cli.InternalRefusal(err)
	}
	return items, nil
}
func (itemsOf) Claim(context.Context, machine.ClaimRequest) (machine.Lease, cli.Result) {
	return machine.Lease{}, cli.InternalRefusal(fmt.Errorf("the starved part claims nothing")).Result()
}

// dependsOnCanceled is the body of a ticket that depends on #41, the open
// pull request of canceled ticket factorio-c.
const dependsOnCanceled = "Build the thing.\n\n## Depends on\n\n- #41\n"

const starvedKey = "overwatch:starved:factorio:factorio-e1"

// starvedFixture is epic factorio-e1 in execution, whose one open ticket
// factorio-t1 waits in todo on canceled ticket factorio-c, and a done
// ticket of the epic.
func starvedFixture(t *testing.T) *starvedWorld {
	t.Helper()
	w := newStarvedWorld(t)
	w.add("factorio", machine.Item{ID: "factorio-e1", Kind: machine.KindEpic, Title: "Checkout flow", Status: machine.Execution, Updated: at(7, 0)}, "")
	w.ticket("factorio-c", "factorio-e1", machine.Canceled, "")
	w.ticket("factorio-t0", "factorio-e1", machine.Done, "")
	w.ticket("factorio-t1", "factorio-e1", machine.Todo, dependsOnCanceled)
	w.deps["factorio-t1"] = []beadsx.Dependency{{ID: "factorio-c", Kind: beadsx.DepBlocks, Status: machine.Canceled}}
	return w
}

func readStarvedPart(t *testing.T, w World) Reading {
	t.Helper()
	i := slices.IndexFunc(Parts(), func(p Part) bool { return p.Name == "starved" })
	if i < 0 {
		t.Fatal("Parts() holds no starved part")
	}
	r, err := Parts()[i].Read(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// An epic in execution whose one todo ticket waits on a canceled ticket,
// while the implementer is idle, rings: the line names the epic, its title,
// and the ticket with its state and what holds it.
func TestStarvedRingsForAnEpicWhoseOpenTicketsNothingMoves(t *testing.T) {
	w := starvedFixture(t)
	r := readStarvedPart(t, w)
	if len(r.Findings) != 1 {
		t.Fatalf("findings = %+v, notes = %q; want one finding", r.Findings, r.Notes)
	}
	f := r.Findings[0]
	if f.Key != starvedKey || f.Project != "factorio" {
		t.Fatalf("key = %q of %q, want %q of factorio", f.Key, f.Project, starvedKey)
	}
	for _, want := range []string{"factorio-e1", `"Checkout flow"`, "factorio-t1 todo", "factorio-c canceled", "(" + starvedKey + ")"} {
		if !strings.Contains(f.Line, want) {
			t.Errorf("line %q does not name %q", f.Line, want)
		}
	}
	if strings.Contains(f.Line, "factorio-t0") {
		t.Errorf("line %q names factorio-t0, which is done", f.Line)
	}
}

// The same epic with a live implementer run that holds its ticket's lease
// moves, so no finding.
func TestStarvedHoldsNoFindingWhileALiveRunHoldsTheTicketsLease(t *testing.T) {
	w := starvedFixture(t)
	w.items["factorio"] = slices.DeleteFunc(w.items["factorio"], func(it machine.Item) bool { return it.ID == "factorio-t1" })
	w.ticket("factorio-t1", "factorio-e1", machine.InProgress, dependsOnCanceled)
	w.lease(t, "factorio-t1", "r-impl1", config.StageImplementer)
	r := readStarvedPart(t, w)
	if len(r.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", r.Findings)
	}
	if !slices.ContainsFunc(r.Notes, func(n string) bool { return strings.Contains(n, "run r-impl1 of implementer holds its lease") }) {
		t.Fatalf("notes = %q, want one that names the run on factorio-t1", r.Notes)
	}
}

// A backlog ticket of the epic that waits on a ticket of no epic, which a
// live run holds, moves too.
func TestStarvedHoldsNoFindingWhileATicketWaitsOnADependencyThatMoves(t *testing.T) {
	w := starvedFixture(t)
	w.ticket("factorio-o", "", machine.InProgress, "")
	w.lease(t, "factorio-o", "r-impl2", config.StageImplementer)
	w.ticket("factorio-t3", "factorio-e1", machine.Backlog, "")
	w.deps["factorio-t3"] = []beadsx.Dependency{{ID: "factorio-o", Kind: beadsx.DepBlocks, Status: machine.InProgress}}
	w.items["factorio"] = slices.DeleteFunc(w.items["factorio"], func(it machine.Item) bool { return it.ID == "factorio-t1" })
	r := readStarvedPart(t, w)
	if len(r.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", r.Findings)
	}
	if !slices.ContainsFunc(r.Notes, func(n string) bool { return strings.Contains(n, "factorio-o") }) {
		t.Fatalf("notes = %q, want one that names factorio-o, which factorio-t3 waits on", r.Notes)
	}
}

// No epic in execution: no finding, though the ticket waits as before.
func TestStarvedHoldsNoFindingWithNoEpicInExecution(t *testing.T) {
	w := starvedFixture(t)
	for i, it := range w.items["factorio"] {
		if it.ID == "factorio-e1" {
			w.items["factorio"][i].Status = machine.SpecReview
		}
	}
	r := readStarvedPart(t, w)
	if len(r.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", r.Findings)
	}
}

// An implementer at its full cap is busy, not starved: no finding.
func TestStarvedHoldsNoFindingWhileTheImplementerHoldsItsCap(t *testing.T) {
	w := starvedFixture(t)
	for i := range w.host.ThroughputFor(config.StageImplementer).Cap {
		w.run(t, fmt.Sprintf("r-busy%d", i), config.StageImplementer, fmt.Sprintf("factorio-x%d", i), at(9, 3), time.Time{})
	}
	r := readStarvedPart(t, w)
	if len(r.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", r.Findings)
	}
	if !slices.ContainsFunc(r.Notes, func(n string) bool { return strings.Contains(n, "fill the cap") }) {
		t.Fatalf("notes = %q, want one that says the implementer holds its cap", r.Notes)
	}
}

// filesFixture is the starved fixture whose todo ticket factorio-t1 holds
// body and waits on no ticket, and ticket factorio-o of no epic, whose
// "May change" list is `internal/admission/**` and whose lease live
// implementer run r-impl2 holds.
func filesFixture(t *testing.T, body string) *starvedWorld {
	t.Helper()
	w := starvedFixture(t)
	w.items["factorio"] = slices.DeleteFunc(w.items["factorio"], func(it machine.Item) bool { return it.ID == "factorio-t1" })
	delete(w.deps, "factorio-t1")
	w.ticket("factorio-t1", "factorio-e1", machine.Todo, body)
	w.ticket("factorio-o", "", machine.InProgress, "Build it.\n\n## Files\n\n**May change:**\n- `internal/admission/**`\n")
	w.lease(t, "factorio-o", "r-impl2", config.StageImplementer)
	return w
}

// factorio-29yx: a todo ticket that admission refuses with rule
// files_in_flight waits on a live run, and moves once that run settles. So
// its epic does not starve, and the note names the other ticket and its run.
func TestStarvedHoldsNoFindingWhileATicketWaitsOnFilesInFlight(t *testing.T) {
	w := filesFixture(t, "Build the thing.\n\n## Files\n\n**May change:**\n- `internal/admission/policy.go`\n")
	r := readStarvedPart(t, w)
	if len(r.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", r.Findings)
	}
	if !slices.ContainsFunc(r.Notes, func(n string) bool {
		return strings.Contains(n, "factorio-t1 todo moves") && strings.Contains(n, "factorio-o") && strings.Contains(n, "r-impl2")
	}) {
		t.Fatalf("notes = %q, want one that names factorio-o and run r-impl2, whose files factorio-t1 waits on", r.Notes)
	}
}

// A ticket that a dependency holds too does not move when the run on its
// files settles, so its epic still starves.
func TestStarvedRingsWhileATicketWaitsOnFilesInFlightAndADependency(t *testing.T) {
	w := filesFixture(t, dependsOnCanceled+"\n## Files\n\n**May change:**\n- `internal/admission/policy.go`\n")
	r := readStarvedPart(t, w)
	if len(r.Findings) != 1 || r.Findings[0].Key != starvedKey {
		t.Fatalf("findings = %+v, notes = %q; want the one finding %s", r.Findings, r.Notes, starvedKey)
	}
}
