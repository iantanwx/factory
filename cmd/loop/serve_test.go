package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	agentfake "github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	beadsfake "github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/server"
)

// A host config with no beads, agent host or App serves the plain verbs:
// production wiring adds nothing.
func TestProductionOptionsPlain(t *testing.T) {
	dir := t.TempDir()
	opts, cleanup, err := productionOptions(context.Background(), dir, wiring{})
	defer cleanup()
	if err != nil || opts.Beads != nil || opts.Agents != nil || len(opts.Setup) != 0 || opts.BaseFiles != nil {
		t.Fatalf("no host config: %+v, %v", opts, err)
	}
	if err := config.SaveHost(filepath.Join(dir, config.HostConfigFile), config.DefaultHost(dir)); err != nil {
		t.Fatal(err)
	}
	opts, cleanup, err = productionOptions(context.Background(), dir, wiring{})
	defer cleanup()
	if err != nil || opts.Beads != nil || opts.Agents != nil || len(opts.Setup) != 0 {
		t.Fatalf("default host config: %+v, %v", opts, err)
	}
}

// A smoke test of the production wiring with fakes in place of Dolt and
// bd, orca and GitHub: the server composes the machine, admission, the
// scheduler, the GitHub actor and the reactor job, starts and stops.
func TestProductionWiringWithFakes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "checkouts")
	cfg := config.DefaultHost(dir)
	cfg.Beads = config.BeadsConfig{BD: "/opt/loop/bd", Dolt: "/opt/loop/dolt"}
	cfg.AgentHost = config.AgentHostConfig{Kind: "orca", Binary: "/opt/orca", CheckoutRoot: root}
	cfg.Credentials.GitHubApp = config.GitHubAppCredentials{AppID: gt.AppID, PrivateKeySecret: "GITHUB_APP_KEY"}
	if err := config.SaveHost(filepath.Join(dir, config.HostConfigFile), cfg); err != nil {
		t.Fatal(err)
	}
	api := gt.NewAPI(t)
	git := gt.NewGitServer(t, api)
	bx := beadsfake.New("tk")
	if err := bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	host := agentfake.New(root)
	stopped := false
	w := wiring{
		beads: func(context.Context, *config.HostConfig) (machine.BeadsResolver, func(context.Context) error, error) {
			return func(string) (beadsx.Client, error) { return bx, nil }, func(context.Context) error { stopped = true; return nil }, nil
		},
		agents: func(c *config.HostConfig, _ *config.Projects) (agenthost.Host, func(string) sched.CheckoutHost, error) {
			if c.AgentHost.CheckoutRoot != root {
				t.Errorf("checkout root = %q", c.AgentHost.CheckoutRoot)
			}
			return host, func(string) sched.CheckoutHost { return host }, nil
		},
		github: &server.GitHub{API: api.URL(), GitURL: git.URL, Protocols: []string{"http"},
			Sleep: func(context.Context, time.Duration) error { return nil }},
	}
	opts, cleanup, err := productionOptions(context.Background(), dir, w)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := opts.BaseFiles.(lazyBase); !ok || opts.ManifestReader == nil || opts.Sched.Refs == nil {
		t.Fatalf("the GitHub readers are not wired: %+v", opts)
	}
	var srv *server.Server
	opts.Setup = append(opts.Setup, func(s *server.Server) error { srv = s; return nil })
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	ready := make(chan struct{})
	opts.Ready = ready
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, opts) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("serve stopped: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not become ready")
	}
	if srv.Machine == nil || srv.Admission == nil || srv.Sched == nil {
		t.Fatalf("composed: machine %v, admission %v, scheduler %v", srv.Machine != nil, srv.Admission != nil, srv.Sched != nil)
	}
	if w.github.Service == nil || w.github.Reactor == nil {
		t.Fatal("the GitHub actor is not set up")
	}
	err = srv.Sched.RegisterInstanceJob("github-reactor", func(context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("the reactor job is not registered: %v", err)
	}
	err = srv.Sched.RegisterJob(merge.JobName, func(context.Context, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("the merge hand-off job is not registered: %v", err)
	}
	for _, verb := range []string{"prd submit", "epic decompose", "work create"} {
		if err := srv.RPC.Handle(verb, nil); err == nil || !strings.Contains(err.Error(), "already has a handler") {
			t.Fatalf("the pipeline verb %s is not registered: %v", verb, err)
		}
	}
	if _, err := (lazyBase{w.github}).Files(context.Background(), "alpha", nil); err == nil {
		t.Fatal("the base reader read a project that is not registered")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cleanup()
	if !stopped {
		t.Fatal("cleanup did not stop beads")
	}
}

// The binary runs `escalation judge` in the caller's own process (KTD22).
// With no rubric it refuses before any judge or Jev is asked.
func TestEscalationJudgeRunsInTheCallersProcess(t *testing.T) {
	t.Chdir(t.TempDir())
	var out strings.Builder
	code := run(context.Background(), []string{"escalation", "judge", "--finding", "f"}, strings.NewReader(""), &out, io.Discard)
	if code != 2 || !strings.Contains(out.String(), `"no_rubric"`) {
		t.Fatalf("judge = %d %s", code, out.String())
	}
}
