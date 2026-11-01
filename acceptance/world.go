//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/agenthosttest"
	agentfake "github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/agenthost/orca"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/gitx/gittest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/pipeline"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/server"
)

// Project is the one project every world registers. Repo is the
// synthetic GitHub repository behind it.
const (
	Project = "demo"
	Repo    = gt.Repo
)

// DefaultConfig enables every run stage the server starts an agent for,
// and merges with the direct mechanism: the loop is the queue, and each
// merge goes through GitHub's merge API (KTD29). It turns off the simplify
// stage, which is on by default, so a scenario passes each ticket on to
// review by itself. A scenario that drives a simplify run names its own
// config.
const DefaultConfig = `merge:
  mechanism: direct
stages:
  implementer: {}
  simplify: {enabled: false}
  code-reviewer: {}
  doc-reviewer: {}
  reviser: {}
  planner: {}
  decomposer: {}
  triage: {}
  reproducer: {}
`

// ShutdownTimeout bounds each world server's graceful stop. After it the
// server closes every connection, as production does after its own.
const ShutdownTimeout = time.Second

// Options shape one world.
type Options struct {
	// Config is the project's .loop/config.yaml. DefaultConfig when empty.
	Config string
	// Files are more files on the project's main branch.
	Files map[string]string
	// Pack replaces files of the embedded pack source that each run
	// receives, by their path in the source, such as
	// agents/loop-doc-reader.md.
	Pack map[string]string
	// Real composes the world as production does on an orca host: the real
	// clock, no coordinator, the project clone at Real.Clone. Nil keeps the
	// fake world.
	Real *RealHost
}

// RealHost is the host of a real world.
type RealHost struct {
	Agents    agenthost.Host     // wrapped in Counting with Declared
	Checkouts sched.CheckoutHost // makes the project's checkouts from Clone
	Clone     string             // absolute path of a lane fixture
	Declared  int                // the sessions the test declares in the plan
}

// devboxOrca is the orca CLI on the devbox, as the orca contract names it.
const devboxOrca = "/home/operator/.config/orca/cli/bin/orca"

// OrcaHost builds a RealHost from LOOP_ORCA_*, as cmd/loop's realAgents
// builds the orca host. Clone is the path of LOOP_ORCA_REPO, which must use
// the path: form.
func OrcaHost(declared int) (*RealHost, error) {
	repo := os.Getenv("LOOP_ORCA_REPO")
	clone, ok := strings.CutPrefix(repo, "path:")
	if !ok || !filepath.IsAbs(clone) {
		return nil, fmt.Errorf("LOOP_ORCA_REPO %q is not path:<absolute clone>", repo)
	}
	base := orca.Config{Bin: os.Getenv("LOOP_ORCA_BIN"), CheckoutRoot: os.Getenv("LOOP_ORCA_ROOT")}
	if base.Bin == "" {
		base.Bin = devboxOrca
	}
	agents, err := orca.New(base)
	if err != nil {
		return nil, err
	}
	base.Repo = repo
	checkouts, err := orca.New(base)
	if err != nil {
		return nil, err
	}
	return &RealHost{Agents: agents, Checkouts: checkouts, Clone: clone, Declared: declared}, nil
}

// World is one instance: the real server, real beads, the fake agent host
// and synthetic GitHub. A real world has the host of Options.Real in place
// of the fake.
type World struct {
	T *testing.T
	// Dir is the instance directory. It is also the state directory, so
	// the Dolt socket path stays short.
	Dir string
	// Repo is the operator's local clone of the project.
	Repo string
	// Remote is the bare remote behind synthetic GitHub.
	Remote string

	API  *gt.API
	Git  *gt.GitServer
	Host *agentfake.Host // nil in a real world
	// Agents is the server's agent host: the fake, or the real world's
	// host wrapped in Counting.
	Agents agenthost.Host
	Srv    *server.Server
	GH     *server.GitHub
	MG     *server.Merge

	root    string // the parent of everything the world writes
	hostCwd string // the host's working directory, outside every checkout
	home    string // the HOME of every CLI process
	bin     string // a PATH that holds loop and git only
	bd      string
	dolt    string
	real    *RealHost
	pl      *server.Pipeline
	loop    string // a real world's copy of Binary under Dir, which each token file names
	pack    map[string]string

	mu      sync.Mutex
	run     *running
	logs    *syncBuffer
	hooks   beadsHooks
	onStop  []func()
	stopped bool
}

