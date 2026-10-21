package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	agentfake "github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// projectRepo makes a bare origin whose main holds project config, and a
// clone of it.
func projectRepo(t *testing.T, cfg string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	origin, seed, repo := filepath.Join(dir, "origin.git"), filepath.Join(dir, "seed"), filepath.Join(dir, "alpha")
	gitT(t, dir, "init", "-q", "--bare", "-b", "main", origin)
	gitT(t, dir, "init", "-q", "-b", "main", seed)
	if err := os.MkdirAll(filepath.Join(seed, ".loop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, ".loop", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, seed, "add", "-A")
	gitT(t, seed, "commit", "-q", "-m", "init")
	gitT(t, seed, "push", "-q", origin, "main")
	gitT(t, dir, "clone", "-q", origin, repo)
	return repo
}

// worktrees makes each fake checkout a git worktree of repo.
type worktrees struct {
	t    *testing.T
	host *agentfake.Host
	repo string
}

func (w worktrees) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	co, err := w.host.CreateCheckout(ctx, spec)
	if err == nil {
		gitT(w.t, w.repo, "worktree", "add", "-q", "-b", spec.Name, co.Path, "origin/main")
	}
	return co, err
}

func (w worktrees) RemoveCheckout(ctx context.Context, path string) error {
	return w.host.RemoveCheckout(ctx, path)
}

// schedWorld is a server with fake beads and a fake agent host, and one
// project whose checkouts are git worktrees.
type schedWorld struct {
	dir  string
	srv  *Server
	bx   *fake.Client
	host *agentfake.Host
}

func newSchedWorld(t *testing.T, opts Options) *schedWorld {
	t.Helper()
	repo := projectRepo(t, "stages:\n  code-reviewer: {}\nmerge:\n  mechanism: direct\n")
	w := &schedWorld{dir: instanceDir(t), bx: fake.New("tk"), host: agentfake.New(filepath.Join(t.TempDir(), "checkouts"))}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(w.dir), "checkouts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	opts.Beads = func(string) (beadsx.Client, error) { return w.bx, nil }
	opts.Agents = w.host
	opts.Sched.Checkouts = func(string) sched.CheckoutHost { return worktrees{t: t, host: w.host, repo: repo} }
	opts.Sched.TickInterval = time.Hour
	opts.Setup = append(opts.Setup, func(s *Server) error { w.srv = s; return nil })
	start(t, w.dir, opts, true)
	if err := w.srv.Projects.Add(context.Background(), config.Project{ID: "alpha", Repo: "iantanwx/alpha", Path: repo}); err != nil {
		t.Fatal(err)
	}
	return w
}

// A run finishes through the CLI from its own checkout. The call is the
// run's last CLI call, and the finish settles the run and frees its lease.
// drain from the host then reports nothing in flight.
func TestRunFinishOverTheSocket(t *testing.T) {
	w := newSchedWorld(t, Options{})
	if w.srv.Sched == nil {
		t.Fatal("the server composed no scheduler")
	}
	id := todoTicket(t, w.bx)
	if _, err := w.bx.Update(context.Background(), id, beadsx.UpdateSpec{IfStatus: machine.Todo, Status: machine.InReview}); err != nil {
		t.Fatal(err)
	}
	st, ref := w.srv.Sched.Dispatch(context.Background(), "alpha", machine.StageCodeReviewer)
	if ref != nil || st == nil {
		t.Fatalf("dispatch = %+v, %+v", st, ref)
	}
	t.Chdir(st.Checkout)
	r := cli.Surface()
	(&cli.Client{Instance: w.dir, Budget: 5 * time.Second, Getwd: func() (string, error) { return st.Checkout, nil }}).Attach(r)
	var out bytes.Buffer
	r.Run(context.Background(), []string{"run", "finish", "--outcome", "done"}, &out, io.Discard)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("no envelope: %s", out.String())
	}
	data, _ := env["data"].(map[string]any)
	if env["exit"] != float64(0) || data["class"] != "done" || data["released"] != true {
		t.Fatalf("run finish = %s", out.String())
	}
	var class string
	var last sql.NullInt64
	if err := w.srv.Store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT outcome_class, last_cli_call_at FROM runs WHERE run_id = ?`, st.RunID).Scan(&class, &last)
	}); err != nil {
		t.Fatal(err)
	}
	if class != "done" || !last.Valid {
		t.Fatalf("class %s, last CLI call %v", class, last)
	}
	l, _ := w.srv.Machine.Lease(context.Background(), "alpha", id)
	if l.Owner != "" {
		t.Fatalf("lease = %+v", l)
	}
	t.Chdir(t.TempDir())
	if _, err := rpc.EnsureAdminCredential(w.dir); err != nil {
		t.Fatal(err)
	}
	denv, code, raw := call(t, w.dir, 5*time.Second, nil, "drain")
	dd, _ := denv["data"].(map[string]any)
	if code != 0 || dd["draining"] != true || dd["drained"] != true || dd["done"] != true {
		t.Fatalf("drain = %d %s", code, raw)
	}
	if _, ref := w.srv.Sched.Dispatch(context.Background(), "alpha", machine.StageCodeReviewer); ref == nil || ref.Code != cli.CodeDraining {
		t.Fatalf("dispatch after drain = %+v", ref)
	}
}

// LocalRefs reads committed files at origin/main and reports a missing
// file as fs.ErrNotExist, which the config loader needs.
func TestLocalRefsReadsTheDefaultBranch(t *testing.T) {
	repo := projectRepo(t, "merge:\n  mechanism: direct\n")
	read, branch, err := LocalRefs(context.Background(), config.Project{ID: "alpha", Path: repo})
	if err != nil || branch != "main" {
		t.Fatal(branch, err)
	}
	data, err := read(context.Background(), config.DefaultRef(branch), ".loop/config.yaml")
	if err != nil || !strings.Contains(string(data), "direct") {
		t.Fatalf("read = %q, %v", data, err)
	}
	snap, err := config.ReadProjectSnapshot(context.Background(), read, branch)
	if err != nil || len(snap.Manifest.Secrets) != 0 {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
}
