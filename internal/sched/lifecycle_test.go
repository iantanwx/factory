package sched

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
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

// A host restart that changes handles leaves the run unknown. Past the age
// bound of two run budgets it raises a coordinator-owned alert and
// reclaims nothing.
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