type running struct {
	cancel context.CancelFunc
	done   chan error
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// New makes a world, starts its server, stores the GitHub App key and
// registers the project through the CLI as the host, and restarts once so
// the coordinator session starts on the registered project. A real world
// starts no coordinator, and it refuses a clone that is not a lane fixture
// before it changes anything.
func New(t *testing.T, o Options) *World {
	t.Helper()
	bd, dolt, ok := BeadsBinaries()
	if !ok {
		t.Skip(SkipMessage)
	}
	if Binary == "" {
		t.Fatal("acceptance: no loop binary; the scenarios' TestMain builds one with BuildBinary")
	}
	if o.Real != nil && !orca.IsLaneFixture(o.Real.Clone) {
		t.Fatalf("acceptance: %s is no lane fixture: it has no .git/%s, so a real world does not rewrite it",
			o.Real.Clone, orca.LaneFixtureMarker)
	}
	root, err := os.MkdirTemp("", "acc")
	if err != nil {
		t.Fatal(err)
	}
	w := &World{T: t, root: root, bd: bd, dolt: dolt, real: o.Real, pack: o.Pack, logs: &syncBuffer{}}
	t.Cleanup(func() {
		w.Stop()
		if t.Failed() {
			t.Logf("server log tail:\n%s", tail(w.logs.String(), 6000))
		}
		_ = os.RemoveAll(root)
	})
	w.Dir = filepath.Join(root, "i")
	w.hostCwd = filepath.Join(root, "host")
	w.home = filepath.Join(root, "home")
	w.bin = filepath.Join(root, "bin")
	for _, d := range []string{w.Dir, w.hostCwd, w.home, w.bin, filepath.Join(root, "co")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("acceptance: git is not installed")
	}
	for name, target := range map[string]string{"loop": Binary, "git": gitPath} {
		if err := os.Symlink(target, filepath.Join(w.bin, name)); err != nil {
			t.Fatal(err)
		}
	}

	w.API = gt.NewAPI(t)
	w.Git = gt.NewGitServer(t, w.API)
	w.API.World.HeadOf = w.Git.RemoteSHA
	w.Remote = w.Git.RemoteDir()
	w.seed(o)

	cfg := config.DefaultHost(w.Dir)
	cfg.Beads = config.BeadsConfig{BD: bd, Dolt: dolt}
	cfg.AgentHost = config.AgentHostConfig{Kind: "fake", CheckoutRoot: filepath.Join(root, "co")}
	if w.real != nil {
		// No checkout root, so the cleanup never sweeps a real root that
		// holds the operator's own worktrees.
		cfg.AgentHost = config.AgentHostConfig{Kind: "orca"}
	}
	cfg.Credentials.GitHubApp = config.GitHubAppCredentials{AppID: gt.AppID, PrivateKeySecret: "GITHUB_APP_KEY"}
	if err := config.SaveHost(filepath.Join(w.Dir, config.HostConfigFile), cfg); err != nil {
		t.Fatal(err)
	}
	// loop install writes the host-admin credential.
	if _, err := rpc.EnsureAdminCredential(w.Dir); err != nil {
		t.Fatal(err)
	}
	if w.real != nil {
		w.Agents = agenthosttest.Counting(t, w.real.Agents, w.real.Declared)
		if q, ok := w.real.Agents.(agenthost.QuotaReader); ok {
			w.Agents = quotaHost{w.Agents, q}
		}
		w.loop = filepath.Join(w.Dir, "acceptance", "loop")
		if err := copyFile(Binary, w.loop); err != nil {
			t.Fatal(err)
		}
	} else {
		w.Host = agentfake.New(filepath.Join(root, "co"), agentfake.WithQuietPeriod(time.Hour))
		w.Agents = w.Host
	}

	w.Start()
	w.Must(w.HostCLI(bytes.NewReader(gt.KeyPEM()), "secret", "set", "GITHUB_APP_KEY", "--project", config.HostScope))
	w.Must(w.HostCLI(nil, "project", "add", Project, "--repo", Repo, "--path", w.Repo))
	if w.real != nil {
		return w
	}
	// The service's next tick starts the coordinator on the new project.
	if err := w.Srv.Sched.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.waitCoordinator()
	return w
}

// seed makes the project's main branch on the remote and the operator's
// clone of it.
func (w *World) seed(o Options) {
	t := w.T
	seed := filepath.Join(w.root, "seed")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgText := o.Config
	if cfgText == "" {
		cfgText = DefaultConfig
	}
	files := map[string]string{"README.md": "A fixture project.\n", config.ProjectConfigPath: cfgText}
	for p, c := range o.Files {
		files[p] = c
	}
	gt.Git(t, seed, "init", "-q", "-b", "main")
	gt.Commit(t, seed, "seed the fixture project", files)
	gt.Git(t, seed, "push", "-q", w.Remote, "main")
	if w.real != nil {
		w.Repo = w.real.Clone
		if err := resetMain(w.Repo, w.Remote); err != nil {
			t.Fatal(err)
		}
		_ = setupGit(w.Repo, "remote", "remove", "origin")
		gt.Git(t, w.Repo, "remote", "add", "origin", w.Git.URL(Repo))
		return
	}
	w.Repo = filepath.Join(w.root, "repo")
	gt.Git(t, w.root, "clone", "-q", w.Remote, w.Repo)
	// The clone's origin is synthetic GitHub, which takes a token the
	// server mints and no agent holds.
	gt.Git(t, w.Repo, "remote", "set-url", "origin", w.Git.URL(Repo))
}

// options composes the server as cmd/loop's productionOptions does, with
// the fakes in place of orca and GitHub's endpoints. Keep the two in step:
// a part production composes and this does not is a part the suite does
// not prove.
func (w *World) options() server.Options {
	t := w.T
	cfg, err := config.LoadHost(filepath.Join(w.Dir, config.HostConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	opts := server.Options{Instance: w.Dir}
	gh := &server.GitHub{API: w.API.URL(), GitURL: w.Git.URL, Protocols: []string{"http"}, GitConfig: gittest.Config,
		Sleep: func(context.Context, time.Duration) error { return nil }}
	mg := &server.Merge{GitHub: gh}
	w.GH, w.MG = gh, mg
	w.pl = &server.Pipeline{GitHub: gh}
	opts.Setup = append(opts.Setup, gh.Setup, mg.Setup, w.pl.Setup)
	opts.Reconcile = append(opts.Reconcile, gh.Reconcile)
	opts.BaseFiles = lazyBase{gh}
	opts.Pulls = lazyPulls{gh}
	opts.ManifestReader = func(ctx context.Context, p config.Project, branch string) (*config.Manifest, error) {
		if gh.Service == nil {
			return nil, errors.New("the GitHub service is not set up")
		}
		return gh.Service.ManifestReader(ctx, p, branch)
	}
	opts.Sched.Refs = func(ctx context.Context, p config.Project) (config.Refs, string, error) {
		if gh.Service == nil {
			return config.Refs{}, "", errors.New("the GitHub service is not set up")
		}
		branch, err := gh.Service.DefaultBranch(ctx, p)
		if err != nil {
			return config.Refs{}, "", err
		}
		return config.Refs{Read: gh.Service.RefReader(p.ID), Tree: gh.Service.TreeReader(p.ID)}, branch, nil
	}
	opts.Sched.BaseHead = func(ctx context.Context, project, branch string) (string, error) {
		if gh.Service == nil {
			return "", errors.New("the GitHub service is not set up")
		}
		return gh.Service.FetchBase(ctx, project, branch)
	}

	resolver, stopBeads, err := w.beads(cfg)
	if err != nil {
		t.Fatalf("beads: %v", err)
	}
	opts.Beads = resolver
	w.onStop = append(w.onStop, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := stopBeads(ctx); err != nil {
			t.Errorf("stop beads: %v", err)
		}
	})
	opts.Agents = w.Agents
	opts.Sched.Checkouts = func(string) sched.CheckoutHost { return checkouts{w} }
	if w.real != nil {
		opts.Sched.Checkouts = func(string) sched.CheckoutHost { return realCheckouts{w} }
		opts.LoopBinary = w.loop
	}
	src, fetched, err := fixtureSkills(w.pack)
	if err != nil {
		t.Fatal(err)
	}
	p, left, err := pack.BriefOf(src, fetched)
	if err != nil || len(left) != 0 {
		t.Fatalf("pack: left out %v, %v", left, err)
	}
	opts.Sched.Pack = p
	if w.real == nil {
		opts.Sched.Coordinator = &sched.CoordinatorConfig{Harness: agenthost.Claude}
		if len(cfg.Projects) > 0 {
			opts.Sched.Coordinator.Project = cfg.Projects[0].ID
		}
	}
	opts.Setup = append(opts.Setup, func(s *server.Server) error {
		if s.Sched == nil {
			return nil
		}
		if gh.Reactor != nil {
			if err := s.Sched.RegisterInstanceJob("github-reactor", gh.Reactor.Tick); err != nil {
				return err
			}
		}
		if mg.HandOff != nil {
			return s.Sched.RegisterJob(merge.JobName, mg.HandOff.TickProject)
		}
		return nil
	})

	// The suite's own seams. Each stage and job runs when a scenario says
	// so: the tick loop runs its first pass at start, which starts the
	// coordinator, and the clock it reads is the agent host's, which moves
	// only when a scenario moves it. A real world reads the wall clock.
	opts.Sched.TickInterval = time.Hour
	if w.Host != nil {
		opts.Sched.Now = w.Host.Clock().Now
	}
	opts.Human.PollInterval = 20 * time.Millisecond
	opts.ShutdownTimeout = ShutdownTimeout
	opts.Logger = slog.New(slog.NewJSONHandler(w.logs, nil))
	opts.Setup = append(opts.Setup, func(s *server.Server) error { w.Srv = s; return nil })
	return opts
}

// fixtureSkills is the fetched third-party set each world's runs receive:
// one skill, impeccable, with an executable file, and the embedded pack
// source with a pin file that pins it and the files of replace in place of
// its own. A world fetches nothing, so no embedded pin could match.
func fixtureSkills(replace map[string]string) (src, fetched fs.FS, err error) {
	set := fstest.MapFS{
		"impeccable/SKILL.md":           {Data: []byte("---\nname: impeccable\ndescription: A fixture.\n---\n\nRun `scripts/impeccable`.\n")},
		"impeccable/scripts/impeccable": {Data: []byte("#!/bin/sh\necho impeccable\n"), Mode: 0o755},
	}
	sub, err := fs.Sub(set, "impeccable")
	if err != nil {
		return nil, nil, err
	}
	sum, err := pack.TreeHash(sub)
	if err != nil {
		return nil, nil, err
	}
	pins := fmt.Sprintf(`{"skills": [{"name": "impeccable", "repo": "acme/impeccable", "commit": %q, "path": "", "sha256": %q}]}`,
		strings.Repeat("a", 40), sum)
	files := fstest.MapFS{pack.ThirdPartyFile: {Data: []byte(pins)}}
	for name, data := range replace {
		files[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return pinned{pack.Source(), files}, set, nil
}

// pinned is a pack source whose files in replace take the place of its
// own.
type pinned struct {
	fs.FS
	replace fstest.MapFS
}

func (p pinned) Open(name string) (fs.File, error) {
	if _, ok := p.replace[name]; ok {
		return p.replace.Open(name)
	}
	return p.FS.Open(name)
}

// lazyBase is admission's base reader over the GitHub service, as in
// cmd/loop.
type lazyBase struct{ gh *server.GitHub }

func (l lazyBase) Files(ctx context.Context, project string, prefixes []string) (map[string]string, error) {
	if l.gh.Service == nil {
		return nil, errors.New("the GitHub service is not set up")
	}
	return l.gh.Service.Files(ctx, project, prefixes)
}

var _ admission.BaseReader = lazyBase{}

// lazyPulls is admission's reader of open pull requests over the GitHub
// service, for the `## Depends on` hold, as in cmd/loop.
type lazyPulls struct{ gh *server.GitHub }

func (l lazyPulls) OpenPulls(ctx context.Context, project string) ([]admission.PullHead, error) {
	if l.gh.Service == nil {
		return nil, errors.New("the GitHub service is not set up")
	}
	prs, err := l.gh.Service.OpenPulls(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]admission.PullHead, len(prs))
	for i, pr := range prs {
		out[i] = admission.PullHead{Number: pr.Number, Branch: pr.Head.Ref}
	}
	return out, nil
}

var _ admission.PullReader = lazyPulls{}

// beads starts the instance's Dolt and resolves the project's workspace on
// first use, as cmd/loop's realBeads does. Each client carries the world's
// hooks.
func (w *World) beads(cfg *config.HostConfig) (machine.BeadsResolver, func(context.Context) error, error) {
	layout := beadsx.Layout{StateDir: cfg.StateDir}
	sup, err := beadsx.NewSupervisor(beadsx.DoltConfig{Binary: cfg.Beads.Dolt, Dir: layout.DoltDir()})
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := sup.Start(ctx); err != nil {
		return nil, nil, err
	}
	var types []string
	for _, t := range machine.Tables() {
		if !beadsx.IsBuiltinType(t.BeadType) {
			types = append(types, t.BeadType)
		}
	}
	var mu sync.Mutex
	clients := map[string]beadsx.Client{}
	resolve := func(project string) (beadsx.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		if c, ok := clients[project]; ok {
			return c, nil
		}
		b, err := beadsx.EnsureWorkspace(context.Background(), beadsx.WorkspaceSpec{
			BD: cfg.Beads.BD, Layout: layout, Project: project, Socket: sup.Socket(),
			Statuses: machine.Statuses(), Types: types,
		})
		if err != nil {
			return nil, err
		}
		c := &hooked{Client: b, w: w}
		clients[project] = c
		return c, nil
	}
	stop := func(ctx context.Context) error {
		pid := sup.PID()
		err := sup.Stop(ctx)
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			err = errors.Join(err, fmt.Errorf("dolt pid %d still runs after Stop", pid))
		}
		return err
	}
	return resolve, stop, nil
}

// ---- Server lifecycle ----

// Start starts the server and waits until its socket serves.
func (w *World) Start() {
	w.T.Helper()
	opts := w.options()
	ready := make(chan struct{})
	opts.Ready = ready
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- server.Run(ctx, opts) }()
	select {
	case <-ready:
	case err := <-r.done:
		w.runStops()
		w.T.Fatalf("the server stopped while starting: %v", err)
	case <-time.After(3 * time.Minute):
		w.T.Fatal("the server did not become ready")
	}
	w.mu.Lock()
	w.run, w.stopped = r, false
	w.mu.Unlock()
}

// Stop stops the server gracefully, then stops Dolt. A second call does
// nothing.
func (w *World) Stop() {
	w.mu.Lock()
	r := w.run
	w.run = nil
	w.mu.Unlock()
	if r == nil {
		return
	}
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil {
			w.T.Logf("server stopped: %v", err)
		}
	case <-time.After(time.Minute):
		w.T.Error("the server did not stop")
	}
	w.runStops()
}

