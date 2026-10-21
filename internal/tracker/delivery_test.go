package tracker_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	bfake "github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/tracker"
	"github.com/iantanwx/loop/internal/tracker/fake"
)

// journal wraps the beads fake with what it lacks: a retention floor that
// makes Tail return beadsx.ErrJournalTruncated, a knob that returns records
// out of order, and counts of the reads.
type journal struct {
	*bfake.Client
	mu      sync.Mutex
	floor   int64 // the lowest retained seq; 0 retains everything
	reverse bool
	tails   int
	lists   int
}

func newJournal() *journal { return &journal{Client: bfake.New("lp")} }

func (j *journal) Tail(ctx context.Context, since int64, limit int) ([]beadsx.Event, error) {
	j.mu.Lock()
	j.tails++
	floor, reverse := j.floor, j.reverse
	j.mu.Unlock()
	if floor > 0 && since < floor-1 {
		return nil, &beadsx.BDError{Op: "events", Exit: 1, Message: "events_journal_truncated", Kind: beadsx.ErrJournalTruncated}
	}
	evs, err := j.Client.Tail(ctx, since, limit)
	if reverse {
		slices.Reverse(evs)
	}
	return evs, err
}

func (j *journal) List(ctx context.Context, f beadsx.Filter) ([]beadsx.Issue, error) {
	j.mu.Lock()
	j.lists++
	j.mu.Unlock()
	return j.Client.List(ctx, f)
}

func (j *journal) setFloor(seq int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.floor = seq
}

func (j *journal) reads() (tails, lists int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.tails, j.lists
}

