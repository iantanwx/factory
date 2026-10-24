package sched

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

func principal(st *Started) rpc.Principal {
	return rpc.Principal{Kind: cli.PrincipalRun, RunID: st.RunID, Project: st.Project, Item: st.Item, Stage: st.Stage, Checkout: st.Checkout}
}

// AE2: the server is down longer than the lease TTL while a run stays
// live. On start the run is re-adopted and its lease renewed, not
// reclaimed. The epoch stays, so the run's next write succeeds.
func TestLiveRunIsReadoptedAfterALongOutage(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	before := w.lease(id)
	w.clock.Advance(3 * time.Hour)
	w.host.Restart(true)
	time.Sleep(5 * time.Millisecond) // the store stamps renewed_at with its own clock
	w.restart()
	after := w.lease(id)
	if after.Owner != st.RunID || after.Epoch != before.Epoch || !after.RenewedAt.After(before.RenewedAt) {
		t.Fatalf("lease before %+v, after %+v", before, after)
	}
	if s, _ := w.runState(st.RunID); s != "running" || w.status(id) != machine.InProgress || w.failures(id) != 0 {
		t.Fatalf("state %s, status %s", s, w.status(id))
	}
	if !w.liveToken(st.RunID) {
		t.Fatal("the re-adopted run's token was revoked")
	}
	w.tick()
	if s, _ := w.runState(st.RunID); s != "running" {
		t.Fatalf("state after a tick = %s", s)
	}
}

// A host restart that changes handles leaves the run unknown. Its process
// is still traced under the new handle, so past the age bound of two run
// budgets it raises a coordinator-owned alert and reclaims nothing.
func TestUnknownLivenessPastTheAgeBoundAlertsOnly(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.RunBudget = time.Hour })
	id := w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	w.host.Restart(false)
	w.restart()
	w.tick()
	if s, _ := w.runState(st.RunID); s != "unknown" {
		t.Fatalf("state = %s, want unknown", s)
	}
	w.clock.Advance(90 * time.Minute)
	w.tick()
	if w.alerts(AlertRunUnknown) != 0 {
		t.Fatal("an alert before the age bound")
	}
	w.clock.Advance(31 * time.Minute)
	w.tick()
	w.tick()
	if w.alerts(AlertRunUnknown) != 1 {
		t.Fatalf("unknown alerts = %d, want 1", w.alerts(AlertRunUnknown))
	}
	if w.lease(id).Owner != st.RunID || w.status(id) != machine.InProgress || w.failures(id) != 0 {
		t.Fatal("an unknown run was reclaimed")
	}
}

// C-bug-1 (F-21): an orca restart moves the epoch, and a run whose terminal
// died with it stays unknown forever. Past the age bound, a complete
// inventory with no terminal and no PTY carrying the run's incarnation
// reclaims the run as failed. A run still traceable under a new handle
// stays unknown.
func TestUnknownRunWithNoTraceIsReclaimedPastTheAgeBound(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.RunBudget = time.Hour })
	gone, traced := w.ticket(machine.Todo), w.ticket(machine.Todo)
	g := w.dispatch(machine.StageImplementer)
	tr := w.dispatch(machine.StageImplementer)
	if w.lease(gone).Owner != g.RunID {
		gone, traced = traced, gone
	}
	// The terminal of g vanishes. The host restarts, and tr's process
	// reappears under a new handle with the same incarnation and PTY.
	if err := w.host.Close(context.Background(), g.Session.ID); err != nil {
		t.Fatal(err)
	}
	w.host.Restart(false)
	w.restart()
	w.tick()
	for _, r := range []*Started{g, tr} {
		if s, _ := w.runState(r.RunID); s != RunUnknown {
			t.Fatalf("run %s state = %s, want unknown", r.RunID, s)
		}
	}
	w.clock.Advance(90 * time.Minute)
	w.tick()
	if s, _ := w.runState(g.RunID); s != RunUnknown || w.lease(gone).Owner != g.RunID {
		t.Fatalf("vanished run reclaimed before the age bound: state %s", s)
	}
	w.clock.Advance(31 * time.Minute)
	w.tick()
	w.tick()
	if s, _ := w.runState(g.RunID); s != "settled" || w.failures(gone) != 1 || w.lease(gone).Owner == g.RunID {
		t.Fatalf("vanished run: state %s, failures %d, lease %+v; want settled, 1 failure, lease freed",
			s, w.failures(gone), w.lease(gone))
	}
	if w.status(gone) != machine.Todo || w.alerts(AlertRunCrashed) != 1 {
		t.Fatalf("vanished run: status %s, crash alerts %d; want todo, 1", w.status(gone), w.alerts(AlertRunCrashed))
	}
	if _, err := os.Stat(g.Checkout); err != nil {
		t.Fatalf("vanished run's checkout: %v", err)
	}
	if s, _ := w.runState(tr.RunID); s != RunUnknown || w.lease(traced).Owner != tr.RunID || w.failures(traced) != 0 {
		t.Fatalf("traced run: state %s, lease %+v, failures %d; want unknown and held", s, w.lease(traced), w.failures(traced))
	}
	if w.alerts(AlertRunUnknown) != 1 {
		t.Fatalf("unknown alerts = %d, want 1 for the traced run", w.alerts(AlertRunUnknown))
	}
}