// Cancel starts the server's stop and returns at once. Wait for it with
// Stopped.
func (w *World) Cancel() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.run != nil {
		w.run.cancel()
	}
}

// Stopped waits for a server that Cancel stopped. A hook may call Cancel
// while Stopped waits.
func (w *World) Stopped() {
	w.T.Helper()
	w.mu.Lock()
	r := w.run
	w.mu.Unlock()
	if r == nil {
		return
	}
	select {
	case err := <-r.done:
		if err != nil {
			w.T.Logf("server stopped: %v", err)
		}
	case <-time.After(time.Minute):
		var b bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&b, 1)
		w.T.Fatalf("the server did not stop:\n%s", b.String())
	}
	w.mu.Lock()
	w.run = nil
	w.mu.Unlock()
	w.runStops()
}

func (w *World) runStops() {
	stops := w.onStop
	w.onStop = nil
	for _, f := range stops {
		f()
	}
}

// Restart stops the server and starts another on the same instance.
func (w *World) Restart() {
	w.T.Helper()
	w.Stop()
	w.Start()
}

func (w *World) waitCoordinator() {
	w.T.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for w.Srv.Sched.Coordinator().Session == nil {
		if time.Now().After(deadline) {
			w.T.Fatal("the coordinator session did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.CheckRunbook(w.CoordinatorCheckout())
}

// CoordinatorCheckout is the coordinator session's checkout.
func (w *World) CoordinatorCheckout() string { return w.Srv.Sched.Coordinator().Checkout }

// ---- Checkouts ----

// checkouts makes each fake checkout a git worktree of the operator's
// clone on a fresh main, as the orca host does from the project's
// repository.
type checkouts struct{ w *World }

func (c checkouts) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	co, err := c.w.Host.CreateCheckout(ctx, spec)
	if err != nil {
		return co, err
	}
	if err := setupGit(c.w.Repo, "fetch", "-q", c.w.Remote, "+main:refs/remotes/origin/main"); err != nil {
		return co, err
	}
	return co, setupGit(c.w.Repo, "worktree", "add", "-q", "-b", spec.Name, co.Path, "origin/main")
}

func (c checkouts) RemoveCheckout(ctx context.Context, path string) error {
	if err := c.w.Host.RemoveCheckout(ctx, path); err != nil {
		return err
	}
	return setupGit(c.w.Repo, "worktree", "remove", "--force", path)
}

// realCheckouts resets the clone's main to the synthetic remote's main,
// then has the real host make the checkout from it.
type realCheckouts struct{ w *World }

func (c realCheckouts) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	if err := resetMain(c.w.Repo, c.w.Remote); err != nil {
		return agenthost.Checkout{}, err
	}
	return c.w.real.Checkouts.CreateCheckout(ctx, spec)
}

func (c realCheckouts) RemoveCheckout(ctx context.Context, path string) error {
	return c.w.real.Checkouts.RemoveCheckout(ctx, path)
}

// resetMain points clone's main, and its work tree, at remote's main.
func resetMain(clone, remote string) error {
	if err := setupGit(clone, "fetch", "-q", remote, "main"); err != nil {
		return err
	}
	return setupGit(clone, "checkout", "-q", "-f", "-B", "main", "FETCH_HEAD")
}

// quotaHost keeps the real host's quota reading behind Counting, so the
// chooser reads it as production does.
type quotaHost struct {
	agenthost.Host
	agenthost.QuotaReader
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o755)
}

