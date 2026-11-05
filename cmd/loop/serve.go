package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/orca"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/server"
	"github.com/iantanwx/loop/internal/server/orcalane"
	"github.com/iantanwx/loop/internal/userspace"
)

// serveVerb runs the server until SIGTERM or SIGINT, then stops it
// gracefully. It prints its one envelope when the server has stopped.
func serveVerb(ctx context.Context, inv cli.Invocation) cli.Result {
	dir := inv.Flags["instance"]
	if dir == "" {
		var err error
		if dir, err = cli.InstanceDir(); err != nil {
			return cli.Reject(cli.CodeInternal, "instance_dir", err.Error())
		}
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	opts, cleanup, err := productionOptions(ctx, dir, realWiring)
	if err != nil {
		return cli.Reject(cli.CodeInternal, "serve_failed", err.Error())
	}
	defer cleanup()
	err = server.Run(ctx, opts)
	switch {
	case errors.Is(err, rpc.ErrInstanceLocked), errors.Is(err, rpc.ErrServerRunning):
		return cli.Reject(cli.CodeInternal, "instance_locked", err.Error())
	case err != nil:
		return cli.Reject(cli.CodeInternal, "serve_failed", err.Error())
	}
	return cli.Found(map[string]any{"instance": dir, "stopped": true})
}

// wiring builds the parts that reach outside the process. Production uses
// realWiring. A test passes fakes, so no default test starts Dolt, orca
// or an agent, or calls GitHub.
type wiring struct {
	// beads starts the record of work: a resolver of each project's
	// client, and the step that stops it.
	beads func(ctx context.Context, cfg *config.HostConfig) (machine.BeadsResolver, func(context.Context) error, error)
	// agents makes the agent host and each project's checkout host.
	agents func(cfg *config.HostConfig, projects *config.Projects) (agenthost.Host, func(project string) sched.CheckoutHost, error)
	// github overrides GitHub's endpoints. Production leaves it nil.
	github *server.GitHub
	// pack builds the pack from the fetched third-party skills. nil is
	// pack.Brief, whose pins the binary embeds.
	pack func(fetched fs.FS) (brief.Pack, []pack.ThirdParty, error)
}

var realWiring = wiring{beads: realBeads, agents: realAgents}

// fetchedSkills is the third-party skill set of the release this binary
// is, which loop install and loop upgrade place: versions/<v>/skills.
func fetchedSkills(instance string) fs.FS {
	return os.DirFS(release.Layout{Dir: instance}.Skills(cli.Version))
}

// productionOptions composes the server from host config:
//
//   - with beads.bd and beads.dolt set, the Dolt supervisor and one beads
//     workspace for each project (KTD3), so the machine and admission run;
//   - with agent_host.kind set too, the agent host and the scheduler,
//     and a coordinator session for each project whose default branch
//     holds a project config;
//   - with credentials.github_app.app_id set, the GitHub actor and reactor:
//     its verbs, its start-up reconcile, the base reader of
//     admission, the manifest reader of secret import, the project config
//     source, and the reactor as the github-reactor native job; and the
//     merge interface after it, with its hand-off as the
//     merge-handoff native job; and the document pipeline with
//     prd submit, epic decompose and work create;
//   - with beads set, the overwatch part server's checks: the install drift
//     check and the GitHub actor.
//
// A host config that names none of them serves only the verbs of the core
// server. The returned cleanup stops what it started.
func productionOptions(ctx context.Context, dir string, w wiring) (server.Options, func(), error) {
	opts := server.Options{Instance: dir, Upgrade: newUpgradeStarter(dir).Start}
	// Each run's CLI execs the release the service runs. A server outside
	// a release, such as a dev build, names no binary.
	bin := filepath.Join(dir, release.CurrentLink, release.BinaryName)
	if _, err := os.Stat(bin); err == nil {
		opts.LoopBinary = bin
	}
	cleanup := func() {}
	cfg, err := config.LoadHost(filepath.Join(dir, config.HostConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return opts, cleanup, nil // the first start writes the default host config
	}
	if err != nil {
		return opts, cleanup, err
	}
	projects := &config.Projects{HostPath: filepath.Join(dir, config.HostConfigFile)}

	var gh *server.GitHub
	var mg *server.Merge
	if len(cfg.Credentials.Apps()) > 0 {
		gh = &server.GitHub{}
		if w.github != nil {
			gh = w.github
		}
		// The merge and pipeline setups use the GitHub service, so they
		// run after it.
		mg = &server.Merge{GitHub: gh}
		opts.Setup = append(opts.Setup, gh.Setup, mg.Setup, (&server.Pipeline{GitHub: gh}).Setup)
		opts.Reconcile = append(opts.Reconcile, gh.Reconcile)
		// Admission needs its base reader when the server composes, before
		// Setup builds the service. The wrapper reads through the service.
		opts.BaseFiles = lazyBase{gh}
		opts.Pulls = lazyPulls{gh}
		opts.ManifestReader = func(ctx context.Context, p config.Project, branch string) (*config.Manifest, error) {
			if gh.Service == nil {
				return nil, errNoGitHub
			}
			return gh.Service.ManifestReader(ctx, p, branch)
		}
		opts.Sched.Refs = func(ctx context.Context, p config.Project) (config.Refs, string, error) {
			if gh.Service == nil {
				return config.Refs{}, "", errNoGitHub
			}
			branch, err := gh.Service.DefaultBranch(ctx, p)
			if err != nil {
				return config.Refs{}, "", err
			}
			return config.Refs{Read: gh.Service.RefReader(p.ID), Tree: gh.Service.TreeReader(p.ID)}, branch, nil
		}
		// Each checkout starts at the base head the mirror reads with the
		// app token. The host clone's own origin may need a person's
		// credential.
		opts.Sched.BaseHead = func(ctx context.Context, project, branch string) (string, error) {
			if gh.Service == nil {
				return "", errNoGitHub
			}
			return gh.Service.FetchBase(ctx, project, branch)
		}
	}

	if cfg.Beads.BD == "" || cfg.Beads.Dolt == "" {
		return opts, cleanup, nil
	}
	resolver, stopBeads, err := w.beads(ctx, cfg)
	if err != nil {
		return opts, cleanup, fmt.Errorf("beads: %w", err)
	}
	opts.Beads = resolver
	// The overwatch part server reads the host lines of loop doctor, then
	// the GitHub doctor lines (ADR 0009).
	// With no home, orca is looked up on the path alone.
	home, _ := os.UserHomeDir()
	opts.Setup = append(opts.Setup, func(s *server.Server) error {
		s.SetOverwatchDoctor(hostChecks(dir, cfg, home), gh)
		return nil
	})
	cleanup = func() {
		// SIGTERM first. A Dolt that has not exited when the bound ends
		// gets SIGKILL, so serve always returns.
		sctx, cancel := context.WithTimeout(context.Background(), beadsStopTimeout)
		defer cancel()
		if err := stopBeads(sctx); err != nil {
			fmt.Fprintln(os.Stderr, "loop serve: stop beads:", err)
		}
	}
	if cfg.AgentHost.Kind == "" {
		return opts, cleanup, nil
	}
	agents, checkouts, err := w.agents(cfg, projects)
	if err != nil {
		cleanup()
		return opts, func() {}, fmt.Errorf("agent host: %w", err)
	}
	opts.Agents, opts.Sched.Checkouts = agents, checkouts
	build := w.pack
	if build == nil {
		build = pack.Brief
	}
	p, left, err := build(fetchedSkills(dir))
	if err != nil {
		cleanup()
		return opts, func() {}, fmt.Errorf("pack: %w", err)
	}
	opts.Sched.Pack = p
	opts.Setup = append(opts.Setup, skillsAlert(ctx, left))
	opts.Sched.SkillStore = userspace.Store{Dir: filepath.Join(cfg.StateDir, userSkillsDir)}
	opts.Sched.FetchSkill = fetchSkill(release.SkillFetcher{})
	opts.Sched.Coordinator = &sched.CoordinatorConfig{Harness: agenthost.Claude}
	if cfg.AgentHost.Kind == "orca" {
		opts.Setup = append(opts.Setup, orcaLane(dir, cfg))
	}
	if gh != nil {
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
	}
	return opts, cleanup, nil
}

// orcaLane registers the orca-acceptance instance job, which runs the
// installed release's orca lane against this host's orca (ADR 0008).
func orcaLane(dir string, cfg *config.HostConfig) func(*server.Server) error {
	return func(s *server.Server) error {
		if s.Sched == nil {
			return nil
		}
		keys, err := release.TrustedKeys()
		if err != nil {
			return err
		}
		layout := release.Layout{Dir: dir}
		job := &orcalane.Job{
			Layout:  layout,
			Host:    func(context.Context) (*config.HostConfig, error) { return config.LoadHost(layout.HostConfig()) },
			Runner:  orcalane.ExecRunner{Dir: dir},
			Orca:    orcalane.OrcaCLI{Runner: orca.ExecRunner{Bin: cfg.AgentHost.Binary}},
			Fetcher: orcalane.ReleaseFetcher{Keys: keys},
			Faults:  s.Faults,
			Now:     time.Now,
		}
		return s.Sched.RegisterInstanceJob(orcalane.JobName, job.Tick)
	}
}

// userSkillsDir is the store of userspace skill pins under the state
// directory (ADR 0013 decision 2).
const userSkillsDir = "userspace-skills"

// fetchSkill fetches one userspace pin with f, which checks its hash and
// its name as it checks a kernel pin.
func fetchSkill(f release.SkillFetcher) userspace.Fetch {
	return func(ctx context.Context, pin config.SkillPin) (fs.FS, error) {
		trees, err := f.Fetch(ctx, []pack.ThirdParty{pin})
		if err != nil {
			return nil, err
		}
		return trees[pin.Name], nil
	}
}

// skillsAlert raises one alert that names each pinned kernel skill the
// pack left out, so the server serves without them and says so. With none
// left out, it closes the alert. The server never fetches a kernel skill:
// loop install does (ADR 0007, decision 7). It fetches userspace pins
// (ADR 0013), and their alert is project_skills_missing.
func skillsAlert(ctx context.Context, left []pack.ThirdParty) func(*server.Server) error {
	return func(s *server.Server) error {
		alerts := config.StoreAlerter(s.Store)
		if len(left) == 0 {
			return alerts.Clear(ctx, config.AlertThirdPartySkills)
		}
		names := make([]string, len(left))
		for i, p := range left {
			names[i] = p.Name
		}
		return alerts.Raise(ctx, config.Alert{
			Kind: config.AlertThirdPartySkills, DedupeKey: config.AlertThirdPartySkills,
			Subject: fmt.Sprintf("runs start without the pinned skills %s: each is missing or fails its hash",
				strings.Join(names, ", ")),
			Body: "loop install fetches each pinned skill of this release, checks its hash, and restarts the service.",
		})
	}
}

// lazyBase is admission's base reader over the GitHub service, which
// Setup builds after admission is composed.
// errNoGitHub is the answer of a GitHub read before Setup built the service.
var errNoGitHub = errors.New("the GitHub service is not set up")

type lazyBase struct{ gh *server.GitHub }

func (l lazyBase) Files(ctx context.Context, project string, prefixes []string) (map[string]string, error) {
	if l.gh.Service == nil {
		return nil, errNoGitHub
	}
	return l.gh.Service.Files(ctx, project, prefixes)
}

// lazyPulls is the reader of open pull requests over the GitHub service,
// for admission's `## Depends on` hold and for dependency-advance.
type lazyPulls struct{ gh *server.GitHub }

func (l lazyPulls) OpenPulls(ctx context.Context, project string) ([]admission.PullHead, error) {
	if l.gh.Service == nil {
		return nil, errNoGitHub
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

var _ admission.BaseReader = lazyBase{}

// beadsStopTimeout bounds the wait for Dolt to exit on SIGTERM at shutdown.
const beadsStopTimeout = 15 * time.Second

// realBeads starts the instance's Dolt sql-server and resolves each
// project's workspace on its first use (KTD3).
func realBeads(ctx context.Context, cfg *config.HostConfig) (machine.BeadsResolver, func(context.Context) error, error) {
	layout := beadsx.Layout{StateDir: cfg.StateDir}
	sup, err := beadsx.NewSupervisor(beadsx.DoltConfig{Binary: cfg.Beads.Dolt, Dir: layout.DoltDir()})
	if err != nil {
		return nil, nil, err
	}
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
		clients[project] = b
		return b, nil
	}
	return resolve, sup.Stop, nil
}

// realAgents makes the orca agent host. A checkout needs its project's
// repository, so each project gets its own orca host for checkouts.
func realAgents(cfg *config.HostConfig, projects *config.Projects) (agenthost.Host, func(string) sched.CheckoutHost, error) {
	if cfg.AgentHost.Kind != "orca" {
		return nil, nil, fmt.Errorf("agent_host.kind %q is not supported; v1 drives orca", cfg.AgentHost.Kind)
	}
	base := orca.Config{Bin: cfg.AgentHost.Binary, CheckoutRoot: cfg.AgentHost.CheckoutRoot, QuietPeriod: cfg.QuietPeriod}
	host, err := orca.New(base)
	if err != nil {
		return nil, nil, err
	}
	var mu sync.Mutex
	per := map[string]*orca.Host{}
	checkouts := func(project string) sched.CheckoutHost {
		mu.Lock()
		defer mu.Unlock()
		if h, ok := per[project]; ok {
			return h
		}
		p, err := projects.Show(project)
		if err != nil {
			return host
		}
		c := base
		c.Repo = "path:" + p.Path
		h, err := orca.New(c)
		if err != nil {
			return host
		}
		per[project] = h
		return h
	}
	return host, checkouts, nil
}

// installCheck is the install drift line of `loop doctor` for the
// instance, which the overwatch part server reads (ADR 0009). The server
// process runs it: the check reads files only, so it needs no terminal and
// no user service bus. A check that cannot run is a failed line.
func installCheck(dir string) func(context.Context) github.CheckLine {
	return func(ctx context.Context) github.CheckLine {
		line := github.CheckLine{Check: "install"}
		in, err := checkInstaller(ctx, dir)
		var rep release.InstallReport
		if err == nil {
			rep, err = in.Check(ctx)
		}
		switch {
		case err != nil:
			line.Detail = fmt.Sprintf("the install check could not run: %v", err)
		case len(rep.Drift) > 0:
			line.Detail = "run loop install: " + strings.Join(rep.Drift, "; ")
		default:
			line.OK, line.Detail = true, "the service, binaries, host config, key file and credential match loop install"
		}
		return line
	}
}

// hostChecks is the host lines of loop doctor that the server process
// can judge: the install line, then one line for each requirement that the
// server runs. The service's own path finds each binary, because the
// server runs them from there.
func hostChecks(dir string, cfg *config.HostConfig, home string) func(context.Context) []github.CheckLine {
	install := installCheck(dir)
	return func(ctx context.Context) []github.CheckLine {
		return append([]github.CheckLine{install(ctx)},
			release.RequirementLines(release.ServerRequirements(runtime.GOOS), release.HostFinder(home, cfg))...)
	}
}

// checkInstaller is the installer of the instance, with only what Check
// reads: the layout, the service, this release, its pins and its skills.
func checkInstaller(ctx context.Context, dir string) (*release.Installer, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	svc, err := release.HostServiceManager(dir)
	if err != nil {
		return nil, err
	}
	meta, err := release.ThisRelease(ctx)
	if err != nil {
		return nil, err
	}
	pins, err := release.PinsFor(release.Pins(), runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	return &release.Installer{Layout: release.Layout{Dir: dir}, Service: svc, Meta: meta, Pins: pins, Skills: pack.ThirdPartyPins()}, nil
}
