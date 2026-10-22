package release

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// recorder is a Runner that records each command and runs nothing.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) run(_ context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return nil
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

type installWorld struct {
	dir  string
	rec  *recorder
	svc  Systemd
	in   *Installer
	pins *pinWorld
}

func newInstallWorld(t *testing.T) *installWorld {
	t.Helper()
	root := t.TempDir()
	w := &installWorld{dir: filepath.Join(root, "instance"), rec: &recorder{}, pins: newPinWorld(t, "1.3.1")}
	w.svc = Systemd{Dir: filepath.Join(root, "systemd"), Name: ServiceName(w.dir), Run: w.rec.run}
	self := filepath.Join(root, "downloads", "loop")
	if err := os.MkdirAll(filepath.Dir(self), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self, []byte("loop binary v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.in = &Installer{Layout: Layout{Dir: w.dir}, Service: w.svc, Prov: w.pins.prov, Pins: w.pins.pins, Self: self,
		Meta: Metadata{Version: "v1.0.0", Schema: 3, MinReaderSchema: 1, Pins: w.pins.pins}}
	return w
}

func TestInstallWritesTheUnitAndTheInstance(t *testing.T) {
	w := newInstallWorld(t)
	ctx := context.Background()
	rep, err := w.in.Install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Drift) != 0 {
		t.Fatalf("drift after install: %v", rep.Drift)
	}

	unit, err := os.ReadFile(w.svc.UnitPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"Type=notify", "Restart=on-failure", "WorkingDirectory=" + w.dir,
		"ExecStart=" + w.dir + "/current/loop serve --instance " + w.dir,
	} {
		if !strings.Contains(string(unit), "\n"+line+"\n") {
			t.Errorf("the unit lacks %q:\n%s", line, unit)
		}
	}
	if strings.Contains(string(unit), "checkout") && !strings.Contains(string(unit), "never from a\n# project checkout") {
		t.Error("the unit names a checkout")
	}
	unitName := ServiceName(w.dir) + ".service"
	if got, want := w.rec.list(), []string{"systemctl --user daemon-reload", "systemctl --user enable --now " + unitName}; !slices.Equal(got, want) {
		t.Fatalf("service commands = %q, want %q", got, want)
	}

	// The binary runs from versions/<v>, through current.
	if cur, err := w.in.Layout.CurrentVersion(); err != nil || cur != "v1.0.0" {
		t.Fatalf("current = %q %v", cur, err)
	}
	if data, err := os.ReadFile(filepath.Join(w.dir, "current", "loop")); err != nil || string(data) != "loop binary v1.0.0" {
		t.Fatalf("current/loop = %q %v", data, err)
	}
	if st, _ := w.in.Layout.ReadState(); st.Highest != "v1.0.0" {
		t.Fatalf("highest = %q", st.Highest)
	}

	// The default host config holds every throughput entry and names the
	// provisioned binaries.
	cfg, err := config.LoadHost(filepath.Join(w.dir, config.HostConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range config.ThroughputKeys() {
		if _, ok := cfg.Throughput[k]; !ok {
			t.Errorf("host config lacks throughput.%s", k)
		}
	}
	if cfg.Beads.BD != filepath.Join(w.dir, "bin", "bd") || cfg.Beads.Dolt != filepath.Join(w.dir, "bin", "dolt") {
		t.Fatalf("beads = %+v", cfg.Beads)
	}
}

func TestInstallWritesTheCredentialOnlyToItsFile(t *testing.T) {
	w := newInstallWorld(t)
	if _, err := w.in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cli.AdminCredentialPath(w.dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %v", info.Mode().Perm())
	}
	cred, err := cli.ReadAdminCredential(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	// No other file the install wrote holds it: not the unit, not host
	// config, not an environment file.
	roots := []string{w.dir, w.svc.Dir}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 || path == cli.AdminCredentialPath(w.dir) {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), cred) {
				t.Errorf("%s holds the host-admin credential", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, tmpl := range []string{"loop.service.tmpl", "loop.plist.tmpl"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "deploy", tmpl))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "Environment") || strings.Contains(string(data), "EnvironmentVariables") ||
			strings.Contains(strings.ToLower(string(data)), "credential}}") {
			t.Errorf("%s sets environment or templates a credential", tmpl)
		}
	}
}

func TestInstallCheckReportsDriftAndChangesNothing(t *testing.T) {
	w := newInstallWorld(t)
	ctx := context.Background()
	if _, err := w.in.Install(ctx); err != nil {
		t.Fatal(err)
	}
	calls := len(w.rec.list())

	// Edit the installed unit and remove bd.
	unit := w.svc.UnitPath()
	data, _ := os.ReadFile(unit)
	edited := strings.Replace(string(data), "Restart=on-failure", "Restart=always", 1)
	if err := os.WriteFile(unit, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(w.dir, "bin", "bd")); err != nil {
		t.Fatal(err)
	}
	rep, err := w.in.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Drift) != 2 || !strings.Contains(rep.Drift[0], "differs") || !strings.Contains(rep.Drift[1], "bd is missing") {
		t.Fatalf("drift = %q", rep.Drift)
	}
	if after, _ := os.ReadFile(unit); string(after) != edited {
		t.Fatal("check changed the unit")
	}
	if _, err := os.Stat(filepath.Join(w.dir, "bin", "bd")); err == nil {
		t.Fatal("check provisioned bd")
	}
	if len(w.rec.list()) != calls {
		t.Fatalf("check ran service commands: %q", w.rec.list()[calls:])
	}

	// Install repairs both.
	rep, err = w.in.Install(ctx)
	if err != nil || len(rep.Drift) != 0 {
		t.Fatalf("install after drift: %v %v", rep.Drift, err)
	}
}