// C-bug-1: an incomplete inventory proves nothing absent. Past the age
// bound, a run with no trace that only incomplete inventories have read
// keeps its lease and raises only the unknown alert.
func TestUnknownRunOnAnIncompleteInventoryIsNotReclaimed(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.RunBudget = time.Hour })
	id := w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	if err := w.host.Close(context.Background(), st.Session.ID); err != nil {
		t.Fatal(err)
	}
	w.host.Restart(false)
	w.host.SetInventoryIncomplete(true)
	w.restart()
	w.tick()
	w.clock.Advance(121 * time.Minute)
	w.tick()
	w.tick()
	if s, _ := w.runState(st.RunID); s != RunUnknown || w.lease(id).Owner != st.RunID || w.failures(id) != 0 {
		t.Fatalf("state %s, lease %+v, failures %d; want unknown and held", s, w.lease(id), w.failures(id))
	}
	if w.alerts(AlertRunUnknown) != 1 {
		t.Fatalf("unknown alerts = %d, want 1", w.alerts(AlertRunUnknown))
	}
}

// AE6: during a drain no run is admitted. The drain ends when the runs
// finish or the timeout passes, and no lease is lost.
func TestDrainAdmitsNothingAndLosesNoLease(t *testing.T) {
	w := newWorld(t)
	a, b := w.ticket(machine.Todo), w.ticket(machine.Todo)
	w.ticket(machine.Todo)
	r1, r2 := w.dispatch(machine.StageImplementer), w.dispatch(machine.StageImplementer)
	ds := w.s.Drain(context.Background(), 0)
	if !ds.Draining || ds.Drained || ds.InFlight != 2 {
		t.Fatalf("drain = %+v", ds)
	}
	if _, ref := w.s.Dispatch(context.Background(), prj, machine.StageImplementer); ref == nil || ref.Code != cli.CodeDraining {
		t.Fatalf("dispatch during a drain = %+v", ref)
	}
	if !w.a.Draining() {
		t.Fatal("admission is not draining")
	}
	w.clock.Advance(DefaultDrainTimeout + time.Minute)
	ds = w.s.Drain(context.Background(), 0)
	if !ds.TimedOut || ds.Drained {
		t.Fatalf("drain after the timeout = %+v", ds)
	}
	if w.lease(a).Owner != r1.RunID || w.lease(b).Owner != r2.RunID {
		t.Fatal("the drain lost a lease")
	}
	for _, r := range []*Started{r1, r2} {
		if _, ref := w.s.Finish(context.Background(), r.RunID, "failed"); ref != nil {
			t.Fatal(ref)
		}
	}
	if ds = w.s.Drain(context.Background(), time.Second); !ds.Drained || ds.InFlight != 0 {
		t.Fatalf("drain after both finished = %+v", ds)
	}
	w.tick()
	if len(w.host.Launches()) != 2 {
		t.Fatal("a tick during the drain started a run")
	}
}

