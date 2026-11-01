//go:build acceptance

package scenarios

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// nextBinary builds, once, a loop binary of a later release.
var (
	nextOnce sync.Once
	nextBin  string
	nextErr  error
)

const nextVersion = "v0.0.1-acceptance"

func nextRelease(t *testing.T) string {
	t.Helper()
	nextOnce.Do(func() { nextBin, nextErr = acceptance.BuildVersion(binDir, nextVersion) })
	if nextErr != nil {
		t.Fatal(nextErr)
	}
	return nextBin
}

func lease(w *acceptance.World, item string) machine.Lease {
	w.T.Helper()
	l, err := w.Srv.Machine.Lease(context.Background(), acceptance.Project, item)
	if err != nil {
		w.T.Fatal(err)
	}
	return l
}

// AE2 (R17, R26): a live run's agent keeps working through a server outage
// longer than the lease TTL. When the server starts, it re-adopts the run
// and renews the lease before it reclaims anything, so the run is not
// reclaimed, and its next write succeeds.
func TestAE2ALiveRunIsReadoptedAfterALongOutage(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	id := createTicket(w, "Add a", "Create `a.txt` holding the line `a`.")
	run := w.Dispatch(machine.StageImplementer)
	before := lease(w, id)

	w.Stop()
	w.Host.Advance(3 * time.Hour)
	w.Host.Write(run.Session.ID) // the agent kept working
	time.Sleep(5 * time.Millisecond)
	w.Start()

	after := lease(w, id)
	if after.Owner != run.RunID || after.Epoch != before.Epoch || !after.RenewedAt.After(before.RenewedAt) {
		t.Fatalf("lease before %+v, after %+v", before, after)
	}
	if s := w.RunState(run.RunID); s != "running" {
		t.Fatalf("run state = %s", s)
	}
	wantStatus(w, id, machine.InProgress)
	if n := counted(w); n != 0 {
		t.Fatalf("%d failures counted", n)
	}
	w.Commit(run.Checkout, "work done through the outage", map[string]string{"a.txt": "a\n"})
	w.Must(w.In(run.Checkout, "branch", "push"))
	w.Must(w.In(run.Checkout, "pr", "open", "--title", "Add a"))
	passSimplify(w, id)
	wantStatus(w, id, machine.InReview)
}