// GitIn runs git in dir as an agent does, with no credential, and returns
// its output.
func (w *World) GitIn(dir string, args ...string) string {
	w.T.Helper()
	cmd := gittest.Command(args...)
	cmd.Dir, cmd.Env = dir, gittest.Env(gitEnv(dir))
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.T.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupGit runs git as the test's own setup, with no user or system config.
func setupGit(dir string, args ...string) error {
	cmd := gittest.Command(args...)
	cmd.Dir = dir
	cmd.Env = gittest.Env(gitEnv(dir))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

func gitEnv(home string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home,
		"GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=agent@example.invalid", "GIT_COMMITTER_NAME=agent",
		"GIT_COMMITTER_EMAIL=agent@example.invalid", "GIT_TERMINAL_PROMPT=0"}
}

// ---- The CLI ----

// Result is one loop invocation: its envelope and its exit.
type Result struct {
	Args []string
	Env  map[string]any
	Code int
	Raw  string
}

// Data is the envelope's data as an object, or nil.
func (r Result) Data() map[string]any {
	d, _ := r.Env["data"].(map[string]any)
	return d
}

// List is the envelope's data as a list, or nil.
func (r Result) List() []any {
	l, _ := r.Env["data"].([]any)
	return l
}

// Str is one field of the envelope's data as a string.
func (r Result) Str(key string) string {
	s, _ := r.Data()[key].(string)
	return s
}

// RefusalCode is the envelope's refusal code, or "".
func (r Result) RefusalCode() string {
	s, _ := r.Env["code"].(string)
	return s
}

func (r Result) String() string {
	return fmt.Sprintf("loop %s: exit %d %s", strings.Join(r.Args, " "), r.Code, strings.TrimSpace(r.Raw))
}

// Must fails the test unless the call exited 0.
func (w *World) Must(r Result) Result {
	w.T.Helper()
	if r.Code != 0 {
		w.T.Fatalf("%s", r)
	}
	return r
}

// CLIEnv is the whole environment of every CLI process: a PATH that holds
// loop and git only, an empty HOME, and the instance. It inherits nothing,
// so no GitHub, harness or push credential reaches an agent.
func (w *World) CLIEnv() []string {
	return []string{"PATH=" + w.bin, "HOME=" + w.home, cli.InstanceEnv + "=" + w.Dir, cli.WaitBudgetEnv + "=3m",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
}

// Command makes one loop process in dir. A scenario that must signal the
// process starts it itself.
func (w *World) Command(dir string, stdin io.Reader, args ...string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.Command(filepath.Join(w.bin, "loop"), args...)
	cmd.Dir = dir
	cmd.Env = w.CLIEnv()
	cmd.Stdin = stdin
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	return cmd, &out
}

// Parse reads one finished invocation.
func (w *World) Parse(args []string, cmd *exec.Cmd, out *bytes.Buffer, err error) Result {
	w.T.Helper()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		w.T.Fatalf("loop %v: %v", args, err)
	}
	r := Result{Args: args, Code: code, Raw: out.String()}
	if err := json.Unmarshal(out.Bytes(), &r.Env); err != nil {
		w.T.Fatalf("loop %v printed no envelope (exit %d): %v\n%s", args, code, err, out.String())
	}
	return r
}

// In runs loop in dir: a run's or a room's checkout, the coordinator's, or
// the host's working directory.
func (w *World) In(dir string, args ...string) Result {
	w.T.Helper()
	cmd, out := w.Command(dir, nil, args...)
	r := w.Parse(args, cmd, out, cmd.Run())
	w.SyncPRHeads()
	return r
}

// HostCLI runs a host verb from outside every checkout.
func (w *World) HostCLI(stdin io.Reader, args ...string) Result {
	w.T.Helper()
	cmd, out := w.Command(w.hostCwd, stdin, args...)
	return w.Parse(args, cmd, out, cmd.Run())
}

// Coordinator runs loop as the coordinator session.
func (w *World) Coordinator(args ...string) Result {
	w.T.Helper()
	return w.In(w.CoordinatorCheckout(), args...)
}

// ---- Stages, jobs and GitHub ----

// CreateTicket creates a PRD-less ticket through the work-create handler,
// in process, as the coordinator on the operator's request, and returns
// its id. It works in both modes, because a real world has no coordinator
// session to call the CLI.
func (w *World) CreateTicket(title, body string) string {
	w.T.Helper()
	flags := map[string]string{"project": Project, "title": title, "request": "the operator asked for this ticket"}
	if body != "" {
		flags["body"] = body
	}
	inv, bad := cli.Surface().Validate("work create", nil, flags, fmt.Sprintf("create-%d", time.Now().UnixNano()))
	if bad != nil {
		w.T.Fatalf("work create: %+v", bad)
	}
	// The coordinator's run id, when a session holds one, names the actor
	// as a call from its checkout does.
	pr := rpc.Principal{Kind: cli.PrincipalCoordinator, RunID: w.Srv.Sched.Coordinator().RunID, Project: Project}
	call := &rpc.Call{Inv: inv, Principal: pr}
	res := w.pl.Pipeline.Handlers()["work create"](context.Background(), call)
	cr, ok := res.Data.(pipeline.CreateResult)
	if res.Refusal != nil || !ok || cr.Item == "" {
		w.T.Fatalf("work create = %+v", res)
	}
	return cr.Item
}

// Run is one run the scheduler started.
type Run struct {
	*sched.Started
}

// Dispatch starts one run of stage, and fails the test when none starts.
func (w *World) Dispatch(stage string) Run {
	w.T.Helper()
	st, ref := w.Srv.Sched.Dispatch(context.Background(), Project, stage)
	if ref != nil || st == nil {
		w.T.Fatalf("dispatch %s = %+v, %+v", stage, st, ref)
	}
	w.CheckRunbook(st.Checkout)
	return Run{st}
}

// Tick runs one pass of the scheduler's loop, as the service does each
// tick interval: it watches runs, settles ended sessions, keeps the
// coordinator, and fires each stage whose deadline passed. The clock moves
// only when a scenario moves it, so no stage fires unless it does.
func (w *World) Tick() {
	w.T.Helper()
	if err := w.Srv.Sched.Tick(context.Background()); err != nil {
		w.T.Fatalf("tick: %v", err)
	}
}

// ClosePR closes a PR without a merge, as a person does on GitHub.
func (w *World) ClosePR(pr *gt.PR) {
	w.API.World.Update(pr, func(p *gt.PR) { p.State = "closed" })
}

// TryDispatch starts one run of stage, or says why none starts.
func (w *World) TryDispatch(stage string) (*sched.Started, *cli.Refusal) {
	return w.Srv.Sched.Dispatch(context.Background(), Project, stage)
}

// React runs one tick of the GitHub reactor, the github-reactor job.
func (w *World) React() {
	w.T.Helper()
	w.SyncPRHeads()
	if err := w.GH.Reactor.Tick(context.Background()); err != nil {
		w.T.Fatalf("reactor tick: %v", err)
	}
}

// HandOff runs one tick of the merge-handoff job.
func (w *World) HandOff() {
	w.T.Helper()
	w.SyncPRHeads()
	if err := w.MG.HandOff.TickProject(context.Background(), Project); err != nil {
		w.T.Fatalf("merge hand-off tick: %v", err)
	}
}

// SyncPRHeads does what GitHub does on a push: each open PR's head
// follows its branch on the remote.
func (w *World) SyncPRHeads() {
	for _, pr := range w.API.World.PRs() {
		if pr.State != "open" {
			continue
		}
		if sha := w.Git.RemoteSHA(pr.Head); sha != "" && sha != pr.HeadSHA {
			w.API.World.Update(w.API.World.PR(pr.Number), func(p *gt.PR) { p.HeadSHA = sha })
		}
	}
}

// PRFor is the PR on item's branch, or nil.
func (w *World) PRFor(item string) *gt.PR {
	for _, pr := range w.API.World.PRs() {
		if pr.Head == "loop/"+item {
			return w.API.World.PR(pr.Number)
		}
	}
	return nil
}

// CI sets the one CI check of sha: "success" or "failure".
func (w *World) CI(sha, conclusion string) {
	w.API.World.SetRuns(sha, gt.CheckRun("ci", sha, "completed", conclusion, time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)))
}