// Cleanup deletes a settled checkout that is clean and fully pushed. It
// skips one with unpushed commits, and one unreadable checkout does not
// stop the others.
func TestCheckoutCleanup(t *testing.T) {
	w := newWorld(t)
	var started []*Started
	for range 3 {
		w.ticket(machine.InReview)
		st := w.dispatch(machine.StageCodeReviewer)
		if _, ref := w.s.Finish(context.Background(), st.RunID, "done"); ref != nil {
			t.Fatal(ref)
		}
		started = append(started, st)
	}
	broken, unpushed, clean := started[0], started[1], started[2]
	if err := os.RemoveAll(broken.Checkout); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unpushed.Checkout, "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, unpushed.Checkout, "add", "work.txt")
	gitIn(t, unpushed.Checkout, "commit", "-q", "-m", "work")
	w.tick() // closes the finished sessions
	rep := w.s.CleanCheckouts(context.Background())
	if len(rep.Deleted) != 1 || rep.Deleted[0] != clean.Checkout || len(rep.Errors) != 1 || rep.Errors[broken.Checkout] == "" {
		t.Fatalf("report = %+v", rep)
	}
	if exists(clean.Checkout) || !exists(unpushed.Checkout) {
		t.Fatal("cleanup deleted the wrong checkout")
	}
	if s := w.str(`SELECT state FROM checkouts WHERE path = ?`, unpushed.Checkout); s != CheckoutSettled {
		t.Fatalf("unpushed checkout state = %s", s)
	}
}

// A run that hits a usage limit pauses that harness until the reset, and
// the chooser picks the next profile.
func TestQuotaHitPausesTheHarness(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) {
		c.Harnesses = []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}
	})
	a := w.ticket(machine.Todo)
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	if st.Profile.Harness != agenthost.Claude {
		t.Fatalf("first profile = %+v", st.Profile)
	}
	w.host.SetQuota(agenthost.Quota{Harness: agenthost.Claude, OK: true, Windows: []agenthost.QuotaWindow{{Name: "5h", UsedPercent: 100}}})
	w.host.Exit(st.Session.ID, 1)
	w.tick()
	if _, class := w.runState(st.RunID); class != "quota-hit" || w.failures(a) != 0 {
		t.Fatalf("class %s, failures %d", class, w.failures(a))
	}
	if n := w.alerts(AlertUsageLimit); n != 1 {
		t.Fatalf("usage-limit alerts = %d", n)
	}
	// The reading recovers, and the pause still holds until its reset.
	w.host.SetQuota(agenthost.Quota{Harness: agenthost.Claude, OK: true})
	next := w.dispatch(machine.StageImplementer)
	if next.Profile.Harness != agenthost.Codex || next.Profile.Model != DefaultModels[agenthost.Codex] {
		t.Fatalf("next profile = %+v", next.Profile)
	}
	if _, ref := w.s.Finish(context.Background(), next.RunID, "failed"); ref != nil {
		t.Fatal(ref)
	}
	w.clock.Advance(DefaultQuotaPause + time.Minute)
	if again := w.dispatch(machine.StageImplementer); again.Profile.Harness != agenthost.Claude {
		t.Fatalf("after the reset = %+v", again.Profile)
	}
}

// A session whose agent wrote only a usage limit settles quota-hit at once,
// with no quota reading, as when Claude Code is proxied. The harness pauses
// until the limit's reset, not for the fixed pause. One inbox line names
// the window, however many runs hit it.
func TestALimitedSessionSettlesQuotaHitAtOnce(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) {
		c.Harnesses = []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}
	})
	a, b := w.ticket(machine.Todo), w.ticket(machine.Todo)
	w.ticket(machine.Todo)
	first, second := w.dispatch(machine.StageImplementer), w.dispatch(machine.StageImplementer)
	if first.Profile.Harness != agenthost.Claude || second.Profile.Harness != agenthost.Claude {
		t.Fatalf("profiles = %+v, %+v", first.Profile, second.Profile)
	}
	reset := w.clock.Now().Add(3 * time.Hour)
	for _, st := range []*Started{first, second} {
		w.host.HitLimit(st.Session.ID, agenthost.Limit{Message: "You've hit your weekly limit · resets 3am (Asia/Singapore)", At: w.clock.Now(), ResetAt: reset})
	}
	w.tick()
	for _, st := range []*Started{first, second} {
		if state, class := w.runState(st.RunID); state != "settled" || class != "quota-hit" {
			t.Fatalf("run %s: state %s, class %s", st.RunID, state, class)
		}
	}
	if w.failures(a)+w.failures(b) != 0 {
		t.Fatal("a usage limit counted as a failure")
	}
	if n := w.alerts(AlertUsageLimit); n != 1 {
		t.Fatalf("usage-limit alerts = %d, want one for the window", n)
	}
	subject := w.str(`SELECT subject FROM alerts WHERE kind = ?`, AlertUsageLimit)
	body := w.str(`SELECT body FROM alerts WHERE kind = ?`, AlertUsageLimit)
	if !strings.Contains(subject, reset.UTC().Format(time.RFC3339)) || !strings.Contains(body, "weekly limit") || !strings.Contains(body, "change the account") {
		t.Fatalf("alert = %q / %q", subject, body)
	}
	// Past the fixed pause, the reset still holds Claude.
	w.clock.Advance(DefaultQuotaPause + time.Minute)
	next := w.dispatch(machine.StageImplementer)
	if next.Profile.Harness != agenthost.Codex {
		t.Fatalf("before the reset = %+v", next.Profile)
	}
	if _, ref := w.s.Finish(context.Background(), next.RunID, "failed"); ref != nil {
		t.Fatal(ref)
	}
	w.clock.Advance(reset.Sub(w.clock.Now()) + time.Minute)
	if again := w.dispatch(machine.StageImplementer); again.Profile.Harness != agenthost.Claude {
		t.Fatalf("after the reset = %+v", again.Profile)
	}
}