func TestInstallRefusesAPinMismatchBeforeTheService(t *testing.T) {
	w := newInstallWorld(t)
	w.pins.files[w.pins.pins[0].URL] = tarGz(t, "bd", []byte("not the pinned bd"))
	if _, err := w.in.Install(context.Background()); err == nil {
		t.Fatal("install accepted a bd whose hash differs from its pin")
	}
	if len(w.rec.list()) != 0 {
		t.Fatalf("a refused install registered the service: %q", w.rec.list())
	}
	if _, err := os.Stat(w.svc.UnitPath()); err == nil {
		t.Fatal("a refused install wrote the unit")
	}
}

func TestLaunchdPlistRunsFromTheInstance(t *testing.T) {
	l := Launchd{Dir: t.TempDir(), Label: "dev.loop.test", UID: 501}
	dir := "/Users/op/.local/state/loop"
	plist, err := l.Unit(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>dev.loop.test</string>", "<string>" + dir + "/current/loop</string>", "<string>serve</string>",
		"<key>WorkingDirectory</key>\n\t<string>" + dir + "</string>", "<key>SuccessfulExit</key>\n\t\t<false/>",
	} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("the plist lacks %q", want)
		}
	}
	comment := string(plist[strings.Index(string(plist), "<!--")+4 : strings.Index(string(plist), "-->")])
	if strings.Contains(comment, "--") {
		t.Error("the plist comment holds \"--\", which XML forbids")
	}
	if _, err := l.Unit("/tmp/has space"); err == nil {
		t.Fatal("an instance path with a space was accepted")
	}
}

// TestInstallFinishesAHalfInstall: an install that stopped before the
// service ever ran leaves current on its version. A newer install moves
// current to itself and finishes.
func TestInstallFinishesAHalfInstall(t *testing.T) {
	w := newInstallWorld(t)
	ctx := context.Background()
	if err := os.MkdirAll(w.in.Layout.VersionDir("v0.9.0"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.in.Layout.Binary("v0.9.0"), []byte("loop binary v0.9.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.in.Layout.SetCurrent("v0.9.0"); err != nil {
		t.Fatal(err)
	}
	rep, err := w.in.Install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Current != "v1.0.0" || len(rep.Drift) != 0 {
		t.Fatalf("current = %q, drift %v", rep.Current, rep.Drift)
	}
}