// MergeByHand merges a PR as a person does on GitHub: the PR reads merged,
// and main moves to a commit that holds the PR's head.
func (w *World) MergeByHand(pr *gt.PR) string {
	w.T.Helper()
	w.PushMain("merge PR "+fmt.Sprint(pr.Number), nil, pr.Head)
	sha := w.Git.RemoteSHA("main")
	w.API.World.Update(pr, func(p *gt.PR) { p.Merged, p.State, p.MergeSHA = true, "closed", sha })
	return sha
}

// PushMain commits files to main on the remote, as another merge does. A
// branch named in merge is merged first.
func (w *World) PushMain(msg string, files map[string]string, mergeBranch string) string {
	w.T.Helper()
	dir := filepath.Join(w.root, "main-"+fmt.Sprint(time.Now().UnixNano()))
	gt.Git(w.T, w.root, "clone", "-q", "-b", "main", w.Remote, dir)
	defer func() { _ = os.RemoveAll(dir) }()
	if mergeBranch != "" {
		gt.Git(w.T, dir, "fetch", "-q", "origin", mergeBranch)
		gt.Git(w.T, dir, "merge", "-q", "--no-ff", "-m", msg, "FETCH_HEAD")
	}
	if len(files) > 0 || mergeBranch == "" {
		gt.Commit(w.T, dir, msg, files)
	}
	gt.Git(w.T, dir, "push", "-q", "origin", "main")
	return gt.Git(w.T, dir, "rev-parse", "HEAD")
}