// A limit that names no reset, such as the Fable limit, falls in the window
// of the pause that still holds. A second hit in that window raises no
// second inbox line, and it does not move the end of the pause.
func TestAHitWithNoResetFallsInTheWindowThatHolds(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) {
		c.Harnesses = []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}
	})
	w.ticket(machine.Todo)
	w.ticket(machine.Todo)
	w.ticket(machine.Todo)
	first, second := w.dispatch(machine.StageImplementer), w.dispatch(machine.StageImplementer)
	if first.Profile.Harness != agenthost.Claude || second.Profile.Harness != agenthost.Claude {
		t.Fatalf("profiles = %+v, %+v", first.Profile, second.Profile)
	}
	fable := "You've reached your Fable limit. Run /usage-credits to continue."
	end := w.clock.Now().Add(DefaultQuotaPause)
	w.host.HitLimit(first.Session.ID, agenthost.Limit{Message: fable, At: w.clock.Now()})
	w.tick()
	w.clock.Advance(10 * time.Minute)
	w.host.HitLimit(second.Session.ID, agenthost.Limit{Message: fable, At: w.clock.Now()})
	w.tick()
	for _, st := range []*Started{first, second} {
		if state, class := w.runState(st.RunID); state != "settled" || class != "quota-hit" {
			t.Fatalf("run %s: state %s, class %s", st.RunID, state, class)
		}
	}
	if n := w.alerts(AlertUsageLimit); n != 1 {
		t.Fatalf("usage-limit alerts = %d, want one for the window", n)
	}
	if due := w.count(`SELECT due_at FROM schedule WHERE kind = 'quota_pause' AND stage = 'claude'`); int64(due) != end.UnixMilli() {
		t.Fatalf("pause ends %s, want %s", time.UnixMilli(int64(due)).UTC(), end.UTC())
	}
	w.clock.Advance(end.Sub(w.clock.Now()) + time.Minute)
	if again := w.dispatch(machine.StageImplementer); again.Profile.Harness != agenthost.Claude {
		t.Fatalf("after the first window = %+v", again.Profile)
	}
}

