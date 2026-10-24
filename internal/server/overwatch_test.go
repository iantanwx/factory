package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/overwatch"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/store"
)

// stallsKey is the key of the fake stalls part's one finding.
var stallsKey = overwatch.Key("stalls", "alpha-x1y@in_progress")

// workParts are a stalls part with one unfiled finding, whose line names
// an item, an entry and the key, and a quiet inbox part.
func workParts() []overwatch.Part {
	return []overwatch.Part{
		{Name: "inbox", Command: "loop inbox list", Read: func(context.Context, overwatch.World) (overwatch.Reading, error) {
			return overwatch.Reading{Notes: []string{"the coordinator's session runs"}}, nil
		}},
		{Name: "stalls", Read: func(context.Context, overwatch.World) (overwatch.Reading, error) {
			return overwatch.Reading{Findings: []overwatch.Finding{{Key: stallsKey,
				Line: "alpha-x1y waits in in_progress past two fires; panel/alpha/3 rang once (" + stallsKey + ")"}}}, nil
		}},
	}
}

// overwatchRings lists the lines the overwatch ring typed into the
// coordinator's session.
func (w *humanWorld) overwatchRings() []string {
	w.t.Helper()
	var out []string
	for _, n := range w.host.Notifications(w.srv.Sched.Coordinator().Session.ID) {
		if strings.HasPrefix(n, "loop overwatch:") {
			out = append(out, n)
		}
	}
	return out
}

func (w *humanWorld) lastRing() (overwatch.RingOutcome, bool) {
	w.t.Helper()
	o, ok, err := overwatch.LastRing(context.Background(), w.srv.Store)
	if err != nil {
		w.t.Fatal(err)
	}
	return o, ok
}

// sweepData calls `loop overwatch sweep` as the coordinator, and returns
// its data.
func (w *humanWorld) sweepData() map[string]any {
	w.t.Helper()
	env, code := w.coordinator("overwatch", "sweep")
	if code != 0 {
		w.t.Fatalf("overwatch sweep = %d %v", code, env)
	}
	return env["data"].(map[string]any)
}

// asData is v as the envelope carries it.
func asData(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The server registers the job overwatch as an instance job, and its part
// list outside tests is overwatch.Parts().
func TestOverwatchJobIsRegisteredOnTheScheduler(t *testing.T) {
	w := newHumanWorld(t, "")
	err := w.srv.Sched.RegisterInstanceJob(overwatch.JobName, func(context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "registered twice") {
		t.Fatalf("a second overwatch job = %v; the server registers the first", err)
	}
	if got := w.srv.overwatchPartList(); len(got) != len(overwatch.Parts()) {
		t.Fatalf("the server's parts = %d, want overwatch.Parts()", len(got))
	}
}

// C7: `loop schema` lists overwatch sweep. Called by the coordinator or
// the host it exits 0 with the report as data, and it rings nobody. A
// run's token is refused.
func TestC7TheVerbSweepsPrintsAndRingsNobody(t *testing.T) {
	v, ok := cli.Surface().Verb("overwatch sweep")
	if !ok || !slices.Equal(v.Callers, []string{"coordinator", "host"}) || v.Side != cli.SideServer {
		t.Fatalf("the schema's overwatch sweep = %+v %v", v, ok)
	}
	w := newHumanWorld(t, "")
	w.srv.setOverwatchParts(nil)
	if d := w.sweepData(); d["code"] != float64(0) || len(d["parts"].([]any)) != 0 {
		t.Fatalf("a sweep of no part = %v", d)
	}
	w.srv.setOverwatchParts(workParts())
	d := w.sweepData()
	parts := d["parts"].([]any)
	if d["code"] != float64(1) || len(parts) != 2 {
		t.Fatalf("data = %v", d)
	}
	if first := parts[0].(map[string]any); first["name"] != "stalls" || first["code"] != float64(1) ||
		!strings.Contains(first["lines"].([]any)[0].(string), stallsKey) {
		t.Fatalf("stalls = %v", first)
	}
	if _, err := rpc.EnsureAdminCredential(w.dir); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if env, code, _ := call(t, w.dir, 5*time.Second, nil, "overwatch", "sweep"); code != 0 || env["data"].(map[string]any)["code"] != float64(1) {
		t.Fatalf("overwatch sweep from the host = %d %v", code, env)
	}
	run := w.runToken("r-1", "alpha-x1y", "implementer")
	if env, _ := w.as(run, "overwatch", "sweep"); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("a run's overwatch sweep = %v", env)
	}
	if rings := w.overwatchRings(); len(rings) != 0 {
		t.Fatalf("the verb rang %q", rings)
	}
	if _, ok := w.lastRing(); ok {
		t.Fatal("the verb wrote a ring row")
	}
}

// C8: given one world, one part list and one clock, the report Job.Tick
// builds and the data `loop overwatch sweep` returns are equal.
func TestC8TheJobAndTheVerbMakeOneReport(t *testing.T) {
	w := newHumanWorld(t, "")
	w.srv.setOverwatchParts(workParts())
	j := w.srv.overwatchJob()
	if err := j.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	tick, ok := j.Last()
	if !ok {
		t.Fatal("the tick kept no report")
	}
	if got, want := w.sweepData(), asData(t, tick); !reflect.DeepEqual(got, want) {
		t.Fatalf("the verb's data\n%v\nthe job's report\n%v", got, want)
	}
}

// The ticket's done-when: one fake part that holds an unfiled finding makes
// a tick ring once and name the part, and the verb prints the same report
// and rings nobody. With a bug whose body holds the finding's key, the tick
// rings nobody.
func TestOverwatchTickRingsForAnUnfiledFindingAndNotForAFiledOne(t *testing.T) {
	w := newHumanWorld(t, "")
	w.srv.setOverwatchParts(workParts())
	j := w.srv.overwatchJob()
	if err := j.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "loop overwatch: work in stalls. Run `loop overwatch sweep`, then follow section 1 of your runbook."
	if rings := w.overwatchRings(); !slices.Equal(rings, []string{want}) {
		t.Fatalf("rings = %q", rings)
	}
	tick, _ := j.Last()
	if !reflect.DeepEqual(w.sweepData(), asData(t, tick)) || len(w.overwatchRings()) != 1 {
		t.Fatal("the verb's report differs from the tick's, or it rang")
	}
	ctx := context.Background()
	bug, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "alpha-x1y stalls", Type: "bug", Description: "Filed for " + stallsKey + "."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, bug.ID, beadsx.UpdateSpec{IfStatus: bug.Status, Status: machine.Reported}); err != nil {
		t.Fatal(err)
	}
	if err := j.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if rings := w.overwatchRings(); len(rings) != 1 {
		t.Fatalf("a filed finding rang again: %q", rings)
	}
	if r, _ := j.Last(); r.Code != overwatch.Quiet || !strings.Contains(r.Parts[0].Lines[0], bug.ID) {
		t.Fatalf("the filed tick = %+v", r)
	}
}