// AE4 (R18): the server restarts while an agent runs a write. The CLI
// waits and retries. When the agent's own write already landed, the retry
// exits 0 and reports a replay. When another actor's write won, the retry
// exits 3 and the state shows the other actor's move.
func TestAE4AWriteAcrossARestart(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	id := createTicket(w, "Add a", "Create `a.txt` holding the line `a`.")
	run := w.Dispatch(machine.StageImplementer)
	notes := func() int {
		return w.Count(`SELECT count(*) FROM transitions WHERE item = ? AND verb = 'note'`, id)
	}

	t.Run("waits through a restart", func(t *testing.T) {
		w.Stop()
		cmd, out := w.Command(run.Checkout, nil, "work", "note", id, "--body", "written while the server was down")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if cmd.ProcessState != nil {
			t.Fatal("the CLI did not wait for the server")
		}
		w.Start()
		r := w.Parse(cmd.Args[1:], cmd, out, cmd.Wait())
		if r.Code != 0 || r.Env["replayed"] == true || notes() != 1 {
			t.Fatalf("%s; %d notes", r, notes())
		}
	})

	t.Run("its own write landed", func(t *testing.T) {
		// The server applies the write to beads and stops before it
		// answers: the write's call outlasts the graceful stop, so the
		// server closes the CLI's connection with no reply.
		w.AfterUpdate(id, func(ctx context.Context) {
			w.Cancel()
			select {
			case <-ctx.Done():
			case <-time.After(3 * acceptance.ShutdownTimeout):
			}
		})
		cmd, out := w.Command(run.Checkout, nil, "work", "note", id, "--body", "landed before the stop")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		w.Stopped()
		w.Start()
		r := w.Parse(cmd.Args[1:], cmd, out, cmd.Wait())
		if r.Code != 0 || r.Env["replayed"] != true {
			t.Fatalf("%s", r)
		}
		if n := notes(); n != 2 {
			t.Fatalf("%d notes, want the earlier one and this one once", n)
		}
	})

	t.Run("another actor won", func(t *testing.T) {
		// The run's work goes to review, and a reviewer holds it.
		head := w.Commit(run.Checkout, "a", map[string]string{"a.txt": "a\n"})
		w.Must(w.In(run.Checkout, "branch", "push"))
		w.Must(w.In(run.Checkout, "pr", "open", "--title", "Add a"))
		w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
		passSimplify(w, run.Item)
		rev := w.Dispatch(machine.StageCodeReviewer)
		// After the server reads the item for the CLI's key, CI turns red
		// and the reactor moves the ticket to repair. Then the server
		// stops, so the CLI's write waits for the next server.
		var hookErr error
		w.AfterGet(id, func(context.Context) {
			w.CI(head, "failure")
			hookErr = w.GH.Reactor.Tick(context.Background())
			w.Cancel()
			if !w.WaitUnreachable() {
				hookErr = context.DeadlineExceeded
			}
		})
		cmd, out := w.Command(rev.Checkout, nil, "review", "submit", id, "--verdict", "pass", "--head", head)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		w.Stopped()
		if hookErr != nil {
			t.Fatalf("the other actor: %v", hookErr)
		}
		w.Start()
		r := w.Parse(cmd.Args[1:], cmd, out, cmd.Wait())
		if r.Code != 3 || r.RefusalCode() != cli.CodeStatusChanged || r.Env["current_status"] != machine.Repair {
			t.Fatalf("%s", r)
		}
		if it := w.Show(id); it.Status != machine.Repair || it.Fact(machine.FactReviewedHead) != "" {
			t.Fatalf("the state is %s, reviewed %q", it.Status, it.Fact(machine.FactReviewedHead))
		}
	})
}

// AE5 (R10): a ticket failed twice at one stage with unchanged inputs. An
// unrelated merge to main leaves a third attempt refused. An edit of the
// ticket, a merge that changes one of its declared files, and a new loop
// release each admit an attempt.
func TestAE5TheRetryLimitFollowsTheInputs(t *testing.T) {
	w := newWorld(t, acceptance.Options{Files: map[string]string{"docs/notes/a.md": "The first note.\n"}})
	id := createTicket(w, "Add a", "Follow docs/notes/a.md and create `a.txt`.")
	failTwice := func() {
		t.Helper()
		for range 2 {
			run := w.Dispatch(machine.StageImplementer)
			if run.Item != id {
				t.Fatalf("the implementer took %s", run.Item)
			}
			w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "failed"))
			wantStatus(w, id, machine.Todo)
		}
	}
	refused := func(why string) {
		t.Helper()
		if st, ref := w.TryDispatch(machine.StageImplementer); st != nil || ref != nil {
			t.Fatalf("%s: a third attempt = %+v %+v", why, st, ref)
		}
		if r := w.Coordinator("work", "peek", "--project", acceptance.Project, "--stage", machine.StageImplementer); r.Code != 1 {
			t.Fatalf("%s: %s", why, r)
		}
	}
	admitted := func(why string) {
		t.Helper()
		run := w.Dispatch(machine.StageImplementer)
		if run.Item != id || run.Attempt != 1 {
			t.Fatalf("%s: run %s on %s, attempt %d", why, run.RunID, run.Item, run.Attempt)
		}
		w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "failed"))
	}

	failTwice()
	refused("unchanged inputs")
	w.PushMain("an unrelated merge", map[string]string{"other.txt": "other\n"}, "")
	w.React()
	refused("an unrelated merge")

	w.Must(w.Coordinator("work", "edit", id, "--project", acceptance.Project,
		"--body", "Follow docs/notes/a.md and create `a.txt` with one line."))
	admitted("an edit")
	w.Must(w.In(w.Dispatch(machine.StageImplementer).Checkout, "run", "finish", "--outcome", "failed"))
	refused("the edited inputs failed twice")

	w.PushMain("change the declared note", map[string]string{"docs/notes/a.md": "The second note.\n"}, "")
	w.React()
	admitted("a merge that changes a declared file")
	w.Must(w.In(w.Dispatch(machine.StageImplementer).Checkout, "run", "finish", "--outcome", "failed"))
	refused("the new base failed twice")

	w.Upgrade(nextVersion, nextRelease(t))
	admitted("a new loop release")
}

