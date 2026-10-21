package admission

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// With 8 implementer runs live, a ninth is refused. Repair work uses the
// same implementer capacity (parity: "repairs use the same implementer
// capacity as new tasks", admission.test.ts, alpha).
func TestImplementerCapAndRepairShareCapacity(t *testing.T) {
	w := newWorld(t)
	for range 8 {
		w.mustAdmit(machine.StageImplementer, w.ticket(machine.Todo))
	}
	_, ref := w.admit(machine.StageImplementer, w.ticket(machine.Todo))
	wantRefusal(t, ref, cli.CodeCapFull)
	_, ref = w.admit(machine.StageImplementer, w.ticket(machine.Repair))
	wantRefusal(t, ref, cli.CodeCapFull)
	if n := w.count(`SELECT count(*) FROM runs WHERE ended_at IS NULL`); n != 8 {
		t.Fatalf("live runs = %d, want 8: a refusal reserved a slot", n)
	}
}

// Code review has its own cap of 8, and every reviewer stage shares the
// reviewers pool of 10.
func TestReviewerCaps(t *testing.T) {
	w := newWorld(t)
	for range 8 {
		w.mustAdmit(machine.StageCodeReviewer, w.ticket(machine.InReview))
	}
	_, ref := w.admit(machine.StageCodeReviewer, w.ticket(machine.InReview))
	wantRefusal(t, ref, cli.CodeCapFull)
	for range 2 {
		w.mustAdmit(machine.StageDocReviewer, w.put(machine.KindEpic, machine.PRDReview, "prd"))
	}
	_, ref = w.admit(machine.StageDocReviewer, w.put(machine.KindEpic, machine.SpecReview, "spec"))
	wantRefusal(t, ref, cli.CodeCapFull)
}

// Intake counts epics in counted document states, not runs. An epic that
// waits on a person holds no slot.
func TestIntakeCapCountsEpicsInCountedStates(t *testing.T) {
	w := newWorld(t)
	counted := []string{machine.PRDReview, machine.PRDAmending, machine.SpecNeeded, machine.SpecDrafting, machine.SpecReview}
	for i := range 8 {
		w.put(machine.KindEpic, counted[i%len(counted)], "epic")
	}
	w.put(machine.KindEpic, machine.PRDSignoff, "waits on a person")
	w.put(machine.KindEpic, machine.Escalated, "waits on a person")
	_, ref := w.admit(stageIntake, "")
	wantRefusal(t, ref, cli.CodeCapFull)
	if c, r := w.a.Peek(context.Background(), prj, stageIntake); c != nil || r != nil {
		t.Fatalf("peek at the intake cap = %+v %+v, want nothing", c, r)
	}
	w.setCaps(map[string]int{stageIntake: 9})
	w.mustAdmit(stageIntake, "")
}

// The intake mutex has one holder at a time, also under a race. Settling
// the holder frees it.
func TestIntakeMutexHasOneHolder(t *testing.T) {
	w := newWorld(t)
	results := make([]*cli.Refusal, 6)
	admitted := make([]Admitted, 6)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { admitted[i], results[i] = w.admit(stageIntake, "") })
	}
	wg.Wait()
	var holder Admitted
	ok := 0
	for i, r := range results {
		if r == nil {
			ok++
			holder = admitted[i]
			continue
		}
		wantRefusal(t, r, cli.CodeMutexHeld)
	}
	if ok != 1 {
		t.Fatalf("%d intake runs admitted at once, want 1", ok)
	}
	w.settle(holder, Settlement{Finish: "done"})
	w.mustAdmit(stageIntake, "")
}

// Caps come from host config only, and a changed cap applies on the next
// load with no restart.
func TestCapFromHostConfigAppliesWithNoRestart(t *testing.T) {
	w := newWorld(t)
	w.setCaps(map[string]int{machine.StageImplementer: 2})
	w.mustAdmit(machine.StageImplementer, w.ticket(machine.Todo))
	w.mustAdmit(machine.StageImplementer, w.ticket(machine.Todo))
	third := w.ticket(machine.Todo)
	_, ref := w.admit(machine.StageImplementer, third)
	wantRefusal(t, ref, cli.CodeCapFull)
	w.setCaps(map[string]int{machine.StageImplementer: 3})
	w.mustAdmit(machine.StageImplementer, third)
}