// C6: the ring record. An accepted ring writes accepted, a refused ring
// writes refused with its text, both with source overwatch. A ring of
// Notifier.Poke writes source inbox. `loop coordinator send` leaves the row
// as it was.
func TestC6TheRingRecordHoldsEachOutcome(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	w.srv.setOverwatchParts(workParts())
	j := w.srv.overwatchJob()
	if err := j.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if o, ok := w.lastRing(); !ok || o.Source != overwatch.SourceOverwatch || o.Result != overwatch.RingAccepted || o.Detail != "" ||
		!o.At.Equal(w.clock.Now()) {
		t.Fatalf("an accepted ring's row = %+v %v", o, ok)
	}

	session := w.srv.Sched.Coordinator().Session.ID
	w.host.RefuseNotify(session, true)
	if err := j.Tick(ctx); err == nil {
		t.Fatal("a refused ring's tick returned no error")
	}
	if o, _ := w.lastRing(); o.Source != overwatch.SourceOverwatch || o.Result != overwatch.RingRefused || !strings.Contains(o.Detail, "refused") {
		t.Fatalf("a refused ring's row = %+v", o)
	}
	if rings := w.overwatchRings(); len(rings) != 1 {
		t.Fatalf("rings = %q", rings)
	}
	w.host.RefuseNotify(session, false)

	// A new inbox entry: the notifier's ring writes source inbox.
	if err := config.StoreAlerter(w.srv.Store).Raise(ctx, config.Alert{Project: "alpha", Kind: sched.AlertRunUnknown, DedupeKey: "u",
		Subject: "a run's liveness is unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := w.srv.Notifier.Poke(ctx); err != nil {
		t.Fatal(err)
	}
	inboxRing, _ := w.lastRing()
	if inboxRing.Source != overwatch.SourceInbox || inboxRing.Result != overwatch.RingAccepted {
		t.Fatalf("the notifier's row = %+v", inboxRing)
	}

	// The operator's send, accepted or refused, writes no row.
	if _, err := rpc.EnsureAdminCredential(w.dir); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if env, code, _ := call(t, w.dir, 5*time.Second, nil, "coordinator", "send", "--body", "read the sweep"); code != 0 {
		t.Fatalf("coordinator send = %d %v", code, env)
	}
	w.host.RefuseNotify(session, true)
	if env, _, _ := call(t, w.dir, 5*time.Second, nil, "coordinator", "send", "--body", "read it again"); env["ok"] == true {
		t.Fatalf("a refused send = %v", env)
	}
	if o, _ := w.lastRing(); o != inboxRing {
		t.Fatalf("coordinator send changed the row to %+v", o)
	}
}

// C6: with no coordinator session the row holds nobody, and the tick
// returns no error.
func TestC6NoSessionRecordsNobody(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	at := time.Date(2026, 10, 24, 9, 3, 0, 0, time.UTC)
	s := &Server{Store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		opts: Options{Human: HumanOptions{Now: func() time.Time { return at }}}}
	ring := s.recordedRing(overwatch.SourceOverwatch)
	if err := ring(context.Background(), "loop overwatch: work in stalls."); !errors.Is(err, inbox.ErrNobodyToRing) {
		t.Fatalf("the ring = %v, want nobody to ring", err)
	}
	j := &overwatch.Job{World: fixedWorld{st: st, at: at}, Parts: workParts(), Ring: ring}
	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("a tick with no session = %v", err)
	}
	o, ok, err := overwatch.LastRing(context.Background(), st)
	if err != nil || !ok || o != (overwatch.RingOutcome{At: at, Source: overwatch.SourceOverwatch, Result: overwatch.RingNobody}) {
		t.Fatalf("the row = %+v %v %v", o, ok, err)
	}
}