// AE6 (R41): two runs are in flight, and the host drains for an upgrade.
// The drain admits no new run. It ends when the runs finish or the drain
// timeout passes, and the upgrade swaps only then. No lease is lost: the
// run still in flight keeps its lease across the swap and finishes with
// the binary it started with.
func TestAE6AnUpgradeDrainsAndLosesNoLease(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	a := createTicket(w, "Add a", "Create `a.txt` holding the line `a`.")
	b := createTicket(w, "Add b", "Create `b.txt` holding the line `b`.")
	runA := w.Dispatch(machine.StageImplementer)
	runB := w.Dispatch(machine.StageImplementer)
	c := createTicket(w, "Add c", "Create `c.txt` holding the line `c`.")
	if runA.Item == b {
		runA, runB = runB, runA
	}
	if runA.Item != a || runB.Item != b {
		t.Fatalf("runs took %s and %s", runA.Item, runB.Item)
	}
	leaseB := lease(w, b)

	drain, out := w.Command(t.TempDir(), nil, "drain")
	if err := drain.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !w.Srv.Sched.Draining() {
		if time.Now().After(deadline) {
			t.Fatal("the drain did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st, ref := w.TryDispatch(machine.StageImplementer); st != nil || ref == nil || ref.Code != cli.CodeDraining {
		t.Fatalf("a dispatch during the drain = %+v %+v", st, ref)
	}

	w.Commit(runA.Checkout, "a", map[string]string{"a.txt": "a\n"})
	w.Must(w.In(runA.Checkout, "branch", "push"))
	w.Must(w.In(runA.Checkout, "pr", "open", "--title", "Add a"))
	w.Must(w.In(runA.Checkout, "run", "finish", "--outcome", "done"))
	if lease(w, b).Owner != runB.RunID {
		t.Fatal("the drain took run B's lease")
	}
	w.Host.Advance(31 * time.Minute) // past the drain timeout, with B in flight
	r := w.Parse(drain.Args[1:], drain, out, drain.Wait())
	if r.Code != 0 || r.Data()["timed_out"] != true || r.Data()["in_flight"] != float64(1) {
		t.Fatalf("%s", r)
	}

	w.Upgrade(nextVersion, nextRelease(t))
	if l := lease(w, b); l.Owner != runB.RunID || l.Epoch != leaseB.Epoch {
		t.Fatalf("B's lease after the upgrade = %+v, before %+v", l, leaseB)
	}
	if s := w.RunState(runB.RunID); s != "running" {
		t.Fatalf("run B is %s", s)
	}
	w.Commit(runB.Checkout, "b", map[string]string{"b.txt": "b\n"})
	w.Must(w.InBinary(acceptance.Binary, runB.Checkout, "branch", "push"))
	w.Must(w.InBinary(acceptance.Binary, runB.Checkout, "pr", "open", "--title", "Add b"))
	w.Must(w.InBinary(acceptance.Binary, runB.Checkout, "run", "finish", "--outcome", "done"))
	passSimplify(w, a)
	passSimplify(w, b)
	wantStatus(w, a, machine.InReview)
	wantStatus(w, b, machine.InReview)
	if n := w.Count(`SELECT count(*) FROM transitions WHERE verb IN ('reclaim', 'release')`); n != 0 {
		t.Fatalf("%d leases were reclaimed or released", n)
	}
	// The new release admits again. The implementer's deadline passed
	// while the clock moved, so the new server's first tick starts c's run
	// itself, once.
	deadline = time.Now().Add(30 * time.Second)
	for w.Status(c) != machine.InProgress {
		if time.Now().After(deadline) {
			t.Fatalf("the new release admitted no run on %s", c)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := w.Count(`SELECT count(*) FROM runs WHERE item = ?`, c); n != 1 {
		t.Fatalf("%d runs on %s", n, c)
	}
}