// An invalid host config keeps the last good caps in force.
func TestInvalidHostConfigKeepsTheLastGoodCaps(t *testing.T) {
	w := newWorld(t)
	w.setCaps(map[string]int{machine.StageImplementer: 1})
	w.mustAdmit(machine.StageImplementer, w.ticket(machine.Todo))
	w.setCaps(map[string]int{machine.StageImplementer: 0})
	_, ref := w.admit(machine.StageImplementer, w.ticket(machine.Todo))
	wantRefusal(t, ref, cli.CodeCapFull)
}

// Concurrent admissions never pass the cap.
func TestConcurrentAdmissionsKeepTheCap(t *testing.T) {
	w := newWorld(t)
	w.setCaps(map[string]int{machine.StageImplementer: 3})
	items := make([]string, 10)
	for i := range items {
		items[i] = w.ticket(machine.Todo)
	}
	var wg sync.WaitGroup
	refs := make([]*cli.Refusal, len(items))
	for i, it := range items {
		wg.Go(func() { _, refs[i] = w.admit(machine.StageImplementer, it) })
	}
	wg.Wait()
	ok := 0
	for _, r := range refs {
		if r == nil {
			ok++
		} else {
			wantRefusal(t, r, cli.CodeCapFull)
		}
	}
	if ok != 3 {
		t.Fatalf("%d admitted, want 3", ok)
	}
}

// Parity: "an assigned ticket with unknown historical execution admits
// one current owner atomically" (admission.test.ts, alpha).
func TestOneTicketAdmitsOneOwner(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	var wg sync.WaitGroup
	refs := make([]*cli.Refusal, 4)
	for i := range refs {
		wg.Go(func() { _, refs[i] = w.admit(machine.StageImplementer, id) })
	}
	wg.Wait()
	ok := 0
	for _, r := range refs {
		if r == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d runs hold %s, want 1: %+v", ok, id, refs)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE item = ? AND ended_at IS NULL`, id); n != 1 {
		t.Fatalf("%d live runs on %s, want 1: a refused claim kept its reservation", n, id)
	}
	if n := w.count(`SELECT count(*) FROM failures WHERE item = ?`, id); n != 1 {
		t.Fatalf("%d attempt rows on %s, want 1", n, id)
	}
}

// Two counted failures with the same inputs refuse a third attempt. The
// failure records stay when changed inputs admit a fresh attempt (parity:
// "two failed current attempts stop; changed inputs permit a fresh attempt
// without resetting history", admission.test.ts, alpha).
func TestRetryLimitAndInputChanges(t *testing.T) {
	cases := map[string]struct {
		change func(w *world, id string)
		admits bool
	}{
		"nothing changed":                  {func(*world, string) {}, false},
		"an unrelated merge":               {func(w *world, _ string) { w.base.set("unrelated.txt", "blob9") }, false},
		"a new file outside the paths":     {func(w *world, _ string) { w.base.set("apps/other.go", "blob9") }, false},
		"a model and harness change":       {func(*world, string) {}, false},
		"a merge that changes a file":      {func(w *world, _ string) { w.base.set("app/task.go", "blob9") }, true},
		"a merge that changes a cited doc": {func(w *world, _ string) { w.base.set("docs/plans/thing.md", "blob9") }, true},
		"an edit to the ticket":            {func(w *world, id string) { w.beads.edit(id, declaredBody+"\nChanged contract.") }, true},
		"a new release":                    {func(w *world, _ string) { w.build("v2") }, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			id := w.ticket(machine.Todo)
			for i := range 2 {
				ad := w.mustAdmit(machine.StageImplementer, id)
				if ad.Attempt != i+1 {
					t.Fatalf("attempt = %d, want %d", ad.Attempt, i+1)
				}
				if out := w.settle(ad, Settlement{Finish: "failed"}); !out.Counted {
					t.Fatalf("a failed finish did not count: %+v", out)
				}
			}
			_, ref := w.admit(machine.StageImplementer, id)
			wantRefusal(t, ref, cli.CodeRetryLimit)
			c.change(w, id)
			var ad Admitted
			if name == "a model and harness change" {
				ad, ref = w.a.Admit(context.Background(), Request{Project: prj, Stage: machine.StageImplementer, Item: id,
					RunID: w.nextRun(), Harness: "codex", Model: "gpt"})
			} else {
				ad, ref = w.admit(machine.StageImplementer, id)
			}
			if !c.admits {
				wantRefusal(t, ref, cli.CodeRetryLimit)
				return
			}
			if ref != nil {
				t.Fatalf("admit after %s: %+v", name, ref)
			}
			if ad.Attempt != 1 {
				t.Fatalf("attempt for new inputs = %d, want 1", ad.Attempt)
			}
			if n := w.failures(id); n != 2 {
				t.Fatalf("failure records = %d, want the 2 earlier ones kept", n)
			}
		})
	}
}

