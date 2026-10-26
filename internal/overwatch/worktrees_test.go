package overwatch

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/sched"
)

// schedWorld is the fake world with the scheduler's reads: its intervals,
// host config, project config, its native jobs, the coordinator, and a
// temporary checkout root. Project factorio enables the implementer and
// alpha the doc-reviewer. The jobs are those of a server with GitHub and
// beads.
type schedWorld struct {
	*fakeWorld
	intervals Intervals
	host      config.HostConfig
	configs   map[string]*config.ProjectConfig // a project with none has no config
	jobs      sched.Jobs
	coord     sched.CoordinatorState
	root      string
}

func newSchedWorld(t *testing.T) *schedWorld {
	t.Helper()
	root := t.TempDir()
	w := &schedWorld{fakeWorld: newFakeWorld(t), root: root,
		intervals: Intervals{Tick: 15 * time.Second, Liveness: 15 * time.Second, Cleanup: 5 * time.Minute},
		host:      config.HostConfig{QuietPeriod: 30 * time.Minute, AgentHost: config.AgentHostConfig{CheckoutRoot: root}},
		configs: map[string]*config.ProjectConfig{
			"factorio": {Stages: map[string]config.StageConfig{config.StageImplementer: {}}},
			"alpha":    {Stages: map[string]config.StageConfig{config.StageDocReviewer: {}}},
		},
		jobs: sched.Jobs{Instance: []string{"github-reactor", "overwatch"}, Project: []string{"decision-timers", "dependency-advance", "merge-handoff"}}}
	w.coord = sched.CoordinatorState{Checkout: w.mkdir(t, "loop/loop-coordinator")}
	return w
}

func (w *schedWorld) Intervals() Intervals { return w.intervals }
func (w *schedWorld) Host(context.Context) (*config.HostConfig, error) {
	h := w.host
	return &h, nil
}
func (w *schedWorld) ProjectConfig(_ context.Context, project string) (*config.ProjectSnapshot, error) {
	c, ok := w.configs[project]
	if !ok {
		return nil, config.ErrConfigMissing
	}
	return &config.ProjectSnapshot{Config: c}, nil
}
func (w *schedWorld) Jobs() sched.Jobs                    { return w.jobs }
func (w *schedWorld) Coordinator() sched.CoordinatorState { return w.coord }
func (w *schedWorld) CheckoutDirs(context.Context) ([]string, error) {
	return ListCheckoutDirs(w.root)
}

