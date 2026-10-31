package userspace

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// alerts is an in-memory config.Alerter: the open alert of each key.
type alerts struct {
	mu   sync.Mutex
	open map[string]config.Alert
}

func (a *alerts) Raise(_ context.Context, al config.Alert) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.open == nil {
		a.open = map[string]config.Alert{}
	}
	a.open[al.DedupeKey] = al
	return nil
}

func (a *alerts) Clear(_ context.Context, key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.open, key)
	return nil
}

// get returns the open alert of key.
func (a *alerts) get(key string) (config.Alert, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	al, ok := a.open[key]
	return al, ok
}

// fetcher answers each fetch of a pin with the tree or the error that the
// test sets for its name, and counts each call. A gate held for a name
// blocks its fetches until the test releases it.
type fetcher struct {
	mu    sync.Mutex
	trees map[string]fs.FS
	fail  map[string]error
	gate  map[string]chan struct{}
	calls map[string]int
}

func newFetcher() *fetcher {
	return &fetcher{trees: map[string]fs.FS{}, fail: map[string]error{}, gate: map[string]chan struct{}{},
		calls: map[string]int{}}
}

func (f *fetcher) fetch(ctx context.Context, pin config.SkillPin) (fs.FS, error) {
	f.mu.Lock()
	f.calls[pin.Name]++
	gate := f.gate[pin.Name]
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[pin.Name]; err != nil {
		return nil, err
	}
	return f.trees[pin.Name], nil
}

func (f *fetcher) set(name string, tree fs.FS, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trees[name], f.fail[name] = tree, err
}

func (f *fetcher) hold(name string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := make(chan struct{})
	f.gate[name] = g
	return g
}

func (f *fetcher) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

type world struct {
	st     Store
	f      *fetcher
	alerts *alerts
	s      *Syncer
	done   chan string // the name of each pin whose fetch ended and settled
	neon   config.SkillPin
	pg     config.SkillPin
}

func newWorld(t *testing.T) *world {
	w := &world{st: Store{Dir: t.TempDir()}, f: newFetcher(), alerts: &alerts{}, done: make(chan string, 64)}
	w.s = NewSyncer(w.st, w.f.fetch, w.alerts, slog.New(slog.DiscardHandler))
	w.s.ended = func(name string) { w.done <- name }
	neon, pg := neonTree(), neonTreeNamed("neon-postgres")
	w.neon, w.pg = pinOf(t, "neon", neon), pinOf(t, "neon-postgres", pg)
	w.f.set("neon", neon, nil)
	w.f.set("neon-postgres", pg, nil)
	return w
}

// settled waits until one fetch ends and the syncer settles its project.
func settled(t *testing.T, w *world) {
	t.Helper()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no fetch ended")
	}
}

// quiet fails t when a fetch ends soon: the last Observe started none.
func quiet(t *testing.T, w *world) {
	t.Helper()
	select {
	case name := <-w.done:
		t.Fatalf("a fetch of %s ran, want none", name)
	case <-time.After(100 * time.Millisecond):
	}
}

const p1Key = config.AlertProjectSkills + ":p1"

// C12: a pin that the store does not hold is fetched once, and stored.
// A second Observe while that fetch runs fetches nothing, and Observe
// returns before the fetch ends. An Observe whose pins the store holds
// fetches nothing.
func TestC12ObserveFetchesEachPinTheStoreLacksOnce(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	gate := w.f.hold("neon")
	returned := make(chan struct{})
	go func() {
		w.s.Observe(ctx, "p1", []config.SkillPin{w.neon})
		w.s.Observe(ctx, "p1", []config.SkillPin{w.neon})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Observe waited for a fetch that blocks")
	}
	if _, ok := w.alerts.get(p1Key); ok {
		t.Fatal("a pin whose first fetch still runs raised an alert")
	}
	close(gate)
	settled(t, w)
	if n := w.f.count("neon"); n != 1 {
		t.Fatalf("fetches of neon = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(w.st.Dir, "p1", "neon", w.neon.SHA256, "SKILL.md")); err != nil {
		t.Fatalf("the store lacks neon after its fetch: %v", err)
	}
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon})
	quiet(t, w)
	if n := w.f.count("neon"); n != 1 {
		t.Fatalf("fetches of neon = %d after an Observe of a held pin, want 1", n)
	}
}