// head is the seq of the last journal record.
func (j *journal) head(t *testing.T) int64 {
	t.Helper()
	evs, err := j.Client.Tail(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(evs) == 0 {
		return 0
	}
	return evs[len(evs)-1].Seq
}

func (j *journal) create(t *testing.T, title string) string {
	t.Helper()
	is, err := j.Create(context.Background(), beadsx.CreateSpec{Title: title})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return is.ID
}

func (j *journal) touch(t *testing.T, id, k, v string) {
	t.Helper()
	is, err := j.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_, err = j.Update(context.Background(), id, beadsx.UpdateSpec{IfStatus: is.Status, SetMetadata: map[string]string{k: v}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
}

// clock is a manual clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Unix(1_800_000_000, 0)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// failingSave fails the next n saves, as a crash between apply and the
// checkpoint write does.
type failingSave struct {
	tracker.CheckpointStore
	mu sync.Mutex
	n  int
}

func (f *failingSave) Save(ctx context.Context, project, adapter string, seq int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n > 0 {
		f.n--
		return errors.New("crash before the checkpoint write")
	}
	return f.CheckpointStore.Save(ctx, project, adapter, seq)
}

func newDelivery(t *testing.T, cp tracker.CheckpointStore, c *clock, batch int) *tracker.Delivery {
	t.Helper()
	return tracker.New(cp, tracker.Options{BatchSize: batch, Now: c.Now, CallTimeout: time.Second, DefaultCooldown: time.Minute})
}

func addProject(t *testing.T, d *tracker.Delivery, id string, j beadsx.Client, name string, a tracker.Adapter) {
	t.Helper()
	if err := d.AddProject(tracker.Project{ID: id, Journal: j, AdapterName: name, Adapter: a}); err != nil {
		t.Fatalf("AddProject(%s): %v", id, err)
	}
}

func deliver(t *testing.T, d *tracker.Delivery, project string) tracker.Report {
	t.Helper()
	rep, err := d.DeliverOnce(context.Background(), project)
	if err != nil {
		t.Fatalf("DeliverOnce(%s): %v", project, err)
	}
	return rep
}

func appliedSeqs(a *fake.Adapter, project string) []int64 {
	var out []int64
	for _, e := range a.Applied(project) {
		out = append(out, e.Seq)
	}
	return out
}

func span(from, to int64) []int64 {
	var out []int64
	for s := from; s <= to; s++ {
		out = append(out, s)
	}
	return out
}

func TestCheckpointAdvancesOnlyAfterApplySucceeds(t *testing.T) {
	ctx := context.Background()
	cp := tracker.NewCheckpoints(openStore(t))
	j := newJournal()
	id := j.create(t, "one")
	j.touch(t, id, "k", "1")
	j.touch(t, id, "k", "2")
	ad := fake.New()
	d := newDelivery(t, cp, newClock(), 100)
	addProject(t, d, "p", j, "linear", ad)

	ad.FailNext(1, errors.New("tracker down"))
	if _, err := d.DeliverOnce(ctx, "p"); err == nil {
		t.Fatal("DeliverOnce with a failing adapter = nil error")
	}
	if got := load(t, cp, "p", "linear"); got != 0 {
		t.Fatalf("checkpoint after a failed apply = %d, want 0", got)
	}

	rep := deliver(t, d, "p")
	if rep.Delivered != 3 || rep.Checkpoint != 3 {
		t.Fatalf("report = %+v, want 3 delivered at checkpoint 3", rep)
	}
	if got := load(t, cp, "p", "linear"); got != 3 {
		t.Fatalf("checkpoint = %d, want 3", got)
	}
}

func TestUnknownResultIsNeverSuccess(t *testing.T) {
	cp := tracker.NewCheckpoints(openStore(t))
	j := newJournal()
	j.create(t, "one")
	j.create(t, "two")
	ad := fake.New()
	d := newDelivery(t, cp, newClock(), 100)
	addProject(t, d, "p", j, "linear", ad)

	ad.LoseResultNext(1)
	if _, err := d.DeliverOnce(context.Background(), "p"); err == nil {
		t.Fatal("DeliverOnce with a lost result = nil error")
	}
	if got := load(t, cp, "p", "linear"); got != 0 {
		t.Fatalf("checkpoint after an unknown result = %d, want 0", got)
	}
	deliver(t, d, "p")
	if got := appliedSeqs(ad, "p"); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("applied = %v, want [1 2] once", got)
	}
	if got := load(t, cp, "p", "linear"); got != 2 {
		t.Fatalf("checkpoint = %d, want 2", got)
	}
}

func TestCrashBetweenApplyAndCheckpointReplaysOnce(t *testing.T) {
	st := openStore(t)
	cp := tracker.NewCheckpoints(st)
	j := newJournal()
	for _, title := range []string{"a", "b", "c"} {
		j.create(t, title)
	}
	ad := fake.New()
	c := newClock()

	crashing := newDelivery(t, &failingSave{CheckpointStore: cp, n: 1}, c, 100)
	addProject(t, crashing, "p", j, "linear", ad)
	if _, err := crashing.DeliverOnce(context.Background(), "p"); err == nil {
		t.Fatal("DeliverOnce with a failed checkpoint write = nil error")
	}
	if got := load(t, cp, "p", "linear"); got != 0 {
		t.Fatalf("checkpoint after the crash = %d, want 0", got)
	}

	// A restarted server reads the old checkpoint and replays the batch.
	restarted := newDelivery(t, cp, c, 100)
	addProject(t, restarted, "p", j, "linear", ad)
	rep := deliver(t, restarted, "p")
	if rep.Delivered != 3 || rep.Checkpoint != 3 {
		t.Fatalf("report = %+v, want the batch of 3 replayed", rep)
	}
	if ad.Calls() != 2 {
		t.Fatalf("adapter calls = %d, want 2 (apply and replay)", ad.Calls())
	}
	if got := appliedSeqs(ad, "p"); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("applied = %v, want [1 2 3] once each", got)
	}
	if got := load(t, cp, "p", "linear"); got != 3 {
		t.Fatalf("checkpoint = %d, want 3", got)
	}
}

func TestDownPastRetentionRecoversThroughReconcile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		floor func(head int64) int64
	}{
		{"floor inside the journal", func(head int64) int64 { return head - 40 }},
		{"every record pruned", func(head int64) int64 { return head + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := tracker.NewCheckpoints(openStore(t))
			j := newJournal()
			ids := []string{j.create(t, "a"), j.create(t, "b")}
			ad := fake.New()
			d := newDelivery(t, cp, newClock(), 100)
			addProject(t, d, "p", j, "linear", ad)
			deliver(t, d, "p")
			if got := load(t, cp, "p", "linear"); got != 2 {
				t.Fatalf("checkpoint = %d, want 2", got)
			}

			// The adapter is down while beads keeps writing, and the
			// journal prunes the records after the checkpoint.
			ids = append(ids, j.create(t, "c"))
			for i := range 200 {
				j.touch(t, ids[i%3], "n", string(rune('a'+i%26)))
			}
			head := j.head(t)
			j.setFloor(tc.floor(head))

			rep := deliver(t, d, "p")
			if !rep.Reconciled || rep.Checkpoint != head {
				t.Fatalf("report = %+v, want reconciled at head %d", rep, head)
			}
			if got := load(t, cp, "p", "linear"); got != head {
				t.Fatalf("checkpoint = %d, want head %d", got, head)
			}
			if ad.Reconciles("p") != 1 {
				t.Fatalf("reconciles = %d, want 1", ad.Reconciles("p"))
			}
			state := ad.State("p")
			for _, id := range ids {
				is, err := j.Get(context.Background(), id)
				if err != nil {
					t.Fatalf("get: %v", err)
				}
				if state[id].Metadata["n"] != is.Metadata["n"] {
					t.Fatalf("%s: tracker has %q, beads has %q", id, state[id].Metadata["n"], is.Metadata["n"])
				}
			}

			// Delivery then continues from the head.
			j.touch(t, ids[0], "n", "after")
			rep = deliver(t, d, "p")
			if rep.Delivered != 1 || rep.Checkpoint != head+1 {
				t.Fatalf("report after reconcile = %+v, want 1 delivered at %d", rep, head+1)
			}
			if ad.State("p")[ids[0]].Metadata["n"] != "after" {
				t.Fatal("the event after reconcile did not apply")
			}
		})
	}
}