// mkdir makes a directory under the checkout root, and returns its path.
func (w *schedWorld) mkdir(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join(w.root, rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// checkout writes one checkouts row of run, at path, in state.
func (w *schedWorld) checkout(t *testing.T, run, path, state string) {
	t.Helper()
	execStore(t, w.st, `INSERT INTO checkouts (id, project, item, path, run_id, state, created_at, updated_at) VALUES (?, 'factorio', '', ?, ?, ?, 0, 0)`,
		run, path, run, state)
}

func readWorktrees(t *testing.T, w World) PartReport {
	t.Helper()
	r := Sweep(context.Background(), w, []Part{worktreesPart()})
	if len(r.Parts) != 1 || r.Parts[0].Name != "worktrees" {
		t.Fatalf("report = %+v", r)
	}
	return r.Parts[0]
}

// workLines are the lines that quote a worktrees key.
func workLines(p PartReport) []string {
	var out []string
	for _, l := range p.Lines {
		if strings.Contains(l, "overwatch:worktrees:") {
			out = append(out, l)
		}
	}
	return out
}

// C16: a checkout row whose run ended longer ago than the quiet period
// plus the cleanup interval is work. One that ended inside that span, and
// one whose run is live, read code 0.
func TestC16ACheckoutOfARunThatEndedLongAgoIsWork(t *testing.T) {
	w := newSchedWorld(t)
	old := w.mkdir(t, "loop/loop-factorio-implementer-r-old")
	recent := w.mkdir(t, "loop/loop-factorio-implementer-r-recent")
	live := w.mkdir(t, "loop/loop-factorio-implementer-r-live")
	w.run(t, "factorio", "r-old", "factorio-a1", w.now.Add(-36*time.Minute))
	w.run(t, "factorio", "r-recent", "factorio-b2", w.now.Add(-34*time.Minute))
	w.run(t, "factorio", "r-live", "factorio-c3", time.Time{})
	w.checkout(t, "r-old", old, sched.CheckoutSettled)
	w.checkout(t, "r-recent", recent, sched.CheckoutSettled)
	w.checkout(t, "r-live", live, sched.CheckoutActive)

	p := readWorktrees(t, w)
	got := workLines(p)
	if p.Code != Work || len(got) != 1 || !strings.Contains(got[0], "overwatch:worktrees:loop-factorio-implementer-r-old") {
		t.Fatalf("code %d, lines %q; want work for r-old only", p.Code, p.Lines)
	}
}

// C16: a room's checkout ends when its room closes.
func TestC16ACheckoutOfARoomClosedLongAgoIsWork(t *testing.T) {
	w := newSchedWorld(t)
	dir := w.mkdir(t, "loop/loop-room-7")
	w.checkout(t, "room-7", dir, sched.CheckoutKept)
	execStore(t, w.st, `INSERT INTO rooms (id, project, checkout_id, state, opened_at, closed_at) VALUES ('room-7', 'factorio', 'room-7', 'closed', 0, ?)`,
		sql.NullInt64{Int64: w.now.Add(-2 * time.Hour).UnixMilli(), Valid: true})
	p := readWorktrees(t, w)
	if got := workLines(p); p.Code != Work || len(got) != 1 || !strings.Contains(got[0], "overwatch:worktrees:loop-room-7") {
		t.Fatalf("code %d, lines %q; want work for loop-room-7", p.Code, p.Lines)
	}
}

// C16: a loop-* directory that no row names is work. A deleted row names
// no directory.
func TestC16ADirectoryThatNoRowNamesIsWork(t *testing.T) {
	w := newSchedWorld(t)
	w.mkdir(t, "loop/loop-factorio-implementer-r-stray")
	gone := w.mkdir(t, "alpha/loop-alpha-implementer-r-deleted")
	w.checkout(t, "r-deleted", gone, sched.CheckoutDeleted)
	p := readWorktrees(t, w)
	got := workLines(p)
	if p.Code != Work || len(got) != 2 ||
		!strings.Contains(strings.Join(got, "\n"), "overwatch:worktrees:loop-factorio-implementer-r-stray") ||
		!strings.Contains(strings.Join(got, "\n"), "overwatch:worktrees:loop-alpha-implementer-r-deleted") {
		t.Fatalf("code %d, lines %q; want work for both directories", p.Code, p.Lines)
	}
}

// C16: a row whose path does not exist is work, even while its run lives.
func TestC16ARowWhosePathDoesNotExistIsWork(t *testing.T) {
	w := newSchedWorld(t)
	w.run(t, "factorio", "r-gone", "factorio-a1", time.Time{})
	w.checkout(t, "r-gone", filepath.Join(w.root, "loop", "loop-factorio-implementer-r-gone"), sched.CheckoutActive)
	p := readWorktrees(t, w)
	if got := workLines(p); p.Code != Work || len(got) != 1 || !strings.Contains(got[0], "overwatch:worktrees:loop-factorio-implementer-r-gone") {
		t.Fatalf("code %d, lines %q; want work for the missing path", p.Code, p.Lines)
	}
}

// C16: the coordinator's own checkout is never work, though no row names
// it.
func TestC16TheCoordinatorsCheckoutIsNeverWork(t *testing.T) {
	w := newSchedWorld(t)
	w.mkdir(t, "alpha/loop-coordinator") // an earlier coordinator project's checkout
	p := readWorktrees(t, w)
	if p.Code != Quiet || len(workLines(p)) != 0 {
		t.Fatalf("code %d, lines %q; the coordinator's checkout is no work", p.Code, p.Lines)
	}
	w.coord = sched.CoordinatorState{} // no session yet
	if p := readWorktrees(t, w); p.Code != Quiet {
		t.Fatalf("code %d, lines %q; the coordinator's checkout is no work before its session", p.Code, p.Lines)
	}
}

// C16: an operator's worktree named feature-x is never read. The part
// stays quiet though nothing can read inside it, and no line names it.
func TestC16AnOperatorsWorktreeIsNeverRead(t *testing.T) {
	w := newSchedWorld(t)
	fx := w.mkdir(t, "loop/feature-x")
	w.mkdir(t, "loop/feature-x/loop-nested")
	if err := os.Chmod(fx, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fx, 0o755) })

	dirs, err := ListCheckoutDirs(w.root)
	if err != nil {
		t.Fatalf("ListCheckoutDirs = %v; it read feature-x", err)
	}
	if want := []string{w.coord.Checkout}; !slices.Equal(dirs, want) {
		t.Fatalf("ListCheckoutDirs = %q, want %q", dirs, want)
	}
	p := readWorktrees(t, w)
	if p.Code != Quiet || strings.Contains(strings.Join(p.Lines, "\n"), "feature-x") {
		t.Fatalf("code %d, lines %q; feature-x is never read", p.Code, p.Lines)
	}
}

// C16: a fault that names the key files the checkout.
func TestC16AFaultThatNamesTheKeyFilesTheCheckout(t *testing.T) {
	w := newSchedWorld(t)
	w.mkdir(t, "loop/loop-factorio-implementer-r-stray")
	w.fault(t, "cleanup misses overwatch:worktrees:loop-factorio-implementer-r-stray", "acknowledged")
	if p := readWorktrees(t, w); p.Code != Quiet {
		t.Fatalf("code %d, lines %q; want the filed finding quiet", p.Code, p.Lines)
	}
}

// ListCheckoutDirs lists each loop-* directory one level under each
// directory of the root, as absolute paths, in order. A file named loop-*
// is no checkout.
func TestListCheckoutDirsListsOnlyLoopDirectories(t *testing.T) {
	w := newSchedWorld(t)
	a := w.mkdir(t, "alpha/loop-alpha-implementer-r-1")
	w.mkdir(t, "alpha/feature-y")
	w.mkdir(t, "loop-top") // directly under the root: a repository directory, not a checkout
	if err := os.WriteFile(filepath.Join(w.root, "alpha", "loop-file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	dirs, err := ListCheckoutDirs(w.root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{a, w.coord.Checkout}; !slices.Equal(dirs, want) {
		t.Fatalf("ListCheckoutDirs = %q, want %q", dirs, want)
	}
	if _, err := ListCheckoutDirs("relative/root"); err == nil {
		t.Fatal("a relative root lists; want an error")
	}
}