// A hit whose reset comes before the end of the pause that holds does not
// end that pause sooner.
func TestAPauseNeverEndsBeforeOneThatHolds(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) {
		c.Harnesses = []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}
	})
	for range 4 {
		w.ticket(machine.Todo)
	}
	first, second := w.dispatch(machine.StageImplementer), w.dispatch(machine.StageImplementer)
	if first.Profile.Harness != agenthost.Claude || second.Profile.Harness != agenthost.Claude {
		t.Fatalf("profiles = %+v, %+v", first.Profile, second.Profile)
	}
	t0 := w.clock.Now()
	weekly := t0.Add(3 * time.Hour)
	w.host.HitLimit(first.Session.ID, agenthost.Limit{Message: "You've hit your weekly limit", At: t0, ResetAt: weekly})
	w.tick()
	w.clock.Advance(10 * time.Minute)
	w.host.HitLimit(second.Session.ID, agenthost.Limit{Message: "You've hit your session limit", At: w.clock.Now(), ResetAt: t0.Add(40 * time.Minute)})
	w.tick()
	if due := w.count(`SELECT due_at FROM schedule WHERE kind = 'quota_pause' AND stage = 'claude'`); int64(due) != weekly.UnixMilli() {
		t.Fatalf("pause ends %s, want %s", time.UnixMilli(int64(due)).UTC(), weekly.UTC())
	}
	// Past the second reset, the first still holds Claude.
	w.clock.Advance(t0.Add(41 * time.Minute).Sub(w.clock.Now()))
	next := w.dispatch(machine.StageImplementer)
	if next.Profile.Harness != agenthost.Codex {
		t.Fatalf("before the weekly reset = %+v", next.Profile)
	}
	if _, ref := w.s.Finish(context.Background(), next.RunID, "failed"); ref != nil {
		t.Fatal(ref)
	}
	w.clock.Advance(weekly.Sub(w.clock.Now()) + time.Minute)
	if again := w.dispatch(machine.StageImplementer); again.Profile.Harness != agenthost.Claude {
		t.Fatalf("after the weekly reset = %+v", again.Profile)
	}
}

// The chooser skips a harness whose window reads 90 % or more. A run that
// ends with no finish while its window reads under 100 % hit no limit.
func TestTheChooserSkipsAWindowAt90Percent(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) {
		c.Harnesses = []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}
	})
	a := w.ticket(machine.Todo)
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	if st.Profile.Harness != agenthost.Claude {
		t.Fatalf("first profile = %+v", st.Profile)
	}
	w.host.SetQuota(agenthost.Quota{Harness: agenthost.Claude, OK: true, Windows: []agenthost.QuotaWindow{{Name: "5h", UsedPercent: 95}}})
	w.host.Exit(st.Session.ID, 1)
	w.tick()
	if _, class := w.runState(st.RunID); class != "crashed" || w.failures(a) != 1 {
		t.Fatalf("class %s, failures %d", class, w.failures(a))
	}
	w.host.SetQuota(agenthost.Quota{Harness: agenthost.Claude, OK: true, Windows: []agenthost.QuotaWindow{{Name: "5h", UsedPercent: 90}}})
	if next := w.dispatch(machine.StageImplementer); next.Profile.Harness != agenthost.Codex {
		t.Fatalf("at 90%% = %+v", next.Profile)
	}
}

