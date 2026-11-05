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
	"github.com/iantanwx/loop/internal/github"
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

// C-bug-2: a canceled blocker is no wait. factorio-t1's line names the open
// pull request of canceled factorio-c as what holds it, and never says that
// factorio-t1 waits on factorio-c.
func TestStarvedNamesNoCanceledBlockerAsAWait(t *testing.T) {
	r := readStarvedPart(t, starvedFixture(t))
	if len(r.Findings) != 1 {
		t.Fatalf("findings = %+v, notes = %q; want one finding", r.Findings, r.Notes)
	}
	line := r.Findings[0].Line
	if strings.Contains(line, "waits on factorio-c") {
		t.Errorf("line %q says factorio-t1 waits on factorio-c, which is canceled", line)
	}
	if !strings.Contains(line, "factorio-c canceled holds it while its pull request stays open") {
		t.Errorf("line %q does not name the pull request of canceled factorio-c as what holds factorio-t1", line)
	}
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

// ciStarvedWorld is the starved world with GitHub reads.
type ciStarvedWorld struct {
	*starvedWorld
	gh *ciReads
}

func (w *ciStarvedWorld) GitHub() GitHubReads { return w.gh }

// ciFixture is epic factorio-e1, whose one open root factorio-h is
// in_review. Its simplify run left head aa2a0097 over simplify_from
// 79ba1763, and CI run 42015677576 at aa2a0097 started at started in
// status with conclusion. Admission reads CI there as pending, so it
// refuses the code review with rule simplify_head_ci. The one blocker of
// factorio-h, factorio-3ey7, is done. Backlog ticket factorio-t2 waits on
// factorio-h. The CI budget is 10 minutes, and the clock reads 09:05.
func ciFixture(t *testing.T, status, conclusion string, started time.Time) *ciStarvedWorld {
	t.Helper()
	sw := newStarvedWorld(t)
	sw.configs["factorio"].Health.CIBudget = 10 * time.Minute
	sw.add("factorio", machine.Item{ID: "factorio-e1", Kind: machine.KindEpic, Title: "Checkout flow", Status: machine.Execution, Updated: at(7, 0)}, "")
	sw.ticket("factorio-3ey7", "factorio-e1", machine.Done, "")
	sw.add("factorio", machine.Item{ID: "factorio-h", Kind: machine.KindTicket, Title: "ticket factorio-h", Status: machine.InReview, Updated: at(9, 3),
		Facts: map[string]string{machine.FactEpic: "factorio-e1", machine.FactSimplifyFrom: "79ba1763"}}, "")
	sw.entered(t, "factorio-h", machine.InProgress, machine.InReview, at(9, 3))
	sw.deps["factorio-h"] = []beadsx.Dependency{{ID: "factorio-3ey7", Kind: beadsx.DepBlocks, Status: machine.Done}}
	sw.ticket("factorio-t2", "factorio-e1", machine.Backlog, "")
	sw.deps["factorio-t2"] = []beadsx.Dependency{{ID: "factorio-h", Kind: beadsx.DepBlocks, Status: machine.InReview}}
	sw.adm.SetPRHeadReader(func(context.Context, string, string) (string, *cli.Refusal) { return "aa2a0097", nil })
	sw.adm.SetCIReader(func(context.Context, string, string) (admission.CIState, error) { return admission.CIPending, nil })
	pr := github.PR{Number: 372, State: "open"}
	pr.Head.Ref, pr.Head.SHA = github.BranchFor("factorio-h"), "aa2a0097"
	return &ciStarvedWorld{starvedWorld: sw, gh: &ciReads{
		pulls: []github.PR{pr},
		runs: []github.RunView{{ID: 42015677576, Workflow: "ci.yml", Event: "pull_request", Branch: pr.Head.Ref, HeadSHA: "aa2a0097",
			Attempt: 1, Status: status, Conclusion: conclusion, CreatedAt: started, StartedAt: started, UpdatedAt: at(9, 4)}},
	}}
}

// C-bug-1 (factorio-aszl): CI at the simplify head of the open root
// factorio-h started at 09:04, inside the CI budget. The root moves when
// CI turns green, so the epic does not starve, and a note names the root
// and the CI run.
func TestStarvedHoldsNoFindingWhileTheOpenRootWaitsOnCIInProgress(t *testing.T) {
	w := ciFixture(t, "in_progress", "", at(9, 4))
	r := readStarvedPart(t, w)
	for _, f := range r.Findings {
		if f.Key == starvedKey {
			t.Errorf("finding %q: the open root factorio-h waits on CI run 42015677576 in progress, so the epic does not starve", f.Line)
			if strings.Contains(f.Line, "waits on factorio-3ey7") {
				t.Errorf("finding %q names done ticket factorio-3ey7 as what factorio-h waits on, not the CI wait at aa2a0097", f.Line)
			}
		}
	}
	if !slices.ContainsFunc(r.Notes, func(n string) bool {
		return strings.Contains(n, "factorio-h in_review moves") && strings.Contains(n, "simplify_head_ci") && strings.Contains(n, "42015677576")
	}) {
		t.Fatalf("notes = %q, want one that names factorio-h and CI run 42015677576", r.Notes)
	}
}

// C-bug-1: CI at the simplify head that is red or runs past the budget
// proves no wait, so the epic still starves. C-bug-2: the line never says
// that factorio-h waits on its done blocker factorio-3ey7.
func TestStarvedRingsWhileCIAtTheSimplifyHeadIsRedOrPastBudget(t *testing.T) {
	cases := []struct {
		name               string
		status, conclusion string
		started            time.Time
	}{
		{"CI is red", "completed", "failure", at(9, 4)},
		{"CI runs past the budget", "in_progress", "", at(8, 50)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := readStarvedPart(t, ciFixture(t, c.status, c.conclusion, c.started))
			if len(r.Findings) != 1 || r.Findings[0].Key != starvedKey {
				t.Fatalf("findings = %+v, notes = %q; want the one finding %s", r.Findings, r.Notes, starvedKey)
			}
			line := r.Findings[0].Line
			if !strings.Contains(line, "factorio-h in_review (code-reviewer does not take it now") {
				t.Errorf("line %q does not say that the code-reviewer does not take factorio-h", line)
			}
			if strings.Contains(line, "waits on factorio-3ey7") {
				t.Errorf("line %q says factorio-h waits on factorio-3ey7, which is done", line)
			}
		})
	}
}
