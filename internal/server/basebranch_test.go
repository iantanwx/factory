package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/room"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

// project add records the default branch it is given, project show names
// it, and secret import reads the manifest from it when no --branch names
// another.
func TestProjectAddRecordsTheDefaultBranch(t *testing.T) {
	dir := instanceDir(t)
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveHost(filepath.Join(dir, config.HostConfigFile), &config.HostConfig{StateDir: dir, Throughput: config.DefaultThroughput()}); err != nil {
		t.Fatal(err)
	}
	var gotBranch string
	start(t, dir, Options{ManifestReader: func(_ context.Context, _ config.Project, branch string) (*config.Manifest, error) {
		gotBranch = branch
		return &config.Manifest{}, nil
	}}, true)
	if env, code, _ := call(t, dir, time.Second, nil, "project", "add", "alpha", "--repo", "iantanwx/alpha", "--path", "/srv/alpha", "--branch", "trunk"); code != 0 {
		t.Fatalf("project add = %d %v", code, env)
	}
	env, code, raw := call(t, dir, time.Second, nil, "project", "show", "alpha")
	if d, _ := env["data"].(map[string]any); code != 0 || d["branch"] != "trunk" {
		t.Fatalf("project show = %d %s", code, raw)
	}
	if env, code, _ := call(t, dir, 5*time.Second, nil, "secret", "import", "--project", "alpha"); code != 0 || gotBranch != "trunk" {
		t.Fatalf("secret import = %d %v; read branch %q, want trunk", code, env, gotBranch)
	}
	if env, code, _ := call(t, dir, time.Second, nil, "project", "move", "alpha", "--repo", "iantanwx/alpha", "--branch", "develop"); code != 0 {
		t.Fatalf("project move = %d %v", code, env)
	}
	env, _, raw = call(t, dir, time.Second, nil, "project", "show", "alpha")
	if d, _ := env["data"].(map[string]any); d["branch"] != "develop" {
		t.Fatalf("project show after move = %s", raw)
	}
}

// Without the GitHub mirror, the base reader and the config reader read
// the registered default branch of the project's own clone.
func TestLocalReadersFollowTheRegisteredBranch(t *testing.T) {
	repo := projectRepo(t, "merge:\n  mechanism: direct\n")
	gitT(t, repo, "update-ref", "refs/remotes/origin/trunk", "HEAD")
	gitT(t, repo, "update-ref", "-d", "refs/remotes/origin/main")
	p := config.Project{ID: "alpha", Repo: "iantanwx/alpha", Path: repo, Branch: "trunk"}

	_, branch, err := LocalRefs(context.Background(), p)
	if err != nil || branch != "trunk" {
		t.Fatalf("LocalRefs branch = %q, %v; want trunk", branch, err)
	}

	hostPath := filepath.Join(t.TempDir(), config.HostConfigFile)
	cfg := config.DefaultHost("/var/lib/loop")
	cfg.Projects = []config.Project{p}
	if err := config.SaveHost(hostPath, cfg); err != nil {
		t.Fatal(err)
	}
	files, err := GitBase{Projects: &config.Projects{HostPath: hostPath}}.Files(context.Background(), "alpha", []string{".loop/"})
	if err != nil || files[".loop/config.yaml"] == "" {
		t.Fatalf("GitBase.Files = %v, %v; want the files of origin/trunk", files, err)
	}
}

// baseRecorder records the base branch of each checkout it creates.
type baseRecorder struct {
	sched.CheckoutHost
	mu    sync.Mutex
	bases map[string]string // checkout name: base branch
}

func (r *baseRecorder) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	r.mu.Lock()
	r.bases[spec.Name] = spec.BaseBranch
	r.mu.Unlock()
	return r.CheckoutHost.CreateCheckout(ctx, spec)
}

// A room starts from the branch its project config was read from, and its
// brief names that branch.
func TestRoomStartsFromTheProjectBranch(t *testing.T) {
	w := newRoomWorld(t)
	snap, err := w.opts.Sched.ProjectConfig(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	trunk := *snap
	trunk.Branch = "trunk"
	w.opts.Sched.ProjectConfig = func(context.Context, string) (*config.ProjectSnapshot, error) { return &trunk, nil }
	inner := w.opts.Sched.Checkouts
	rec := &baseRecorder{bases: map[string]string{}}
	w.opts.Sched.Checkouts = func(p string) sched.CheckoutHost {
		rec.CheckoutHost = inner(p)
		return rec
	}
	w.restart()

	r := w.openRoom("intake", "", "the operator wants widgets")
	if r["state"] != room.Live {
		t.Fatalf("room = %v", r)
	}
	rec.mu.Lock()
	got := rec.bases["loop-"+r["room"].(string)]
	rec.mu.Unlock()
	if got != "trunk" {
		t.Fatalf("room checkout base = %q, want trunk; bases %v", got, rec.bases)
	}
	raw, err := os.ReadFile(filepath.Join(r["checkout"].(string), brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil || b.BaseBranch != "trunk" {
		t.Fatalf("room brief base = %q, %v; want trunk", b.BaseBranch, err)
	}
}