// Failures count for each stage: two implementer failures leave the code
// reviewer free.
func TestFailuresCountForEachStage(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	for range 2 {
		w.settle(w.mustAdmit(machine.StageImplementer, id), Settlement{Finish: "failed"})
	}
	_, ref := w.admit(machine.StageImplementer, id)
	wantRefusal(t, ref, cli.CodeRetryLimit)
	if _, err := w.bx.Update(context.Background(), id, beadsx.UpdateSpec{IfStatus: machine.Todo, Status: machine.InReview}); err != nil {
		t.Fatal(err)
	}
	w.mustAdmit(machine.StageCodeReviewer, id)
}

// not-ready, quota-hit, infra, blocked and done never count.
func TestNonCountingClassesLeaveTheCount(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	start := time.Now()
	for _, s := range []Settlement{
		{NotReady: true},
		{QuotaHit: true},
		{Finish: "blocked"},
		{Finish: "done"},
		{LastCLICall: start.Add(time.Minute), Ended: start.Add(30 * time.Minute),
			Outages: []Outage{{Start: start.Add(2 * time.Minute), End: start.Add(12 * time.Minute)}}},
	} {
		ad := w.mustAdmit(machine.StageImplementer, id)
		s.Started = start
		if out := w.settle(ad, s); out.Counted {
			t.Fatalf("%+v counted as %s", s, out.Class)
		}
	}
	if n := w.failures(id); n != 0 {
		t.Fatalf("failures = %d, want 0", n)
	}
	if n := w.count(`SELECT count(*) FROM failures WHERE item = ?`, id); n != 0 {
		t.Fatalf("%d attempt rows left after settlement", n)
	}
	ad := w.mustAdmit(machine.StageImplementer, id)
	if ad.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", ad.Attempt)
	}
}

