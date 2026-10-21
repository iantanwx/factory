package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/release"
)

// registerRelease plugs in the client-side host verbs of U16: install,
// upgrade, rollback, restore and doctor. Doctor calls the server verb
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
	// self is the running binary.
	self func() (string, error)
	// home is the operator's home directory.
	home func() (string, error)
	// platform is GOOS/GOARCH.
	platform string
}

func newHostVerbs(r *cli.Registry, c *cli.Client) *hostVerbs {
	return &hostVerbs{
		reg: r, client: c,
		service: release.HostServiceManager,
		source: func() release.Source {
			return release.Source{Base: os.Getenv(release.ReleaseBaseEnv)}
		},
		keys:     release.TrustedKeys,
		self:     os.Executable,
		home:     os.UserHomeDir,
		platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
}

func (h *hostVerbs) register() error {
	for _, code := range release.Refusals() {
		if err := h.reg.RegisterRefusal(code); err != nil {
			return err
		}
	}
	for path, fn := range map[string]cli.Handler{
		"install": h.install, "upgrade": h.upgrade, "rollback": h.rollback, "restore": h.restore, "doctor": h.doctor,
	} {
		if err := h.reg.SetHandler(path, fn); err != nil {
			return err
		}
	}
	return nil
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
		return cli.Refuse(cli.Refusal{Code: re.Code, Rule: re.Rule, Hint: re.Hint})
	}
	return cli.Refuse(cli.Refusal{Code: cli.CodeInternal, Rule: "host_verb_failed", Hint: err.Error()})
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
	return &release.Installer{Layout: release.Layout{Dir: dir}, Service: svc, Prov: h.prov, Pins: pins, Self: self, Meta: meta}, nil
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
		rep, err = in.Install(ctx)
	}
	if err != nil {
		return fail(err)
	}
	if len(rep.Drift) > 0 {
		return cli.Refuse(cli.Refusal{Code: release.CodeDrift, Rule: "drift", Hint: strings.Join(rep.Drift, "; ")})
	}
	return cli.Found(rep)
}

func (h *hostVerbs) upgrade(ctx context.Context, inv cli.Invocation) cli.Result {
	dir, ctl, res := h.prepare(ctx, inv)
	if res != nil {
		return *res
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
	u := &release.Upgrader{Host: host, Source: src, Keys: keys, Platform: h.platform, Prov: h.prov, Self: self}
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
	host, err := h.host(dir, ctl)
	if err != nil {
		return fail(err)
	}
	rep, err := (&release.Rollback{Host: host}).Run(ctx)
	if err != nil {
		return fail(err)
	}
	return cli.Found(rep)
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
	d := &release.Doctor{Layout: in.Layout, Installer: in, Home: home, Repos: release.OrcaRepos(execOutput),
		Remote: func(ctx context.Context) ([]release.DoctorLine, error) { return h.githubLines(ctx, ctl), nil }}
	lines := d.Run(ctx)
	if failed := release.Failed(lines); len(failed) > 0 {
		var hint []string
		for _, l := range failed {
			hint = append(hint, l.Check+": "+l.Detail)
		}
		return cli.Refuse(cli.Refusal{Code: release.CodeDoctorFailed, Rule: "doctor_failed", Hint: strings.Join(hint, "; ")})
	}
	return cli.Found(lines)
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

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "host-" + hex.EncodeToString(b[:])
}

// authorize is the probe the CLI's gate makes: the server checks that the
// caller of a client-side host verb is no run, room or agent session.
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
	var projects []struct {
		Project string               `json:"project"`
		Lines   []release.DoctorLine `json:"lines"`
	}
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
