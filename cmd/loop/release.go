package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/hexid"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/server"
	"github.com/iantanwx/loop/internal/store"
)

// registerRelease plugs in the client-side host verbs: install,
// upgrade, rollback, restore and doctor, and the version verb that names
// the newest release. Doctor calls the server verb
// doctor-github for the checks that need GitHub. It replaces the gate that
// cli.Client.Attach set on upgrade and rollback with handlers that make the
// same authorization probe whenever the server answers. run calls it after
// Attach.
func registerRelease(r *cli.Registry, c *cli.Client) error {
	return newHostVerbs(r, c).register()
}

// hostVerbs holds what the host verbs reach outside the process. A test
// replaces each part.
type hostVerbs struct {
	reg    *cli.Registry
	client *cli.Client
	// service is the host's service manager for an instance.
	service func(instance string) (release.ServiceManager, error)
	// source is where upgrade reads releases.
	source func() release.Source
	// keys are the trusted release keys.
	keys func() ([]release.PublicKey, error)
	// prov fetches pinned binaries.
	prov release.Provisioner
	// fetcher fetches pinned third-party skills.
	fetcher release.SkillFetcher
	// self is the running binary.
	self func() (string, error)
	// home is the operator's home directory.
	home func() (string, error)
	// platform is GOOS/GOARCH.
	platform string
	// setup makes install's host steps. Nil skips them.
	setup func(inv cli.Invocation) (*release.HostSetup, error)
	// cmd runs git and gh for register.
	cmd cmdRunner
	// secret reads one value from the secret store.
	secret func(ctx context.Context, l release.Layout, scope, name string) ([]byte, error)
	// githubAPI is GitHub's REST root for register's App calls; githubAPI
	// when empty.
	githubAPI string
	// stderr is where register talks to the operator; os.Stderr when nil.
	stderr io.Writer
	// pollEvery is how often register asks whether an App is installed.
	pollEvery time.Duration
	// detach starts `loop <args>` for the instance as a process outside
	// the caller's session, where the coordinator's upgrade and rollback
	// run.
	detach func(ctx context.Context, instance string, args []string) (server.UpgradeStart, error)
	// latestWithin bounds version's read of the newest release.
	latestWithin time.Duration
}

func (h *hostVerbs) out() io.Writer {
	if h.stderr == nil {
		return os.Stderr
	}
	return h.stderr
}

func (h *hostVerbs) poll() time.Duration {
	if h.pollEvery == 0 {
		return 5 * time.Second
	}
	return h.pollEvery
}

func newHostVerbs(r *cli.Registry, c *cli.Client) *hostVerbs {
	h := &hostVerbs{
		reg: r, client: c,
		service: release.HostServiceManager,
		source: func() release.Source {
			return release.Source{Base: os.Getenv(release.ReleaseBaseEnv)}
		},
		keys:         release.TrustedKeys,
		self:         os.Executable,
		home:         os.UserHomeDir,
		platform:     runtime.GOOS + "/" + runtime.GOARCH,
		latestWithin: 5 * time.Second,
	}
	h.setup = h.hostSetup
	h.cmd = execCmd
	h.secret = release.ReadSecret
	h.detach = func(ctx context.Context, instance string, args []string) (server.UpgradeStart, error) {
		return newUpgradeStarter(instance).start(ctx, args)
	}
	return h
}

func (h *hostVerbs) register() error {
	for _, code := range release.Refusals() {
		if err := h.reg.RegisterRefusal(code); err != nil {
			return err
		}
	}
	for path, fn := range map[string]cli.Handler{
		"install": h.install, "register": h.registerProject, "upgrade": h.upgrade, "rollback": h.rollback, "restore": h.restore, "doctor": h.doctor,
		"version": h.version,
	} {
		if err := h.reg.SetHandler(path, fn); err != nil {
			return err
		}
	}
	return nil
}

// version answers what internal/cli's version answers, with the newest
// release and whether this one is older (ADR D13). It reads the newest
// release here because internal/cli does not import internal/release. A
// failed read, or one past the deadline, names latest_error, and still
// exits 0.
func (h *hostVerbs) version(ctx context.Context, _ cli.Invocation) cli.Result {
	data := map[string]any{"version": cli.Version, "go": runtime.Version(), "platform": h.platform, "latest": "", "behind": false}
	ctx, cancel := context.WithTimeout(ctx, h.latestWithin)
	defer cancel()
	latest, err := h.source().LatestVersion(ctx)
	if err != nil {
		data["latest_error"] = err.Error()
		return cli.Found(data)
	}
	data["latest"] = latest
	data["behind"] = release.ValidVersion(cli.Version) && release.CompareVersions(cli.Version, latest) < 0
	return cli.Found(data)
}