// PushBranch commits files on top of a remote branch, as a person who
// pushes to a PR does.
func (w *World) PushBranch(branch, msg string, files map[string]string) string {
	w.T.Helper()
	dir := filepath.Join(w.root, "br-"+fmt.Sprint(time.Now().UnixNano()))
	gt.Git(w.T, w.root, "clone", "-q", "-b", branch, w.Remote, dir)
	defer func() { _ = os.RemoveAll(dir) }()
	sha := gt.Commit(w.T, dir, msg, files)
	gt.Git(w.T, dir, "push", "-q", "origin", branch)
	w.SyncPRHeads()
	return sha
}

// Commit writes files in a checkout and commits them as the agent.
func (w *World) Commit(checkout, msg string, files map[string]string) string {
	w.T.Helper()
	for p, c := range files {
		path := filepath.Join(checkout, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			w.T.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(c), 0o644); err != nil {
			w.T.Fatal(err)
		}
		if err := setupGit(checkout, "add", p); err != nil {
			w.T.Fatal(err)
		}
	}
	if err := setupGit(checkout, "commit", "-q", "--allow-empty", "-m", msg); err != nil {
		w.T.Fatal(err)
	}
	cmd := gittest.Command("rev-parse", "HEAD")
	cmd.Dir, cmd.Env = checkout, gittest.Env(gitEnv(checkout))
	out, err := cmd.Output()
	if err != nil {
		w.T.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// ---- Reads ----

// Show reads one item from the server's machine.
func (w *World) Show(item string) machine.Item {
	w.T.Helper()
	it, ref := w.Srv.Machine.Show(context.Background(), Project, item)
	if ref != nil {
		w.T.Fatalf("show %s: %+v", item, ref)
	}
	return it
}

// Status is one item's status.
func (w *World) Status(item string) string { return w.Show(item).Status }

// Count runs one count query on the store.
func (w *World) Count(query string, args ...any) int {
	w.T.Helper()
	var n int
	if err := w.Srv.Store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(query, args...).Scan(&n)
	}); err != nil {
		w.T.Fatalf("%s: %v", query, err)
	}
	return n
}

