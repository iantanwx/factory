package sched

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	beadsfake "github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

const prj = "alpha"

// world is one instance: a store, fake beads, the machine and admission, a
// secret store, run tokens, a fake agent host whose checkouts are real git
// worktrees of a project repository with an origin, and a scheduler.
type world struct {
	t        *testing.T
	dir      string
	st       *store.Store
	bx       *beadsfake.Client
	m        *machine.Machine
	a        *admission.Admission
	sec      *secrets.Store
	tokens   *rpc.Tokens
	host     *fake.Host
	clock    *fake.Clock
	hostPath string
	repo     string // the project's local clone
	origin   string // its bare origin
	root     string // the checkout root
	project  *projectSource
	co       *gitCheckouts
	s        *Scheduler
	runs     atomic.Int64
	cfg      Config // the scheduler's config, for a rebuild
}

func newWorld(t *testing.T) *world {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w := &world{t: t, dir: dir, st: st, hostPath: filepath.Join(dir, config.HostConfigFile), root: filepath.Join(dir, "checkouts")}
	if err := os.MkdirAll(w.root, 0o755); err != nil {
		t.Fatal(err)
	}
	w.origin, w.repo = makeProjectRepo(t, dir)
	if err := config.StoreProjectRow(st)(ctx, config.Project{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}); err != nil {
		t.Fatal(err)
	}
	w.writeHost(nil, nil)
	w.bx = beadsfake.New("tk")
	if err := w.bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	keys, err := secrets.NewFileKeyProvider(ensureKey(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	w.sec = secrets.New(st, keys)
	w.tokens = &rpc.Tokens{Store: st}
	w.clock = fake.NewClock(time.Now().UTC().Truncate(time.Second))
	w.host = fake.New(w.root, fake.WithClock(w.clock))
	w.co = &gitCheckouts{t: t, host: w.host, repo: w.repo}
	w.project = &projectSource{snap: snapshot(t, "stages:\n  implementer: {}\n  code-reviewer: {}\nmerge:\n  mechanism: direct\n", "")}
	w.build()
	return w
}

func ensureKey(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "keys", "kek")
	if err := secrets.EnsureKeyFile(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// build makes the machine, admission and scheduler, as a server start does.
func (w *world) build() {
	w.t.Helper()
	resolver := func(string) (beadsx.Client, error) { return w.bx, nil }
	a, err := admission.New(admission.Config{Store: w.st, Host: &config.HostLoader{Path: w.hostPath}, Beads: resolver, Release: "v1"})
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
	w.cfg = Config{
		Store: w.st, Host: &config.HostLoader{Path: w.hostPath},
		Projects: func() ([]config.Project, error) {
			return []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}}, nil
		},
		ProjectConfig: w.project.load,
		Machine:       m, Admission: a, Beads: resolver, Secrets: w.sec, Tokens: w.tokens,
		Agents: w.host, Checkouts: func(string) CheckoutHost { return w.co },
		Pack:   brief.Pack{Version: "v1", FS: fstest.MapFS{"loop-implementer/SKILL.md": {Data: []byte("# runbook\n")}}},
		Alerts: config.StoreAlerter(w.st), Release: "v1", Now: w.clock.Now,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewRunID: func() string { return fmt.Sprintf("r-%d", w.runs.Add(1)) },
	}
	w.rebuild()
}

// rebuild makes a new scheduler over the same machine, as a restart does.
func (w *world) rebuild() {
	w.t.Helper()
	s, err := New(w.cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	w.s = s
}

// restart makes new admission, machine and scheduler over the same store,
// beads and host, and runs the start-up reconcile.
func (w *world) restart() {
	w.t.Helper()
	w.build()
	if err := w.s.Reconcile(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

// writeHost writes host config with the default throughput, the given caps
// and schedules over it.
func (w *world) writeHost(caps map[string]int, schedules map[string]string, edit ...func(*config.HostConfig)) {
	w.t.Helper()
	cfg := config.DefaultHost("/var/lib/loop")
	// Nothing fires on its own in a test: each stage and job runs once a
	// year unless the test sets its schedule.
	for k, e := range cfg.Throughput {
		if e.Schedule != "" {
			e.Schedule = "0 0 1 1 *"
			cfg.Throughput[k] = e
		}
	}
	for k, n := range caps {
		e := cfg.Throughput[k]
		e.Cap = n
		cfg.Throughput[k] = e
	}
	for k, s := range schedules {
		e := cfg.Throughput[k]
		e.Schedule = s
		cfg.Throughput[k] = e
	}
	for _, f := range edit {
		f(cfg)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(w.hostPath, data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func snapshot(t *testing.T, cfg, manifest string) *config.ProjectSnapshot {
	t.Helper()
	pc, err := config.ParseProjectConfig([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	snap := &config.ProjectSnapshot{Config: pc, Manifest: &config.Manifest{}}
	if manifest != "" {
		if snap.Manifest, err = config.ParseManifest([]byte(manifest)); err != nil {
			t.Fatal(err)
		}
	}
	return snap
}

type projectSource struct {
	mu   sync.Mutex
	snap *config.ProjectSnapshot
}

func (p *projectSource) set(s *config.ProjectSnapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snap = s
}

func (p *projectSource) load(context.Context, string) (*config.ProjectSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snap, nil
}

// makeProjectRepo makes a bare origin with one commit on main that tracks
// the project's .loop files, and a clone of it.
func makeProjectRepo(t *testing.T, dir string) (origin, repo string) {
	t.Helper()
	origin = filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")
	repo = filepath.Join(dir, "alpha")
	gitIn(t, dir, "init", "-q", "--bare", "-b", "main", origin)
	gitIn(t, dir, "init", "-q", "-b", "main", seed)
	for _, f := range []string{".loop/config.yaml", ".loop/secrets.yaml", "README.md"} {
		p := filepath.Join(seed, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "init")
	gitIn(t, seed, "push", "-q", origin, "main")
	gitIn(t, dir, "clone", "-q", origin, repo)
	return origin, repo
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitCheckouts makes each fake checkout a real git worktree of the project
// clone, as orca's worktree create does.
type gitCheckouts struct {
	t    *testing.T
	host *fake.Host
	repo string
	fail atomic.Bool
}

func (g *gitCheckouts) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	if g.fail.Load() {
		return agenthost.Checkout{}, fmt.Errorf("%w: test", agenthost.ErrHostUnavailable)
	}
	co, err := g.host.CreateCheckout(ctx, spec)
	if err != nil {
		return co, err
	}
	gitIn(g.t, g.repo, "worktree", "add", "-q", "-b", spec.Name, co.Path, "origin/main")
	return co, nil
}

// RemoveCheckout forgets the checkout in the fake and removes the worktree.
func (g *gitCheckouts) RemoveCheckout(ctx context.Context, path string) error {
	if err := g.host.RemoveCheckout(ctx, path); err != nil {
		return err
	}
	gitIn(g.t, g.repo, "worktree", "remove", "--force", path)
	return nil
}

func admissionRequest(item string) admission.Request {
	return admission.Request{Project: prj, Stage: machine.StageImplementer, Item: item, RunID: "orphan-1", Harness: "claude", Model: "opus"}
}

// ticket makes a ticket in status.
func (w *world) ticket(status string) string {
	w.t.Helper()
	ctx := context.Background()
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "Fix the thing", Description: "No paths.", Type: "task"})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: status}); err != nil {
		w.t.Fatal(err)
	}
	return is.ID
}

func (w *world) status(id string) string {
	w.t.Helper()
	it, ref := w.m.Show(context.Background(), prj, id)
	if ref != nil {
		w.t.Fatal(ref)
	}
	return it.Status
}

func (w *world) dispatch(stage string) *Started {
	w.t.Helper()
	st, ref := w.s.Dispatch(context.Background(), prj, stage)
	if ref != nil {
		w.t.Fatalf("dispatch %s: %+v", stage, ref)
	}
	if st == nil {
		w.t.Fatalf("dispatch %s started nothing", stage)
	}
	return st
}

func (w *world) tick() {
	w.t.Helper()
	if err := w.s.Tick(context.Background()); err != nil {
		w.t.Fatal(err)
	}
	w.s.Wait()
}

func (w *world) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(query, args...).Scan(&n) }); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) str(query string, args ...any) string {
	w.t.Helper()
	var s sql.NullString
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(query, args...).Scan(&s) }); err != nil {
		w.t.Fatal(err)
	}
	return s.String
}

func (w *world) runState(run string) (state, class string) {
	return w.str(`SELECT state FROM runs WHERE run_id = ?`, run), w.str(`SELECT outcome_class FROM runs WHERE run_id = ?`, run)
}

func (w *world) failures(item string) int {
	return w.count(`SELECT count(*) FROM failures WHERE item = ? AND class IN ('failed', 'crashed')`, item)
}

func (w *world) alerts(kind string) int {
	return w.count(`SELECT coalesce(sum(count), 0) FROM alerts WHERE kind = ?`, kind)
}

func (w *world) lease(item string) machine.Lease {
	w.t.Helper()
	l, err := w.m.Lease(context.Background(), prj, item)
	if err != nil {
		w.t.Fatal(err)
	}
	return l
}

func (w *world) liveToken(run string) bool {
	return w.count(`SELECT count(*) FROM run_tokens WHERE run_id = ? AND revoked_at IS NULL`, run) > 0
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
