package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	agentfake "github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	beadsfake "github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/overwatch"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/server"
	"github.com/iantanwx/loop/internal/server/orcalane"
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
			stop := func(ctx context.Context) error {
				// A stop with no deadline waits forever on a Dolt that
				// ignores SIGTERM.
				_, stopped = ctx.Deadline()
				return nil
			}
			return func(string) (beadsx.Client, error) { return bx, nil }, stop, nil
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
	if _, ok := opts.Pulls.(lazyPulls); !ok {
		t.Fatalf("the open pull requests of the Depends on hold are not wired: %+v", opts.Pulls)
	}
	if _, err := opts.Pulls.OpenPulls(context.Background(), "alpha"); !errors.Is(err, errNoGitHub) {
		t.Fatalf("an open pull request read before setup = %v", err)
	}
	if _, ok := opts.BaseFiles.(lazyBase); !ok || opts.ManifestReader == nil || opts.Sched.Refs == nil {
		t.Fatalf("the GitHub readers are not wired: %+v", opts)
	}
	// C-bug-1: checkouts start at the base head the mirror reads.
	if opts.Sched.BaseHead == nil {
		t.Fatal("the base head source is not wired")
	}
	if _, err := opts.Sched.BaseHead(context.Background(), "alpha", "main"); !errors.Is(err, errNoGitHub) {
		t.Fatalf("a base head read before setup = %v", err)
	}
	if opts.Sched.Pack.FS == nil || opts.Sched.Pack.Version == "" {
		t.Fatal("the skill pack is not wired")
	}
	var srv *server.Server
	opts.Setup = append(opts.Setup, func(s *server.Server) error { srv = s; return nil })
	// The install check reads the service unit under HOME.
	t.Setenv("HOME", t.TempDir())
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		t.Fatal(err)
	}
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
	if _, err := opts.Pulls.OpenPulls(context.Background(), "alpha"); err == nil || errors.Is(err, errNoGitHub) {
		t.Fatalf("the open pull request reader did not read through the service: %v", err)
	}
	if _, err := (lazyBase{w.github}).Files(context.Background(), "alpha", nil); err == nil {
		t.Fatal("the base reader read a project that is not registered")
	}
	if line := sweepPart(t, dir, "server")[0]; !strings.HasPrefix(line, "install failed: run loop install: ") {
		t.Fatalf("the overwatch server part does not read the install check: %q", line)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cleanup()
	if !stopped {
		t.Fatal("cleanup did not stop beads with a bounded context")
	}
}

// The binary runs `escalation judge` in the caller's own process (KTD22).
// With no rubric it refuses before any judge or Jev is asked.
func TestEscalationJudgeRunsInTheCallersProcess(t *testing.T) {
	t.Chdir(t.TempDir())
	var out strings.Builder
	code := run(context.Background(), []string{"escalation", "judge", "4", "--finding", "f"}, strings.NewReader(""), &out, io.Discard)
	if code != 2 || !strings.Contains(out.String(), `"no_rubric"`) {
		t.Fatalf("judge = %d %s", code, out.String())
	}
}

// The binary runs `review proof` in the reviewer's own checkout. Outside a
// git checkout it refuses before it makes any worktree.
func TestReviewProofRunsInTheCallersCheckout(t *testing.T) {
	t.Chdir(t.TempDir())
	var out strings.Builder
	code := run(context.Background(), []string{"review", "proof", "--test", "a_test.go"}, strings.NewReader(""), &out, io.Discard)
	if code != 2 || !strings.Contains(out.String(), `"not_a_checkout"`) {
		t.Fatalf("review proof = %d %s", code, out.String())
	}
}