// DoneWriters counts the transitions that moved item to done.
func (w *World) DoneWriters(item string) int {
	return w.Count(`SELECT count(*) FROM transitions WHERE project = ? AND item = ? AND to_status = 'done'`, Project, item)
}

// Entries lists the coordinator's inbox, all of it or what awaits one actor.
func (w *World) Entries(awaits string) []map[string]any {
	w.T.Helper()
	args := []string{"inbox", "list"}
	if awaits != "" {
		args = append(args, "--awaits", awaits)
	}
	r := w.Coordinator(args...)
	if r.Code == 1 {
		return nil
	}
	if r.Code != 0 {
		w.T.Fatalf("%s", r)
	}
	var out []map[string]any
	for _, e := range r.List() {
		out = append(out, e.(map[string]any))
	}
	return out
}

// Entry is the inbox entry with id, or nil.
func (w *World) Entry(id string) map[string]any {
	for _, e := range w.Entries("") {
		if e["entry"] == id {
			return e
		}
	}
	return nil
}

// RunState is a run's state in the store.
func (w *World) RunState(run string) string {
	w.T.Helper()
	var s string
	if err := w.Srv.Store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state FROM runs WHERE run_id = ?`, run).Scan(&s)
	}); err != nil {
		w.T.Fatalf("run %s: %v", run, err)
	}
	return s
}

// Transition is one applied row of the history.
type Transition struct {
	Kind, Verb, From, To string
}

// Transitions lists every applied transition of the world.
func (w *World) Transitions() []Transition {
	w.T.Helper()
	var out []Transition
	if err := w.Srv.Store.Read(context.Background(), func(tx *sql.Tx) error {
		rs, err := tx.Query(`SELECT kind, verb, from_status, to_status FROM transitions ORDER BY intent_id`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var t Transition
			if err := rs.Scan(&t.Kind, &t.Verb, &t.From, &t.To); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rs.Err()
	}); err != nil {
		w.T.Fatal(err)
	}
	return out
}

// Logs is the server's log so far.
func (w *World) Logs() string { return w.logs.String() }

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// WriteFile writes a file outside every checkout and returns its path.
func (w *World) WriteFile(name, content string) string {
	w.T.Helper()
	path := filepath.Join(w.root, "files", fmt.Sprint(time.Now().UnixNano())+"-"+name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		w.T.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		w.T.Fatal(err)
	}
	return path
}

// ---- Releases ----

// Upgrade does what `loop upgrade` does around the binary swap: the
// running server stops, the instance records the release it replaced, and
// a server of the new release starts. Every later CLI call runs binary,
// whose version is version. The swap itself, its signature check and its
// rollback belong to the release verbs, and cmd/loop's release tests prove
// them.
func (w *World) Upgrade(version, binary string) {
	w.T.Helper()
	prev := cli.Version
	w.Stop()
	if err := os.WriteFile(filepath.Join(w.Dir, server.PreviousVersionFile), []byte(prev+"\n"), 0o600); err != nil {
		w.T.Fatal(err)
	}
	link := filepath.Join(w.bin, "loop")
	if err := os.Remove(link); err != nil {
		w.T.Fatal(err)
	}
	if err := os.Symlink(binary, link); err != nil {
		w.T.Fatal(err)
	}
	cli.Version = version
	w.T.Cleanup(func() { cli.Version = prev })
	w.Start()
}

// InBinary runs one loop binary in dir: a run that started under an
// earlier release keeps that release's binary.
func (w *World) InBinary(binary, dir string, args ...string) Result {
	w.T.Helper()
	cmd, out := w.Command(dir, nil, args...)
	cmd.Path = binary
	cmd.Args[0] = binary
	return w.Parse(args, cmd, out, cmd.Run())
}

// WaitUnreachable waits up to ten seconds until the server's socket
// accepts no connection, and reports whether it got there. A hook may call
// it from the server's own goroutine.
func (w *World) WaitUnreachable() bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", cli.SocketPath(w.Dir))
		if err != nil {
			return true
		}
		_ = c.Close()
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