func (h *hostVerbs) instance() (string, error) {
	if h.client.Instance != "" {
		return h.client.Instance, nil
	}
	return cli.InstanceDir()
}

// fail turns an error into the envelope's refusal.
func fail(err error) cli.Result {
	var re *release.Error
	if errors.As(err, &re) {
		return cli.Reject(re.Code, re.Rule, re.Hint)
	}
	return cli.Reject(cli.CodeInternal, "host_verb_failed", err.Error())
}

// prepare finds the instance and, when the server answers, asks it to
// check the caller of the host verb (KTD12). A server that is down is
// never waited on: the verb then rests on the credential file alone.
func (h *hostVerbs) prepare(ctx context.Context, inv cli.Invocation) (string, *serverControl, *cli.Result) {
	dir, err := h.instance()
	if err != nil {
		res := fail(err)
		return "", nil, &res
	}
	ctl := &serverControl{h: h, instance: dir}
	if ctl.Reachable(ctx) {
		if res := ctl.authorize(ctx, inv.Verb.Path); res != nil {
			return "", nil, res
		}
	}
	return dir, ctl, nil
}

func (h *hostVerbs) host(dir string, ctl *serverControl) (release.Host, error) {
	svc, err := h.service(dir)
	if err != nil {
		return release.Host{}, err
	}
	return release.Host{Layout: release.Layout{Dir: dir}, Service: svc, Server: ctl}, nil
}

func (h *hostVerbs) installer(ctx context.Context, dir string) (*release.Installer, error) {
	svc, err := h.service(dir)
	if err != nil {
		return nil, err
	}
	self, err := h.self()
	if err != nil {
		return nil, err
	}
	meta, err := release.ThisRelease(ctx)
	if err != nil {
		return nil, err
	}
	pins, err := release.PinsFor(release.Pins(), h.platform)
	if err != nil {
		return nil, err
	}
	home, err := h.home()
	if err != nil {
		return nil, err
	}
	return &release.Installer{Layout: release.Layout{Dir: dir}, Service: svc, Prov: h.prov, Pins: pins, Self: self, Meta: meta,
		Skills: pack.ThirdPartyPins(), Fetcher: h.fetcher, Requires: release.HostRequirements(strings.Split(h.platform, "/")[0]), Home: home}, nil
}

func (h *hostVerbs) install(ctx context.Context, inv cli.Invocation) cli.Result {
	dir, _, res := h.prepare(ctx, inv)
	if res != nil {
		return *res
	}
	in, err := h.installer(ctx, dir)
	if err != nil {
		return fail(err)
	}
	var rep release.InstallReport
	if inv.Flags["check"] == "true" {
		rep, err = in.Check(ctx)
	} else {
		if h.setup != nil {
			if in.Setup, err = h.setup(inv); err != nil {
				return fail(err)
			}
		}
		rep, err = in.Install(ctx)
	}
	if err != nil {
		return fail(err)
	}
	if len(rep.Drift) > 0 {
		return cli.Reject(release.CodeDrift, "drift", strings.Join(rep.Drift, "; "))
	}
	return cli.Found(rep)
}

// hostSetup makes install's host steps for the operator's own account.
// The App flow talks to the operator on stderr, because stdout carries the
// envelope, and reads a pasted address from stdin when it is a terminal.
func (h *hostVerbs) hostSetup(inv cli.Invocation) (*release.HostSetup, error) {
	home, err := h.home()
	if err != nil {
		return nil, err
	}
	s := &release.HostSetup{Home: home, GOOS: strings.Split(h.platform, "/")[0], Run: release.ExecRunner}
	if u, err := user.Current(); err == nil {
		s.User = u.Username
	}
	for _, p := range []string{"orca", filepath.Join(home, ".config", "orca", "cli", "bin", "orca")} {
		if abs, err := exec.LookPath(p); err == nil {
			if s.Orca, err = filepath.Abs(abs); err == nil {
				break
			}
		}
	}
	if inv.Flags["no-github-app"] == "true" {
		return s, nil
	}
	s.App = h.appFlow(inv.Flags["github-owner"]).Create
	return s, nil
}