func TestReconcileFailureLeavesCheckpointAndRetries(t *testing.T) {
	cp := tracker.NewCheckpoints(openStore(t))
	j := newJournal()
	for _, title := range []string{"a", "b", "c", "d"} {
		j.create(t, title)
	}
	j.setFloor(3)
	ad := fake.New()
	d := newDelivery(t, cp, newClock(), 100)
	addProject(t, d, "p", j, "linear", ad)

	ad.FailNext(1, errors.New("tracker down"))
	if _, err := d.DeliverOnce(context.Background(), "p"); err == nil {
		t.Fatal("DeliverOnce with a failing reconcile = nil error")
	}
	if got := load(t, cp, "p", "linear"); got != 0 {
		t.Fatalf("checkpoint after a failed reconcile = %d, want 0", got)
	}
	rep := deliver(t, d, "p")
	if !rep.Reconciled || rep.Checkpoint != 4 || ad.Reconciles("p") != 1 {
		t.Fatalf("report = %+v, reconciles %d; want one reconcile at 4", rep, ad.Reconciles("p"))
	}
	if len(ad.State("p")) != 4 {
		t.Fatalf("tracker holds %d items, want 4", len(ad.State("p")))
	}
}

func TestEventsForOneItemArriveInJournalOrder(t *testing.T) {
	cp := tracker.NewCheckpoints(openStore(t))
	j := newJournal()
	ids := []string{j.create(t, "a"), j.create(t, "b"), j.create(t, "c")}
	for round := range 4 {
		for _, id := range ids {
			j.touch(t, id, "round", string(rune('0'+round)))
		}
	}
	head := j.head(t)
	ad := fake.New()
	d := newDelivery(t, cp, newClock(), 4)
	addProject(t, d, "p", j, "linear", ad)

	for range 100 {
		if !deliver(t, d, "p").More {
			break
		}
	}
	if got := appliedSeqs(ad, "p"); !slices.Equal(got, span(1, head)) {
		t.Fatalf("applied = %v, want 1..%d in order", got, head)
	}
	last := map[string]int64{}
	for _, e := range ad.Applied("p") {
		if e.Seq <= last[e.Item] {
			t.Fatalf("%s: seq %d after %d", e.Item, e.Seq, last[e.Item])
		}
		last[e.Item] = e.Seq
	}
	for _, id := range ids {
		if got := ad.State("p")[id].Metadata["round"]; got != "3" {
			t.Fatalf("%s ends at round %q, want 3", id, got)
		}
	}
}

func TestOutOfOrderJournalAppliesNothing(t *testing.T) {
	cp := tracker.NewCheckpoints(openStore(t))
	j := newJournal()
	j.create(t, "a")
	j.create(t, "b")
	j.reverse = true
	ad := fake.New()
	d := newDelivery(t, cp, newClock(), 100)
	addProject(t, d, "p", j, "linear", ad)
	if _, err := d.DeliverOnce(context.Background(), "p"); !errors.Is(err, tracker.ErrJournalOrder) {
		t.Fatalf("DeliverOnce = %v, want ErrJournalOrder", err)
	}
	if ad.Calls() != 0 || load(t, cp, "p", "linear") != 0 {
		t.Fatalf("calls = %d, checkpoint moved; want nothing applied", ad.Calls())
	}
}