// C12: a new Syncer over a store that holds each pin, as after a restart,
// fetches nothing. A stored tree that is deleted, or changes in one byte,
// is fetched again at the next Observe. An Observe after a failed fetch
// fetches again.
func TestC12ObserveFetchesAgainOnlyWhatTheStoreLost(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if err := w.st.Put("p1", w.neon, neonTree()); err != nil {
		t.Fatal(err)
	}
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon})
	quiet(t, w)
	if n := w.f.count("neon"); n != 0 {
		t.Fatalf("fetches after a restart over a held pin = %d, want 0", n)
	}

	dir := filepath.Join(w.st.Dir, "p1", "neon", w.neon.SHA256)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon})
	settled(t, w)
	if n := w.f.count("neon"); n != 1 {
		t.Fatalf("fetches after the tree was deleted = %d, want 1", n)
	}

	if err := os.WriteFile(filepath.Join(dir, "references", "api.md"), []byte("# APi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon})
	settled(t, w)
	if n := w.f.count("neon"); n != 2 {
		t.Fatalf("fetches after one byte changed = %d, want 2", n)
	}

	w.f.set("neon-postgres", nil, errors.New("codeload: 503"))
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon, w.pg})
	settled(t, w)
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon, w.pg})
	settled(t, w)
	if n, m := w.f.count("neon-postgres"), w.f.count("neon"); n != 2 || m != 2 {
		t.Fatalf("fetches of neon-postgres, neon = %d, %d; want 2, 2", n, m)
	}
}

// C11: a failed pin raises one alert for its project, keyed by it, and a
// second failed pin joins the same alert. A project that pins nothing has
// none. After a fetch passes for each pin, the alert closes.
func TestC11OneAlertForEachProjectAndItCloses(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.f.set("neon", nil, errors.New("codeload: 404"))
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon})
	settled(t, w)
	w.s.Observe(ctx, "p2", nil)
	al, ok := w.alerts.get(p1Key)
	if !ok || al.Kind != "project_skills_missing" || al.Project != "p1" || !strings.Contains(al.Subject, "neon") {
		t.Fatalf("alert = %+v, %v; want one project_skills_missing for p1 that names neon", al, ok)
	}
	if len(w.alerts.open) != 1 {
		t.Fatalf("open alerts = %v, want p1's only", w.alerts.open)
	}

	// A tree with another hash fails as a fetch error does.
	w.f.set("neon-postgres", neonTree(), nil)
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon, w.pg})
	settled(t, w)
	settled(t, w)
	al, _ = w.alerts.get(p1Key)
	if !strings.Contains(al.Subject, "neon,") || !strings.Contains(al.Subject, "neon-postgres") {
		t.Fatalf("alert subject = %q, want neon and neon-postgres", al.Subject)
	}

	// A retry does not close the alert while it runs.
	w.f.set("neon", neonTree(), nil)
	w.f.set("neon-postgres", neonTreeNamed("neon-postgres"), nil)
	gate := w.f.hold("neon")
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon, w.pg})
	settled(t, w) // neon-postgres passes, neon still blocks
	al, ok = w.alerts.get(p1Key)
	if !ok || strings.Contains(al.Subject, "neon-postgres") {
		t.Fatalf("alert = %+v, %v while neon's retry runs; want one that names neon only", al, ok)
	}
	close(gate)
	settled(t, w)
	if al, ok := w.alerts.get(p1Key); ok {
		t.Fatalf("alert = %+v after each pin passed, want it closed", al)
	}
}

// C11: a list that drops the failed pin, while the store holds each
// other pin, closes the alert. A held pin whose stored tree is deleted
// and whose next fetch fails opens it again.
func TestC11AlertClosesWhenTheListDropsTheFailedPin(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if err := w.st.Put("p1", w.neon, neonTree()); err != nil {
		t.Fatal(err)
	}
	w.f.set("neon-postgres", nil, errors.New("codeload: 404"))
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon, w.pg})
	settled(t, w)
	if _, ok := w.alerts.get(p1Key); !ok {
		t.Fatal("no alert for the failed neon-postgres")
	}
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon})
	if al, ok := w.alerts.get(p1Key); ok {
		t.Fatalf("alert = %+v after the list dropped neon-postgres, want it closed", al)
	}

	if err := os.RemoveAll(filepath.Join(w.st.Dir, "p1", "neon", w.neon.SHA256)); err != nil {
		t.Fatal(err)
	}
	w.f.set("neon", nil, errors.New("codeload: 503"))
	w.s.Observe(ctx, "p1", []config.SkillPin{w.neon})
	settled(t, w)
	al, ok := w.alerts.get(p1Key)
	if !ok || !strings.Contains(al.Subject, "neon") || !strings.Contains(al.Body, "codeload: 503") {
		t.Fatalf("alert = %+v, %v; want one that names neon and its reason", al, ok)
	}
}