// sweepPart runs `loop overwatch sweep` as the host, and returns the lines
// of one part.
func sweepPart(t *testing.T, dir, part string) []string {
	t.Helper()
	r := cli.Surface()
	cwd := t.TempDir()
	c := &cli.Client{Instance: dir, Budget: 10 * time.Second, Getwd: func() (string, error) { return cwd, nil }}
	c.Attach(r)
	v, ok := r.Verb("overwatch sweep")
	if !ok {
		t.Fatal("no overwatch sweep verb")
	}
	res := c.Remote(context.Background(), cli.Invocation{Verb: v, Flags: map[string]string{}, RequestID: newRequestID()})
	if res.Refusal != nil {
		t.Fatalf("overwatch sweep = %+v", res.Refusal)
	}
	var rep overwatch.Report
	raw, err := json.Marshal(res.Data)
	if err == nil {
		err = json.Unmarshal(raw, &rep)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rep.Parts {
		if p.Name == part && len(p.Lines) > 0 {
			return p.Lines
		}
	}
	t.Fatalf("the sweep holds no part %s with lines: %+v", part, rep)
	return nil
}

// The spec assumes the service process can run release.Installer.Check
// with no terminal and no user service bus. The check runs here with
// neither, on an instance that loop install never set up, and it reports
// drift: it ran. Its line is the install line of loop doctor.
func TestInstallCheckRunsWithNoTerminalOrUserBus(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"TERM", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	line := installCheck(dir)(ctx)
	if line.Check != "install" || line.OK || !strings.HasPrefix(line.Detail, "run loop install: ") ||
		!strings.Contains(line.Detail, "is not installed") {
		t.Fatalf("install check = %+v", line)
	}
	in, err := checkInstaller(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	doctor := (&release.Doctor{Layout: in.Layout, Installer: in}).Run(ctx)
	if len(doctor) == 0 || doctor[0] != line {
		t.Fatalf("loop doctor's install line = %+v, the server's = %+v", doctor, line)
	}
}

// writeSkill writes a fetched skill under dir/name, as install places it:
// a SKILL.md that names it, one executable file and one other file.
func writeSkill(t *testing.T, dir, name string) {
	t.Helper()
	files := map[string]struct {
		body string
		mode os.FileMode
	}{
		"SKILL.md":         {"---\nname: " + name + "\ndescription: A fixture.\n---\n\nRun `scripts/" + name + "`.\n", 0o644},
		"scripts/" + name:  {"#!/bin/sh\necho " + name + "\n", 0o755},
		"reference/one.md": {"# One\n", 0o644},
	}
	for rel, f := range files {
		path := filepath.Join(dir, name, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(f.body), f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, f.mode); err != nil {
			t.Fatal(err)
		}
	}
}

// pinnedSource is a pack source with one runbook and one pin for each
// name, at the tree hash of that name's directory under skills now.
func pinnedSource(t *testing.T, skills string, names ...string) fstest.MapFS {
	t.Helper()
	var pins []string
	for _, n := range names {
		sum, err := pack.TreeHash(os.DirFS(filepath.Join(skills, n)))
		if err != nil {
			t.Fatal(err)
		}
		pins = append(pins, fmt.Sprintf(`{"name": %q, "repo": "acme/%s", "commit": %q, "path": "", "sha256": %q}`,
			n, n, strings.Repeat("a", 40), sum))
	}
	return fstest.MapFS{
		"manifest.json":           {Data: []byte(`{"runbooks": {"implementer": {"skills": []}}, "skills": []}`)},
		"runbooks/implementer.md": {Data: []byte("---\nname: loop-implementer\n---\n")},
		pack.ThirdPartyFile:       {Data: []byte(`{"skills": [` + strings.Join(pins, ", ") + `]}`)},
	}
}

type alertRow struct{ Project, Subject, Body, State string }

// serveSkillAlerts starts the server that productionOptions composes over
// fakes, reads each alert of kind third_party_skills_missing once it is
// ready, and stops it. It returns the pack the scheduler received.
func serveSkillAlerts(t *testing.T, dir string, src fs.FS) (brief.Pack, []alertRow) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "checkouts")
	bx := beadsfake.New("tk")
	if err := bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	host := agentfake.New(root)
	w := wiring{
		beads: func(context.Context, *config.HostConfig) (machine.BeadsResolver, func(context.Context) error, error) {
			return func(string) (beadsx.Client, error) { return bx, nil }, func(context.Context) error { return nil }, nil
		},
		agents: func(*config.HostConfig, *config.Projects) (agenthost.Host, func(string) sched.CheckoutHost, error) {
			return host, func(string) sched.CheckoutHost { return host }, nil
		},
		pack: func(fetched fs.FS) (brief.Pack, []pack.ThirdParty, error) { return pack.BriefOf(src, fetched) },
	}
	opts, cleanup, err := productionOptions(context.Background(), dir, w)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var rows []alertRow
	opts.Setup = append(opts.Setup, func(s *server.Server) error {
		return s.Store.Read(context.Background(), func(tx *sql.Tx) error {
			res, err := tx.Query(`SELECT project, subject, body, state FROM alerts WHERE kind = ? ORDER BY id`, config.AlertThirdPartySkills)
			if err != nil {
				return err
			}
			defer func() { _ = res.Close() }()
			for res.Next() {
				var r alertRow
				if err := res.Scan(&r.Project, &r.Subject, &r.Body, &r.State); err != nil {
					return err
				}
				rows = append(rows, r)
			}
			return res.Err()
		})
	})
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
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return opts.Sched.Pack, rows
}