// appFlow is the App flow for an organisation, or for the operator's own
// account when owner is empty. It talks to the operator on stderr, and
// reads a pasted address from stdin when it is a terminal.
func (h *hostVerbs) appFlow(owner string) *release.AppFlow {
	flow := &release.AppFlow{
		Owner: owner, Name: "loop-" + hexid.Random(3), Homepage: release.Homepage,
		Web: "https://github.com", API: githubAPI,
		Listen: appListen(), Out: h.out(), Open: openBrowser, Timeout: 30 * time.Minute,
	}
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
		flow.Paste = os.Stdin
	}
	return flow
}

// appListen is where the App flow listens. On a Tailscale host it is the
// tailnet address, so a browser on another tailnet machine reaches the
// page and GitHub's redirect. Elsewhere it is loopback.
func appListen() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "ip", "-4").Output()
	if err != nil {
		return release.AppListen
	}
	ip, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if net.ParseIP(ip) == nil {
		return release.AppListen
	}
	_, port, _ := net.SplitHostPort(release.AppListen)
	return net.JoinHostPort(ip, port)
}

// openBrowser shows url in the desktop's browser when this host has one.
// Without one it does nothing: the flow has printed the link.
func openBrowser(url string) {
	for _, name := range []string{os.Getenv("BROWSER"), "xdg-open", "open"} {
		if name == "" {
			continue
		}
		if path, err := exec.LookPath(name); err == nil {
			_ = exec.Command(path, url).Start()
			return
		}
	}
}

func (h *hostVerbs) upgrade(ctx context.Context, inv cli.Invocation) cli.Result {
	dir, ctl, res := h.prepare(ctx, inv)
	if res != nil {
		return *res
	}
	if coord, err := h.coordinator(); err != nil {
		return fail(err)
	} else if coord {
		args := []string{"upgrade"}
		if v := inv.Arg("version"); v != "" {
			if !release.ValidVersion(v) {
				return cli.Reject(cli.CodeUsage, "bad_version", fmt.Sprintf("%v: %q", server.ErrBadVersion, v))
			}
			args = append(args, v)
		}
		return h.handOff(ctx, dir, args)
	}
	host, err := h.host(dir, ctl)
	if err != nil {
		return fail(err)
	}
	keys, err := h.keys()
	if err != nil {
		return fail(err)
	}
	self, err := release.ThisRelease(ctx)
	if err != nil {
		return fail(err)
	}
	src := h.source()
	version := inv.Arg("version")
	if version == "" {
		if version, err = src.LatestVersion(ctx); err != nil {
			return fail(&release.Error{Code: release.CodeReleaseUnavailable, Rule: "latest_unavailable", Hint: err.Error()})
		}
	}
	u := &release.Upgrader{Host: host, Source: src, Keys: keys, Platform: h.platform, Prov: h.prov, Fetcher: h.fetcher, Self: self}
	rep, err := u.Run(ctx, version)
	if err != nil {
		return fail(err)
	}
	if !rep.Changed {
		return cli.Result{Exit: cli.ExitNothing, Data: rep}
	}
	return cli.Found(rep)
}

func (h *hostVerbs) rollback(ctx context.Context, inv cli.Invocation) cli.Result {
	dir, ctl, res := h.prepare(ctx, inv)
	if res != nil {
		return *res
	}
	if coord, err := h.coordinator(); err != nil {
		return fail(err)
	} else if coord {
		return h.handOff(ctx, dir, []string{"rollback"})
	}
	host, err := h.host(dir, ctl)
	if err != nil {
		return fail(err)
	}
	rb := &release.Rollback{Host: host, HandBack: func(ctx context.Context) error { return handBack(ctx, host.Layout) }}
	rep, err := rb.Run(ctx)
	if err != nil {
		return fail(err)
	}
	return cli.Found(rep)
}