// A crashed run counts once, even when reconcile replays its settlement.
// A claimed run with no CLI call and no finish is crashed.
func TestCrashCountsOnceUnderReplay(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	ad := w.mustAdmit(machine.StageImplementer, id)
	s := Settlement{RunID: ad.RunID, Ended: time.Now()}
	for i := range 3 {
		out, err := w.a.RecordOutcome(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		if out.Class != ClassCrashed || !out.Counted || out.Replayed != (i > 0) {
			t.Fatalf("settlement %d = %+v", i, out)
		}
		s.Finish = "done" // a replay that reads otherwise keeps the stored class
	}
	if n := w.failures(id); n != 1 {
		t.Fatalf("failures = %d, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE run_id = ? AND ended_at IS NOT NULL AND outcome_class = 'crashed'`, ad.RunID); n != 1 {
		t.Fatal("the run row did not settle as crashed")
	}
}

// A run whose end is unknown is never counted, and it stays live.
func TestUnknownEndSettlesNothing(t *testing.T) {
	w := newWorld(t)
	ad := w.mustAdmit(machine.StageImplementer, w.ticket(machine.Todo))
	out, err := w.a.RecordOutcome(context.Background(), Settlement{RunID: ad.RunID})
	if err != nil || out.Class != ClassUnknown || out.Counted {
		t.Fatalf("RecordOutcome = %+v, %v", out, err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE run_id = ? AND ended_at IS NULL`, ad.RunID); n != 1 {
		t.Fatal("an unknown end ended the run")
	}
	if _, err := w.a.RecordOutcome(context.Background(), Settlement{RunID: "nobody", Ended: time.Now()}); err == nil {
		t.Fatal("a settlement of an unknown run passed")
	}
}

// An item with an open dependency is refused, and so is a backlog ticket.
func TestDependencyOpen(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	w.deps.open[id] = []string{"tk-99"}
	_, ref := w.admit(machine.StageImplementer, id)
	wantRefusal(t, ref, cli.CodeDependencyOpen)
	_, ref = w.admit(machine.StageImplementer, w.ticket(machine.Backlog))
	wantRefusal(t, ref, cli.CodeDependencyOpen)
	if n := w.count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("a refused item reserved %d runs", n)
	}
}

// Admission makes no outside read while it holds the store's write lock or
// while the machine holds the item's lock.
func TestNoOutsideReadUnderALock(t *testing.T) {
	w := newWorld(t)
	spy := &guardSpy{a: w.a, flag: w.base.inGuard}
	m, err := machine.New(machine.Config{Store: w.st, Beads: w.a.cfg.Beads, Claims: spy, Registry: cli.Surface()})
	if err != nil {
		t.Fatal(err)
	}
	w.a.Attach(m)
	for range 3 {
		w.mustAdmit(machine.StageImplementer, w.ticket(machine.Todo))
	}
	if _, err := w.a.Peek(context.Background(), prj, machine.StageImplementer); err != nil {
		t.Fatal(err)
	}
	if w.base.calls < 3 {
		t.Fatalf("base reads = %d, want one for each admission", w.base.calls)
	}
	if spy.calls != 3 {
		t.Fatalf("guard calls = %d, want 3", spy.calls)
	}
	if w.base.blocked != 0 {
		t.Fatalf("%d base reads happened under a lock", w.base.blocked)
	}
}

type guardSpy struct {
	a     *Admission
	flag  interface{ Store(bool) }
	calls int
}

func (g *guardSpy) AdmitClaim(ctx context.Context, c machine.ClaimCheck) *cli.Refusal {
	g.calls++
	g.flag.Store(true)
	defer g.flag.Store(false)
	return g.a.AdmitClaim(ctx, c)
}

// A claim that skips admission is refused.
func TestClaimOutsideAdmissionIsRefused(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	_, res := w.m.Claim(context.Background(), machine.ClaimRequest{Project: prj, Item: id, Stage: machine.StageImplementer, RunID: "rogue"})
	if res.Refusal == nil || res.Refusal.Rule != "not_admitted" {
		t.Fatalf("a direct claim = %+v", res.Refusal)
	}
}

// Parity: "fresh state is read before a new reservation"
// (admission-recovery.test.ts, alpha). A status that moved after the
// snapshot refuses the claim and keeps no reservation.
func TestStaleStatusRefusesAndKeepsNoReservation(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	w.a.Attach(staleItems{Machine: w.m, id: id, status: machine.Repair})
	_, ref := w.admit(machine.StageImplementer, id)
	wantRefusal(t, ref, cli.CodeStatusChanged)
	if n := w.count(`SELECT count(*) FROM runs`) + w.count(`SELECT count(*) FROM failures`); n != 0 {
		t.Fatalf("%d reservation rows left", n)
	}
}

type staleItems struct {
	*machine.Machine
	id, status string
}

func (s staleItems) Show(ctx context.Context, project, item string) (machine.Item, *cli.Refusal) {
	it, ref := s.Machine.Show(ctx, project, item)
	if item == s.id {
		it.Status = s.status
	}
	return it, ref
}

// The work query runs the same policy and claims nothing.
func TestPeek(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if c, r := w.a.Peek(ctx, prj, machine.StageImplementer); c != nil || r != nil {
		t.Fatalf("peek on an empty queue = %+v %+v", c, r)
	}
	held := w.ticket(machine.Todo)
	for range 2 {
		w.settle(w.mustAdmit(machine.StageImplementer, held), Settlement{Finish: "failed"})
	}
	blocked := w.ticket(machine.Todo)
	w.deps.open[blocked] = []string{"tk-1"}
	leased := w.ticket(machine.Todo)
	w.mustAdmit(machine.StageImplementer, leased)
	free := w.ticket(machine.Repair)
	runs := w.count(`SELECT count(*) FROM runs`)
	c, r := w.a.Peek(ctx, prj, machine.StageImplementer)
	if r != nil || c == nil || c.Item != free || c.Status != machine.Repair {
		t.Fatalf("peek = %+v %+v, want %s", c, r, free)
	}
	if w.count(`SELECT count(*) FROM runs`) != runs {
		t.Fatal("peek reserved a run")
	}
	w.setCaps(map[string]int{machine.StageImplementer: 1})
	if c, r := w.a.Peek(ctx, prj, machine.StageImplementer); c != nil || r != nil {
		t.Fatalf("peek at the cap = %+v %+v", c, r)
	}
	if c, r := w.a.Peek(ctx, prj, "fixer"); r != nil || c == nil || c.Item != "" {
		t.Fatalf("peek of a stage with no item = %+v %+v", c, r)
	}
	if _, r := w.a.Peek(ctx, prj, "nonsense"); r == nil || r.Code != cli.CodeUsage {
		t.Fatalf("peek of an unknown stage = %+v", r)
	}
}

// Every stage that claims an item has a throughput entry, so no claim row
// escapes a cap.
func TestEveryClaimStageHasAThroughputEntry(t *testing.T) {
	for _, tb := range machine.Tables() {
		for _, r := range tb.Rows {
			if r.Verb == "claim" && !slices.Contains(config.RunStages(), r.Stage) {
				t.Fatalf("claim stage %s has no throughput entry", r.Stage)
			}
		}
	}
}