// C13: with one pinned skill missing and one that fails its hash, the
// server still serves the pack with every other skill, and raises one
// alert with no project that names both and names loop install. A later
// start with every skill in place closes it.
func TestAServerWithAMissingOrWrongSkillSetStillServesAndSaysSo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultHost(dir)
	cfg.Beads = config.BeadsConfig{BD: "/opt/loop/bd", Dolt: "/opt/loop/dolt"}
	cfg.AgentHost = config.AgentHostConfig{Kind: "orca", Binary: "/opt/orca", CheckoutRoot: filepath.Join(dir, "checkouts")}
	if err := config.SaveHost(filepath.Join(dir, config.HostConfigFile), cfg); err != nil {
		t.Fatal(err)
	}
	skills := release.Layout{Dir: dir}.Skills(cli.Version)
	for _, n := range []string{"widget", "gadget", "gizmo"} {
		writeSkill(t, skills, n)
	}
	src := pinnedSource(t, skills, "widget", "gadget", "gizmo")
	if err := os.WriteFile(filepath.Join(skills, "gadget", "reference", "one.md"), []byte("# Edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(skills, "gizmo")); err != nil {
		t.Fatal(err)
	}

	p, rows := serveSkillAlerts(t, dir, src)
	got, err := p.Skills()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"loop-implementer", "loop-widget"}; !slices.Equal(got, want) {
		t.Fatalf("the served pack holds %v, want %v", got, want)
	}
	if len(rows) != 1 || rows[0].State != "open" || rows[0].Project != "" {
		t.Fatalf("alerts = %+v, want one open alert with no project", rows)
	}
	text := rows[0].Subject + "\n" + rows[0].Body
	for _, want := range []string{"gadget", "gizmo", "loop install"} {
		if !strings.Contains(text, want) {
			t.Errorf("the alert does not name %s: %q", want, text)
		}
	}
	if strings.Contains(text, "widget") {
		t.Errorf("the alert names widget, which matches its pin: %q", text)
	}

	// loop install places the set again.
	for _, n := range []string{"gadget", "gizmo"} {
		if err := os.RemoveAll(filepath.Join(skills, n)); err != nil {
			t.Fatal(err)
		}
		writeSkill(t, skills, n)
	}
	p, rows = serveSkillAlerts(t, dir, src)
	if got, _ := p.Skills(); !slices.Equal(got, []string{"loop-gadget", "loop-gizmo", "loop-implementer", "loop-widget"}) {
		t.Fatalf("the served pack holds %v after the repair", got)
	}
	if len(rows) != 1 || rows[0].State != "closed" {
		t.Fatalf("alerts after the repair = %+v, want the one alert closed", rows)
	}
}

