package admission

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

const prj = "alpha"

// world is one store, fake beads, a host config file, a fake base branch
// and an admission over a machine that uses it as its ClaimGuard.
type world struct {
	t        *testing.T
	st       *store.Store
	bx       *fake.Client
	beads    *editable
	hostPath string
	base     *fakeBase
	deps     *fakeDeps
	pulls    *fakePulls
	m        *machine.Machine
	a        *Admission
	runs     atomic.Int64
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := config.StoreProjectRow(st)(ctx, config.Project{ID: prj, Repo: "iantanwx/alpha", Path: "/srv/alpha"}); err != nil {
		t.Fatal(err)
	}
	bx := fake.New("tk")
	if err := bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, st: st, bx: bx, beads: &editable{Client: bx, body: map[string]string{}},
		hostPath: filepath.Join(dir, config.HostConfigFile), base: newFakeBase(st), deps: &fakeDeps{open: map[string][]string{}},
		pulls: &fakePulls{}}
	w.setCaps(nil)
	w.build("v1")
	return w
}

// build makes the admission and the machine, as a server start does.
func (w *world) build(release string) {
	w.t.Helper()
	resolver := func(string) (beadsx.Client, error) { return w.beads, nil }
	a, err := New(Config{Store: w.st, Host: &config.HostLoader{Path: w.hostPath}, Beads: resolver,
		Base: w.base, Deps: w.deps, Pulls: w.pulls, Release: release})
	if err != nil {
		w.t.Fatal(err)
	}
	m, err := machine.New(machine.Config{Store: w.st, Beads: resolver, Claims: a, Registry: cli.Surface(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		w.t.Fatal(err)
	}
	a.Attach(m)
	w.m, w.a = m, a
}

// setCaps writes host config with the default throughput and the given
// caps over it.
func (w *world) setCaps(caps map[string]int) {
	w.t.Helper()
	cfg := config.DefaultHost("/var/lib/loop")
	for k, n := range caps {
		e := cfg.Throughput[k]
		e.Cap = n
		cfg.Throughput[k] = e
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(w.hostPath, data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// put creates an item of kind in status with a body.
func (w *world) put(kind machine.Kind, status, body string) string {
	w.t.Helper()
	ctx := context.Background()
	t, _ := machine.TableFor(kind)
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "item", Description: body, Type: t.BeadType})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: status}); err != nil {
		w.t.Fatal(err)
	}
	return is.ID
}

func (w *world) ticket(status string) string { return w.put(machine.KindTicket, status, declaredBody) }

func (w *world) nextRun() string { return fmt.Sprintf("run-%d", w.runs.Add(1)) }

func (w *world) admit(stage, item string) (Admitted, *cli.Refusal) {
	return w.a.Admit(context.Background(), Request{Project: prj, Stage: stage, Item: item, RunID: w.nextRun(),
		Harness: "claude", Model: "opus"})
}

func (w *world) mustAdmit(stage, item string) Admitted {
	w.t.Helper()
	ad, ref := w.admit(stage, item)
	if ref != nil {
		w.t.Fatalf("admit %s %s: %+v", stage, item, ref)
	}
	return ad
}

func wantRefusal(t *testing.T, ref *cli.Refusal, code string) {
	t.Helper()
	if ref == nil || ref.Code != code {
		t.Fatalf("want %s, got %+v", code, ref)
	}
}

// settle records the run's outcome, then returns the item to its queue
// the way the scheduler does at settlement.
func (w *world) settle(ad Admitted, s Settlement) Outcome {
	w.t.Helper()
	s.RunID = ad.RunID
	if s.Ended.IsZero() {
		s.Ended = time.Now()
	}
	out, err := w.a.RecordOutcome(context.Background(), s)
	if err != nil {
		w.t.Fatal(err)
	}
	if ad.Item != "" {
		res := w.m.Apply(context.Background(), machine.Request{Project: prj, Item: ad.Item, Verb: "release",
			Actor: machine.Server("test")})
		if res.Refusal != nil {
			w.t.Fatalf("release %s: %+v", ad.Item, res.Refusal)
		}
	}
	return out
}

func (w *world) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(query, args...).Scan(&n) }); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) failures(item string) int {
	return w.count(`SELECT count(*) FROM failures WHERE item = ? AND class IN ('failed', 'crashed')`, item)
}

// declaredBody declares one file and cites one document.
const declaredBody = "Fix the thing.\n\n## Files\n\n**May change:**\n- `app/task.go`\n\n**Must not change:**\n- `elsewhere/`\n\nSee `docs/plans/thing.md`.\n"

// editable lets a test edit an item's body, which the fake cannot.
type editable struct {
	beadsx.Client
	mu   sync.Mutex
	body map[string]string
}

func (e *editable) edit(id, body string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.body[id] = body
}

func (e *editable) Get(ctx context.Context, id string) (beadsx.Issue, error) {
	is, err := e.Client.Get(ctx, id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if b, ok := e.body[id]; ok {
		is.Description = b
	}
	return is, err
}

// fakeBase is a base branch. Each read proves no store write is in flight
// on this goroutine's path: a read under the write lock would block here.
type fakeBase struct {
	st      *store.Store
	mu      sync.Mutex
	files   map[string]string
	calls   int
	blocked int // reads made while the store's write lock was held
	inGuard *atomic.Bool
}

func newFakeBase(st *store.Store) *fakeBase {
	return &fakeBase{st: st, files: map[string]string{
		"app/task.go": "blob1", "docs/plans/thing.md": "blob2", "unrelated.txt": "blob3",
	}, inGuard: &atomic.Bool{}}
}

func (b *fakeBase) set(path, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.files[path] = id
}

func (b *fakeBase) Files(ctx context.Context, _ string, prefixes []string) (map[string]string, error) {
	lockFree(ctx, b.st, &b.mu, &b.blocked)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.inGuard.Load() {
		b.blocked++
	}
	out := map[string]string{}
	for p, id := range b.files {
		for _, pre := range prefixes {
			if strings.HasPrefix(p, pre) {
				out[p] = id
			}
		}
	}
	return out, nil
}

// lockFree takes the store's write lock for an instant. A caller that
// already holds it would wait out the timeout, and the read counts as
// blocked.
func lockFree(ctx context.Context, st *store.Store, mu *sync.Mutex, blocked *int) {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := st.Write(c, func(*store.Tx) error { return nil }); err != nil {
		mu.Lock()
		*blocked++
		mu.Unlock()
	}
}

type fakeDeps struct {
	mu   sync.Mutex
	open map[string][]string
}

func (d *fakeDeps) OpenDependencies(_ context.Context, _, item string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.open[item], nil
}

// workEdit applies the work edit row as the coordinator does.
func (w *world) workEdit(id, field, value string) {
	w.t.Helper()
	res := w.m.Apply(context.Background(), machine.Request{Project: prj, Item: id, Verb: "edit", Command: "work edit",
		Params: map[string]string{field: value}, Actor: machine.Actor{Name: "coordinator:test"}})
	if res.Refusal != nil {
		w.t.Fatalf("work edit: %+v", *res.Refusal)
	}
}