func TestProjectWithNoAdapterHasNoDelivery(t *testing.T) {
	st := openStore(t)
	j := newJournal()
	j.create(t, "a")
	ticks := make(chan time.Time)
	d := tracker.New(tracker.NewCheckpoints(st), tracker.Options{Ticks: ticks})
	if err := d.AddProject(tracker.Project{ID: "solo", Journal: j}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	rep, err := d.DeliverOnce(context.Background(), "solo")
	if err != nil || rep != (tracker.Report{}) {
		t.Fatalf("DeliverOnce = %+v, %v; want an empty report", rep, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	ticks <- time.Now()
	ticks <- time.Now() // the second send proves the first tick was handled
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if tails, lists := j.reads(); tails != 0 || lists != 0 {
		t.Fatalf("journal reads = %d tails, %d lists; want none", tails, lists)
	}
	if n := checkpointRows(t, st); n != 0 {
		t.Fatalf("checkpoint rows = %d, want 0", n)
	}
}

func TestRateLimitedAdapterSharesOneCooldown(t *testing.T) {
	ctx := context.Background()
	cp := tracker.NewCheckpoints(openStore(t))
	c := newClock()
	d := newDelivery(t, cp, c, 100)
	linear := fake.New()
	other := fake.New()
	j1, j2, j3 := newJournal(), newJournal(), newJournal()
	for _, j := range []*journal{j1, j2, j3} {
		j.create(t, "a")
	}
	addProject(t, d, "p1", j1, "linear", linear)
	addProject(t, d, "p2", j2, "linear", linear)
	addProject(t, d, "p3", j3, "jira", other)

	linear.RateLimitNext(30 * time.Second)
	if _, err := d.DeliverOnce(ctx, "p1"); !errors.Is(err, tracker.ErrRateLimited) {
		t.Fatalf("p1 = %v, want ErrRateLimited", err)
	}
	tails2, _ := j2.reads()
	for _, p := range []string{"p1", "p2"} {
		if _, err := d.DeliverOnce(ctx, p); !errors.Is(err, tracker.ErrCoolingDown) {
			t.Fatalf("%s during the cooldown = %v, want ErrCoolingDown", p, err)
		}
	}
	if linear.Calls() != 1 {
		t.Fatalf("linear calls = %d, want 1: the cooldown is shared", linear.Calls())
	}
	if tails, _ := j2.reads(); tails != tails2 {
		t.Fatal("delivery read the journal during the cooldown")
	}
	if rep := deliver(t, d, "p3"); rep.Delivered != 1 {
		t.Fatalf("another adapter = %+v, want it unaffected", rep)
	}

	c.Advance(29 * time.Second)
	if _, err := d.DeliverOnce(ctx, "p2"); !errors.Is(err, tracker.ErrCoolingDown) {
		t.Fatalf("p2 at 29s = %v, want ErrCoolingDown", err)
	}
	c.Advance(time.Second)
	for _, p := range []string{"p1", "p2"} {
		if rep := deliver(t, d, p); rep.Delivered != 1 || rep.Checkpoint != 1 {
			t.Fatalf("%s after the cooldown = %+v", p, rep)
		}
	}
}

func TestRateLimitWithNoRetryAfterUsesDefault(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	d := newDelivery(t, tracker.NewCheckpoints(openStore(t)), c, 100) // default cooldown 1m
	j := newJournal()
	j.create(t, "a")
	ad := fake.New()
	addProject(t, d, "p", j, "linear", ad)
	ad.FailNext(1, tracker.ErrRateLimited)
	if _, err := d.DeliverOnce(ctx, "p"); !errors.Is(err, tracker.ErrRateLimited) {
		t.Fatalf("DeliverOnce = %v, want ErrRateLimited", err)
	}
	c.Advance(59 * time.Second)
	if _, err := d.DeliverOnce(ctx, "p"); !errors.Is(err, tracker.ErrCoolingDown) {
		t.Fatalf("at 59s = %v, want ErrCoolingDown", err)
	}
	c.Advance(time.Second)
	deliver(t, d, "p")
}

func TestEmptyTailIsNoOp(t *testing.T) {
	st := openStore(t)
	d := newDelivery(t, tracker.NewCheckpoints(st), newClock(), 100)
	ad := fake.New()
	addProject(t, d, "p", newJournal(), "linear", ad)
	rep := deliver(t, d, "p")
	if rep != (tracker.Report{}) || ad.Calls() != 0 {
		t.Fatalf("report = %+v, calls %d; want nothing", rep, ad.Calls())
	}
	if n := checkpointRows(t, st); n != 0 {
		t.Fatalf("checkpoint rows = %d, want 0", n)
	}
}

func TestHungApplyTimesOutAndKeepsCheckpoint(t *testing.T) {
	cp := tracker.NewCheckpoints(openStore(t))
	d := tracker.New(cp, tracker.Options{CallTimeout: 20 * time.Millisecond})
	j := newJournal()
	j.create(t, "a")
	ad := fake.New()
	addProject(t, d, "p", j, "linear", ad)
	ad.SetHang(true)
	if _, err := d.DeliverOnce(context.Background(), "p"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DeliverOnce = %v, want a deadline", err)
	}
	if got := load(t, cp, "p", "linear"); got != 0 {
		t.Fatalf("checkpoint = %d, want 0", got)
	}
}

func TestCancelledApplyKeepsCheckpoint(t *testing.T) {
	cp := tracker.NewCheckpoints(openStore(t))
	d := tracker.New(cp, tracker.Options{CallTimeout: time.Minute})
	j := newJournal()
	j.create(t, "a")
	ad := fake.New()
	addProject(t, d, "p", j, "linear", ad)
	ad.SetHang(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d.DeliverOnce(ctx, "p")
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for ad.Calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("DeliverOnce = %v, want context.Canceled", err)
	}
	if got := load(t, cp, "p", "linear"); got != 0 {
		t.Fatalf("checkpoint = %d, want 0", got)
	}
	ad.SetHang(false)
	if rep := deliver(t, d, "p"); rep.Checkpoint != 1 {
		t.Fatalf("retry = %+v, want checkpoint 1", rep)
	}
}

func TestCheckpointsOfTwoProjectsAreIndependent(t *testing.T) {
	cp := tracker.NewCheckpoints(openStore(t))
	d := newDelivery(t, cp, newClock(), 100)
	ad := fake.New()
	j1, j2 := newJournal(), newJournal()
	j1.create(t, "a")
	for _, title := range []string{"a", "b", "c"} {
		j2.create(t, title)
	}
	addProject(t, d, "p1", j1, "linear", ad)
	addProject(t, d, "p2", j2, "linear", ad)

	ad.FailNext(1, errors.New("tracker down"))
	if _, err := d.DeliverOnce(context.Background(), "p1"); err == nil {
		t.Fatal("p1 = nil error")
	}
	deliver(t, d, "p2")
	if a, b := load(t, cp, "p1", "linear"), load(t, cp, "p2", "linear"); a != 0 || b != 3 {
		t.Fatalf("checkpoints = %d and %d, want 0 and 3", a, b)
	}
	deliver(t, d, "p1")
	if a, b := load(t, cp, "p1", "linear"), load(t, cp, "p2", "linear"); a != 1 || b != 3 {
		t.Fatalf("checkpoints = %d and %d, want 1 and 3", a, b)
	}
	if len(ad.Applied("p1")) != 1 || len(ad.Applied("p2")) != 3 {
		t.Fatalf("applied p1=%d p2=%d, want 1 and 3", len(ad.Applied("p1")), len(ad.Applied("p2")))
	}
}

func TestUnknownProject(t *testing.T) {
	d := newDelivery(t, tracker.NewCheckpoints(openStore(t)), newClock(), 100)
	if _, err := d.DeliverOnce(context.Background(), "nope"); !errors.Is(err, tracker.ErrUnknownProject) {
		t.Fatalf("DeliverOnce = %v, want ErrUnknownProject", err)
	}
}

func TestAddProjectValidates(t *testing.T) {
	d := newDelivery(t, tracker.NewCheckpoints(openStore(t)), newClock(), 100)
	j := newJournal()
	for _, p := range []tracker.Project{
		{ID: "", Journal: j},
		{ID: "p", Journal: j, Adapter: fake.New()},            // no adapter name
		{ID: "p", Adapter: fake.New(), AdapterName: "linear"}, // no journal
	} {
		if err := d.AddProject(p); err == nil {
			t.Fatalf("AddProject(%+v) = nil error", p)
		}
	}
	addProject(t, d, "p", j, "linear", fake.New())
	if err := d.AddProject(tracker.Project{ID: "p", Journal: j}); err == nil {
		t.Fatal("a second AddProject of one id = nil error")
	}
}

func TestRunDeliversOnEachTick(t *testing.T) {
	cp := tracker.NewCheckpoints(openStore(t))
	ticks := make(chan time.Time)
	d := tracker.New(cp, tracker.Options{BatchSize: 2, Ticks: ticks})
	j := newJournal()
	for _, title := range []string{"a", "b", "c", "d", "e"} {
		j.create(t, title)
	}
	ad := fake.New()
	addProject(t, d, "p", j, "linear", ad)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	ticks <- time.Now()
	ticks <- time.Now() // handled only after the first tick finished
	if got := appliedSeqs(ad, "p"); !slices.Equal(got, span(1, 5)) {
		t.Fatalf("applied after one tick = %v, want 1..5 (a tick drains the journal)", got)
	}
	j.create(t, "f")
	ticks <- time.Now()
	ticks <- time.Now()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if got := load(t, cp, "p", "linear"); got != 6 {
		t.Fatalf("checkpoint = %d, want 6", got)
	}
}