// C6: a failed row write leaves the ring's own result unchanged.
func TestC6AFailedRowWriteLeavesTheRingAsItWas(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	if err := w.srv.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER refuse_ring BEFORE INSERT ON schedule WHEN NEW.kind = 'coordinator_ring'
			BEGIN SELECT RAISE(ABORT, 'the store refuses the ring row'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.srv.recordedRing(overwatch.SourceOverwatch)(ctx, "loop overwatch: work in stalls."); err != nil {
		t.Fatalf("an accepted ring with a refused row = %v", err)
	}
	w.waitNotified("loop overwatch: work in stalls.")
	if o, ok := w.lastRing(); ok {
		t.Fatalf("the refused write left a row %+v", o)
	}
}

// fixedWorld serves the records read of a server with no machine: no
// project, and the store. A part that makes any other read panics, and
// the sweep reads that part UNREAD.
type fixedWorld struct {
	overwatch.World
	st *store.Store
	at time.Time
}

func (f fixedWorld) Now() time.Time                                     { return f.at }
func (f fixedWorld) Projects(context.Context) ([]config.Project, error) { return nil, nil }
func (f fixedWorld) Store() *store.Store                                { return f.st }

// Work 6: the production World's intervals are the scheduler's own, with
// its defaults filled.
func TestOverwatchWorldIntervalsAreTheSchedulers(t *testing.T) {
	w := newHumanWorld(t, "")
	want := overwatch.Intervals{Tick: time.Hour, Liveness: sched.DefaultLivenessInterval, Cleanup: sched.DefaultCleanupInterval}
	if got := w.srv.overwatch.world.Intervals(); got != want {
		t.Fatalf("Intervals() = %+v, want %+v", got, want)
	}
}

// Work 6: the production World lists each loop-* directory one level
// under a directory of agent_host.checkout_root, as absolute paths. An
// operator's worktree is not listed, and a host config with no checkout
// root lists nothing.
func TestOverwatchWorldCheckoutDirsListsTheLoopsDirectories(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	root := t.TempDir()
	for _, d := range []string{"loop/loop-factorio-implementer-r-1", "loop/feature-x", "alpha/loop-room-2"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.LoadHost(w.srv.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentHost.CheckoutRoot = ""
	if err := config.SaveHost(w.srv.HostPath, cfg); err != nil {
		t.Fatal(err)
	}
	if dirs, err := w.srv.overwatch.world.CheckoutDirs(ctx); err == nil || !strings.Contains(err.Error(), "checkout_root") {
		t.Fatalf("CheckoutDirs with no root = %q %v, want an error that names checkout_root", dirs, err)
	}
	cfg.AgentHost.CheckoutRoot = root
	if err := config.SaveHost(w.srv.HostPath, cfg); err != nil {
		t.Fatal(err)
	}
	dirs, err := w.srv.overwatch.world.CheckoutDirs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "alpha/loop-room-2"), filepath.Join(root, "loop/loop-factorio-implementer-r-1")}
	if !slices.Equal(dirs, want) {
		t.Fatalf("CheckoutDirs = %q, want %q", dirs, want)
	}
}