// The coordinator session starts with the service and restarts after a
// crash.
func TestCoordinatorRestartsAfterACrash(t *testing.T) {
	w := newWorld(t)
	w.cfg.Coordinator = &CoordinatorConfig{Project: prj, Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	if err := w.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.tick()
	first := w.s.Coordinator()
	if first.Session == nil || first.Session.Kind != agenthost.KindCoordinator || !w.liveToken(first.RunID) {
		t.Fatalf("coordinator = %+v", first)
	}
	w.tick()
	if got := w.s.Coordinator(); got.RunID != first.RunID {
		t.Fatal("a live coordinator was restarted")
	}
	w.host.Exit(first.Session.ID, 1)
	w.tick()
	second := w.s.Coordinator()
	if second.Session == nil || second.RunID == first.RunID || second.Checkout != first.Checkout {
		t.Fatalf("after the crash = %+v", second)
	}
	if w.liveToken(first.RunID) || !w.liveToken(second.RunID) {
		t.Fatal("the tokens did not move to the new session")
	}
	tf, err := cli.ReadTokenFile(second.Checkout)
	if err != nil || tf.Kind != cli.PrincipalCoordinator || tf.RunID != second.RunID {
		t.Fatalf("coordinator token %+v, %v", tf, err)
	}
	// A restart of the server keeps the coordinator it finds live.
	w.restart()
	w.tick()
	if got := w.s.Coordinator(); got.RunID != second.RunID {
		t.Fatal("a server restart replaced a live coordinator")
	}
}

// A run admitted but not started when the server died settles as
// not-ready on the next start, and its claim is released.
func TestOrphanAdmittedRunIsReleasedOnStart(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	ad, ref := w.a.Admit(context.Background(), admissionRequest(id))
	if ref != nil {
		t.Fatal(ref)
	}
	w.restart()
	if _, class := w.runState(ad.RunID); class != "not-ready" || w.status(id) != machine.Todo || w.lease(id).Owner != "" {
		t.Fatalf("class %s, status %s", class, w.status(id))
	}
}

// probes wraps the fake host and counts the scheduler's own liveness and
// inventory reads. The fake's Ended reads liveness without Settle.
type probes struct {
	*fake.Host
	settles, inventories atomic.Int64
	failInventory        atomic.Bool
}

func (h *probes) Settle(ctx context.Context, id agenthost.RunID) (agenthost.Settlement, error) {
	h.settles.Add(1)
	return h.Host.Settle(ctx, id)
}

func (h *probes) Inventory(ctx context.Context) (agenthost.Inventory, error) {
	h.inventories.Add(1)
	if h.failInventory.Load() {
		return agenthost.Inventory{}, errors.New("the inventory read failed")
	}
	return h.Host.Inventory(ctx)
}

// A watch reads each run's liveness once, through Ended, and reads the
// inventory at most once, even when that read fails.
func TestWatchReadsLivenessAndInventoryOnce(t *testing.T) {
	w := newWorld(t)
	h := &probes{Host: w.host}
	w.cfg.Agents = h
	w.rebuild()
	w.ticket(machine.Todo)
	w.ticket(machine.InReview)
	a := w.dispatch(machine.StageImplementer)
	b := w.dispatch(machine.StageCodeReviewer)
	h.settles.Store(0)
	h.inventories.Store(0)
	w.tick()
	if n := h.settles.Load(); n != 0 {
		t.Fatalf("a watch of two live runs called Settle %d times, want 0", n)
	}
	if n := h.inventories.Load(); n != 1 {
		t.Fatalf("a watch read the inventory %d times, want 1", n)
	}
	h.failInventory.Store(true)
	w.tick()
	if n := h.inventories.Load(); n != 2 {
		t.Fatalf("a watch with a failed inventory read it %d more times, want 1", n-1)
	}
	for _, st := range []*Started{a, b} {
		if s, _ := w.runState(st.RunID); s != "running" {
			t.Fatalf("run %s state = %s, want running", st.RunID, s)
		}
	}
}

// A tick forgets each run's last call once the call is older than the
// write gap, so the map holds only runs that called recently.
func TestTickPrunesOldLastCalls(t *testing.T) {
	w := newWorld(t)
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	w.s.OnCall(context.Background(), principal(st))
	calls := func() int {
		w.s.mu.Lock()
		defer w.s.mu.Unlock()
		return len(w.s.lastCall)
	}
	w.tick()
	if n := calls(); n != 1 {
		t.Fatalf("last calls inside the gap = %d, want 1", n)
	}
	w.clock.Advance(callWriteGap)
	w.tick()
	if n := calls(); n != 0 {
		t.Fatalf("last calls past the gap = %d, want 0", n)
	}
	w.s.OnCall(context.Background(), principal(st))
	if got := w.count(`SELECT coalesce(last_cli_call_at, 0) FROM runs WHERE run_id = ?`, st.RunID); int64(got) != w.clock.Now().UnixMilli() {
		t.Fatalf("a call after the prune was not written: last_cli_call_at = %d", got)
	}
}

// The cleanup pass purges the idempotency keys whose expiry passed, and
// keeps a key with no expiry.
func TestCleanupPurgesExpiredKeys(t *testing.T) {
	w := newWorld(t)
	err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		for key, expires := range map[string]time.Time{"gone": tx.Now().Add(-time.Minute), "kept": {}} {
			if err := tx.StoreResult(store.IdemRecord{Key: key, Fingerprint: store.Fingerprint{RunID: "r-x", Verb: "work move"},
				Result: []byte(`{}`), ExpiresAt: expires}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w.tick()
	if n := w.count(`SELECT count(*) FROM idempotency`); n != 2 {
		t.Fatalf("keys before the cleanup interval = %d, want 2", n)
	}
	w.clock.Advance(DefaultCleanupInterval)
	w.tick()
	if w.count(`SELECT count(*) FROM idempotency WHERE key = 'gone'`) != 0 || w.count(`SELECT count(*) FROM idempotency WHERE key = 'kept'`) != 1 {
		t.Fatal("the cleanup did not purge only the expired key")
	}
}