// handBack leaves the release below this one the one coordinator row it
// reads (sched.HandBack). An instance with no database has no coordinator.
func handBack(ctx context.Context, l release.Layout) error {
	if _, err := os.Stat(l.DB()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	ps, err := (&config.Projects{HostPath: l.HostConfig()}).List()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	st, err := store.Open(ctx, l.DB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	return sched.HandBack(ctx, st, ps)
}

// coordinator reports whether the caller works in the coordinator's
// checkout, from where the CLI sends the coordinator's token.
func (h *hostVerbs) coordinator() (bool, error) {
	getwd := h.client.Getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	cwd, err := getwd()
	if err != nil {
		return false, fmt.Errorf("read the working directory: %w", err)
	}
	checkout, err := cli.FindCheckout(cwd)
	if errors.Is(err, cli.ErrNoCheckout) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tf, err := cli.ReadTokenFile(checkout)
	if err != nil {
		return false, err
	}
	return tf.Kind == cli.PrincipalCoordinator, nil
}

// handOff starts the coordinator's upgrade or rollback outside its session
// (factorio-obc). The verb drains, stops and restarts the server, which can
// take hours. In the session it would hold the coordinator's turn for the
// whole drain, call drain, a host-only verb, with the coordinator's token,
// and die when the session ends. The detached process works in the home
// directory with no token, as the host, and the server checks it again
// there.
func (h *hostVerbs) handOff(ctx context.Context, dir string, args []string) cli.Result {
	st, err := h.detach(ctx, dir, args)
	if err != nil {
		return fail(fmt.Errorf("start loop %s outside the coordinator's session: %w", args[0], err))
	}
	return cli.Found(map[string]any{"started": true, "verb": args[0], "unit": st.Unit, "command": st.Command,
		"next": fmt.Sprintf("loop %s drains this server, stops it and starts it again, outside this session; "+
			"after the restart, run `loop version` and report the version it names", strings.Join(args, " "))})
}

func (h *hostVerbs) restore(ctx context.Context, inv cli.Invocation) cli.Result {
	dir, ctl, res := h.prepare(ctx, inv)
	if res != nil {
		return *res
	}
	host, err := h.host(dir, ctl)
	if err != nil {
		return fail(err)
	}
	// The optional snapshot argument names one snapshot; the newest when absent.
	rep, err := (&release.Restore{Host: host}).Run(ctx, inv.Arg("snapshot"))
	if err != nil {
		return fail(err)
	}
	return cli.Found(rep)
}

func (h *hostVerbs) doctor(ctx context.Context, inv cli.Invocation) cli.Result {
	dir, ctl, res := h.prepare(ctx, inv)
	if res != nil {
		return *res
	}
	in, err := h.installer(ctx, dir)
	if err != nil {
		return fail(err)
	}
	home, err := h.home()
	if err != nil {
		return fail(err)
	}
	d := &release.Doctor{Layout: in.Layout, Installer: in, Home: home, Requires: in.Requires, Repos: release.OrcaRepos(execOutput),
		Remote: func(ctx context.Context) ([]release.DoctorLine, error) { return h.githubLines(ctx, ctl), nil }}
	lines := append(d.Run(ctx), h.envLines(ctx, ctl, inv.Flags["fix"] == "true")...)
	if failed := release.Failed(lines); len(failed) > 0 {
		var hint []string
		for _, l := range failed {
			hint = append(hint, l.Check+": "+l.Detail)
		}
		return cli.Reject(release.CodeDoctorFailed, "doctor_failed", strings.Join(hint, "; "))
	}
	return cli.Found(lines)
}

// envLines asks the server with `loop doctor-env` for each .env in a
// checkout with no live run, because only the server knows which runs
// live. With fix, the server removes each one (KTD17).
func (h *hostVerbs) envLines(ctx context.Context, ctl *serverControl, fix bool) []release.DoctorLine {
	fail := func(format string, args ...any) []release.DoctorLine {
		return []release.DoctorLine{{Check: server.StaleEnvCheck, Detail: fmt.Sprintf(format, args...)}}
	}
	if !ctl.Reachable(ctx) {
		return fail("unchecked: the server is not running, and only the server knows which runs live; start it and run loop doctor again")
	}
	v, ok := h.reg.Verb("doctor-env")
	if !ok {
		return fail("unchecked: this build has no doctor-env verb")
	}
	flags := map[string]string{}
	if fix {
		flags["fix"] = "true"
	}
	res := ctl.client().Remote(ctx, cli.Invocation{Verb: v, Flags: flags, RequestID: newRequestID()})
	if r := res.Refusal; r != nil {
		return fail("the server's .env check could not run: %s %s: %s", r.Code, r.Rule, r.Hint)
	}
	var lines []release.DoctorLine
	raw, err := json.Marshal(res.Data)
	if err == nil {
		err = json.Unmarshal(raw, &lines)
	}
	if err != nil {
		return fail("the server's .env check answered in a form this build cannot read: %v", err)
	}
	return lines
}

func execOutput(ctx context.Context, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, bin, args...).Output()
}

// serverControl reaches the instance's server through the CLI's socket
// client.
type serverControl struct {
	h        *hostVerbs
	instance string
}

// Reachable makes one connection to the socket. A missing socket, or a
// socket file no server listens on, answers false at once.
func (s *serverControl) Reachable(ctx context.Context) bool {
	d := net.Dialer{Timeout: time.Second}
	conn, err := d.DialContext(ctx, "unix", cli.SocketPath(s.instance))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (s *serverControl) client() *cli.Client {
	c := *s.h.client
	c.Instance = s.instance
	return &c
}

func newRequestID() string { return "host-" + hexid.Random(8) }

// authorize is the probe the CLI's gate makes: the server checks that the
// caller of a client-side host verb is no run, room or agent session, or
// is the coordinator in its own checkout.
func (s *serverControl) authorize(ctx context.Context, path string) *cli.Result {
	v, ok := s.h.reg.Verb(path)
	if !ok {
		res := fail(fmt.Errorf("no verb %q", path))
		return &res
	}
	probe := *v
	probe.RPC = cli.AuthorizeMethod
	res := s.client().Remote(ctx, cli.Invocation{Verb: &probe, Flags: map[string]string{cli.ExtraAuthorize: "true"}, RequestID: newRequestID()})
	if res.Refusal != nil {
		return &res
	}
	return nil
}

// drainLimit bounds a drain the server never reports done.
const drainLimit = 6 * time.Hour

// Drain calls `loop drain` until the server reports the drain done (AE6).
// A server that runs no scheduler admits no run, so there is nothing to
// drain.
func (s *serverControl) Drain(ctx context.Context) (release.DrainReport, error) {
	v, ok := s.h.reg.Verb("drain")
	if !ok {
		return release.DrainReport{}, errors.New("no drain verb")
	}
	ctx, cancel := context.WithTimeout(ctx, drainLimit)
	defer cancel()
	for {
		res := s.client().Remote(ctx, cli.Invocation{Verb: v, Flags: map[string]string{}, RequestID: newRequestID()})
		if res.Refusal != nil {
			if res.Refusal.Code == cli.CodeNotImplemented {
				return release.DrainReport{Skipped: "the server runs no scheduler, so it admits no run"}, nil
			}
			return release.DrainReport{}, fmt.Errorf("%s %s: %s", res.Refusal.Code, res.Refusal.Rule, res.Refusal.Hint)
		}
		var st struct {
			Drained  bool `json:"drained"`
			TimedOut bool `json:"timed_out"`
			Done     bool `json:"done"`
			InFlight int  `json:"in_flight"`
		}
		raw, err := json.Marshal(res.Data)
		if err == nil {
			err = json.Unmarshal(raw, &st)
		}
		if err != nil {
			return release.DrainReport{}, fmt.Errorf("read the drain answer: %w", err)
		}
		if st.Done {
			return release.DrainReport{Drained: st.Drained, TimedOut: st.TimedOut, InFlight: st.InFlight}, nil
		}
		if ctx.Err() != nil {
			return release.DrainReport{}, ctx.Err()
		}
	}
}

// githubLines runs the GitHub checks of `loop doctor` on the server with
// `loop doctor-github`, because only the server holds the GitHub
// credentials. Each line names its project. A check that cannot run fails
// one github line that says why, and is never a pass: with the server
// down, the line reads unchecked.
func (h *hostVerbs) githubLines(ctx context.Context, ctl *serverControl) []release.DoctorLine {
	fail := func(format string, args ...any) []release.DoctorLine {
		return []release.DoctorLine{{Check: "github", Detail: fmt.Sprintf(format, args...)}}
	}
	if !ctl.Reachable(ctx) {
		return fail("unchecked: the server is not running, and only the server holds the GitHub credentials; start it and run loop doctor again")
	}
	v, ok := h.reg.Verb("doctor-github")
	if !ok {
		return fail("unchecked: this build has no doctor-github verb")
	}
	res := ctl.client().Remote(ctx, cli.Invocation{Verb: v, Flags: map[string]string{}, RequestID: newRequestID()})
	if r := res.Refusal; r != nil {
		if r.Code == cli.CodeNotImplemented {
			return fail("unchecked: the server runs no GitHub actor; set credentials.github_app in host config and restart it")
		}
		return fail("the server's GitHub checks could not run: %s %s: %s", r.Code, r.Rule, r.Hint)
	}
	if res.Exit == cli.ExitNothing {
		return []release.DoctorLine{{Check: "github", OK: true, Detail: "the server holds no project to check"}}
	}
	var projects []server.ProjectDoctor
	raw, err := json.Marshal(res.Data)
	if err == nil {
		err = json.Unmarshal(raw, &projects)
	}
	if err != nil {
		return fail("the server's GitHub checks answered in a form this build cannot read: %v", err)
	}
	var lines []release.DoctorLine
	for _, p := range projects {
		for _, l := range p.Lines {
			l.Check = p.Project + "/" + l.Check
			lines = append(lines, l)
		}
	}
	return lines
}