// The production server names <instance>/current/loop as each run's
// binary when that path exists, and names none when it does not.
func TestC10TheProductionLoopBinaryIsTheCurrentLink(t *testing.T) {
	dir := t.TempDir()
	opts, cleanup, err := productionOptions(context.Background(), dir, wiring{})
	defer cleanup()
	if err != nil || opts.LoopBinary != "" {
		t.Fatalf("no current link: LoopBinary %q, %v", opts.LoopBinary, err)
	}
	version := filepath.Join(dir, release.VersionsDir, "v9.9.9")
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(version, release.BinaryName), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(release.VersionsDir, "v9.9.9"), filepath.Join(dir, release.CurrentLink)); err != nil {
		t.Fatal(err)
	}
	opts, cleanup, err = productionOptions(context.Background(), dir, wiring{})
	defer cleanup()
	if want := filepath.Join(dir, release.CurrentLink, release.BinaryName); err != nil || opts.LoopBinary != want {
		t.Fatalf("with a current link: LoopBinary %q, want %s, %v", opts.LoopBinary, want, err)
	}
}

// C21: serve registers the orca-acceptance job when agent_host.kind is
// orca, and not for fake or an empty kind.
func TestC21TheOrcaLaneJobExistsOnlyOnAnOrcaHost(t *testing.T) {
	for _, kind := range []string{"orca", "fake", ""} {
		t.Run("kind "+kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "instance")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "checkouts")
			cfg := config.DefaultHost(dir)
			cfg.Beads = config.BeadsConfig{BD: "/opt/loop/bd", Dolt: "/opt/loop/dolt"}
			cfg.AgentHost = config.AgentHostConfig{Kind: kind, Binary: "/opt/orca", CheckoutRoot: root}
			if err := config.SaveHost(filepath.Join(dir, config.HostConfigFile), cfg); err != nil {
				t.Fatal(err)
			}
			bx := beadsfake.New("tk")
			if err := bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
				t.Fatal(err)
			}
			host := agentfake.New(root)
			w := wiring{
				beads: func(context.Context, *config.HostConfig) (machine.BeadsResolver, func(context.Context) error, error) {
					return func(string) (beadsx.Client, error) { return bx, nil }, func(context.Context) error { return nil }, nil
				},
				agents: func(*config.HostConfig, *config.Projects) (agenthost.Host, func(string) sched.CheckoutHost, error) {
					return host, func(string) sched.CheckoutHost { return host }, nil
				},
			}
			opts, cleanup, err := productionOptions(context.Background(), dir, w)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			registered := false
			opts.Setup = append(opts.Setup, func(s *server.Server) error {
				if s.Sched != nil {
					err := s.Sched.RegisterInstanceJob(orcalane.JobName, func(context.Context) error { return nil })
					registered = err != nil && strings.Contains(err.Error(), "twice")
				}
				return nil
			})
			t.Setenv("HOME", t.TempDir())
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
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if want := kind == "orca"; registered != want {
				t.Fatalf("orca-acceptance registered = %v, want %v", registered, want)
			}
		})
	}
}

// The server part reads the host lines of loop doctor: the install line,
// then one requirement line for each host row. orca is found where host
// config names it.
func TestServeHostChecksHoldTheRequirementLines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	agent := filepath.Join(t.TempDir(), "orca")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.HostConfig{AgentHost: config.AgentHostConfig{Kind: "orca", Binary: agent}}

	lines := hostChecks(dir, cfg, home)(context.Background())
	if len(lines) == 0 || lines[0].Check != "install" {
		t.Fatalf("host lines = %+v", lines)
	}
	got := map[string]bool{}
	for _, l := range lines[1:] {
		got[l.Check] = l.OK
	}
	if ok, found := got["requires_git"]; !found || ok {
		t.Fatalf("requires_git with an empty PATH = %v %v: %+v", ok, found, lines)
	}
	if !got["requires_orca"] {
		t.Fatalf("requires_orca with host config naming %s: %+v", agent, lines)
	}
}

// The server part checks only the rows that the server process runs. A
// host CLI verb runs gh and systemd-run with the operator's own path, so a
// line on the service's path would ring for a binary the server never runs.
func TestServeHostChecksSkipTheRowsOnlyHostVerbsRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())

	var got []string
	for _, l := range hostChecks(dir, nil, home)(context.Background())[1:] {
		got = append(got, l.Check)
	}
	want := []string{"requires_git", "requires_python3", "requires_orca", "requires_op"}
	if !slices.Equal(got, want) {
		t.Fatalf("server requirement lines = %v, want %v", got, want)
	}
}
