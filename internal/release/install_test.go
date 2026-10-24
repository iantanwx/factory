package release

import (
	"archive/tar"
	"context"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
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

// skillInstallWorld is an empty instance whose Installer embeds 4 skill
// pins, served from a local server that counts each archive it serves.
type skillInstallWorld struct {
	in    *Installer
	svc   *fakeService
	mu    sync.Mutex
	files map[string][]byte // archive by pin name
	gets  map[string]int    // archives served, by pin name
}

var skillNames = []string{"alpha", "bravo", "charlie", "delta"}

func skillBody(name string) string {
	return "---\nname: " + name + "\ndescription: The " + name + " skill.\n---\n\nDo " + name + ".\n"
}

func newSkillInstallWorld(t *testing.T) *skillInstallWorld {
	t.Helper()
	root := t.TempDir()
	w := &skillInstallWorld{svc: &fakeService{path: filepath.Join(root, "loop.service")}, files: map[string][]byte{}, gets: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		w.mu.Lock()
		data, ok := w.files[name]
		w.gets[name]++
		w.mu.Unlock()
		if !ok {
			http.NotFound(rw, r)
			return
		}
		_, _ = rw.Write(data)
	}))
	t.Cleanup(srv.Close)
	var pins []pack.ThirdParty
	for i, name := range skillNames {
		body := skillBody(name)
		w.files[name] = skillArchive(t,
			member{name: "repo-" + name + "/", typ: tar.TypeDir, mode: 0o775},
			member{name: "repo-" + name + "/README.md", typ: tar.TypeReg, mode: 0o664, body: "outside the skill\n"},
			member{name: "repo-" + name + "/skills/" + name + "/SKILL.md", typ: tar.TypeReg, mode: 0o664, body: body},
			member{name: "repo-" + name + "/skills/" + name + "/scripts/run", typ: tar.TypeReg, mode: 0o775, body: "#!/bin/sh\necho " + name + "\n"},
		)
		sha := treeHash(t, fstest.MapFS{
			"SKILL.md":    {Data: []byte(body), Mode: 0o644},
			"scripts/run": {Data: []byte("#!/bin/sh\necho " + name + "\n"), Mode: 0o755},
		})
		pins = append(pins, pack.ThirdParty{Name: name, Repo: "owner/" + name, Commit: strings.Repeat(string(rune('a'+i)), 40), Path: "skills/" + name, SHA256: sha})
	}
	bins := newPinWorld(t, "1.3.1")
	self := filepath.Join(root, "downloads", "loop")
	if err := os.MkdirAll(filepath.Dir(self), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self, []byte("loop binary v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.in = &Installer{Layout: Layout{Dir: filepath.Join(root, "instance")}, Service: w.svc, Prov: bins.prov, Pins: bins.pins, Self: self,
		Meta:    Metadata{Version: "v1.0.0", Schema: 3, MinReaderSchema: 1, Pins: bins.pins},
		Skills:  pins,
		Fetcher: SkillFetcher{HTTP: srv.Client(), URL: func(p pack.ThirdParty) string { return srv.URL + "/" + p.Name }}}
	return w
}

func (w *skillInstallWorld) served() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.gets)
}

func (w *skillInstallWorld) skillsDir() string {
	return filepath.Join(w.in.Layout.Dir, "versions", "v1.0.0", "skills")
}

func TestC8InstallPlacesTheSetAndKeepsAMatchingOne(t *testing.T) {
	w := newSkillInstallWorld(t)
	ctx := context.Background()
	rep, err := w.in.Install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Skills, skillNames) {
		t.Fatalf("report skills = %q, want %q", rep.Skills, skillNames)
	}
	if got := CheckSkills(w.in.Skills, w.skillsDir()); len(got) != 0 {
		t.Fatalf("CheckSkills after install = %q", got)
	}
	if got := w.in.Layout.Skills("v1.0.0"); got != w.skillsDir() {
		t.Fatalf("Layout.Skills = %q, want %q", got, w.skillsDir())
	}
	entries, err := os.ReadDir(w.skillsDir())
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		dirs = append(dirs, e.Name())
	}
	if !slices.Equal(dirs, skillNames) {
		t.Fatalf("versions/v1.0.0/skills holds %q, want %q", dirs, skillNames)
	}
	if data, err := os.ReadFile(filepath.Join(w.skillsDir(), "bravo", "SKILL.md")); err != nil || string(data) != skillBody("bravo") {
		t.Fatalf("bravo/SKILL.md = %q %v", data, err)
	}
	if info, err := os.Stat(filepath.Join(w.skillsDir(), "bravo", "scripts", "run")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("bravo/scripts/run = %v %v, want mode 0755", info, err)
	}
	if info, err := os.Stat(filepath.Join(w.skillsDir(), "bravo", "SKILL.md")); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("bravo/SKILL.md = %v %v, want mode 0644", info, err)
	}
	if _, err := os.Stat(filepath.Join(w.skillsDir(), "bravo", "README.md")); err == nil {
		t.Fatal("a file outside the skill path was placed")
	}
	once := map[string]int{"alpha": 1, "bravo": 1, "charlie": 1, "delta": 1}
	if got := w.served(); !maps.Equal(got, once) {
		t.Fatalf("archives served = %v, want %v", got, once)
	}

	// A second install finds every pin matching, and fetches nothing.
	rep, err = w.in.Install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skills) != 0 {
		t.Fatalf("a second install fetched %q", rep.Skills)
	}
	if got := w.served(); !maps.Equal(got, once) {
		t.Fatalf("archives served after a second install = %v, want %v", got, once)
	}

	// With 3 pins matching and 1 directory missing, install fetches all 4.
	if err := os.RemoveAll(filepath.Join(w.skillsDir(), "charlie")); err != nil {
		t.Fatal(err)
	}
	rep, err = w.in.Install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Skills, skillNames) {
		t.Fatalf("report skills = %q, want %q", rep.Skills, skillNames)
	}
	if got, want := w.served(), map[string]int{"alpha": 2, "bravo": 2, "charlie": 2, "delta": 2}; !maps.Equal(got, want) {
		t.Fatalf("archives served after a repair = %v, want %v", got, want)
	}
	if got := CheckSkills(w.in.Skills, w.skillsDir()); len(got) != 0 {
		t.Fatalf("CheckSkills after the repair = %q", got)
	}
	for _, d := range rep.Drift {
		if strings.Contains(d, "skill") {
			t.Fatalf("skill drift after the repair = %q", d)
		}
	}
}

func TestC9AFailedSkillFetchRefusesInstall(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(w *skillInstallWorld)
		code    string
		rule    string
	}{
		{name: "a pin that fails its hash", code: CodeChecksumMismatch, rule: "skill_mismatch",
			breakIt: func(w *skillInstallWorld) {
				w.in.Skills = slices.Clone(w.in.Skills)
				w.in.Skills[2].SHA256 = strings.Repeat("0", 64)
			}},
		{name: "a fetch error", code: CodeReleaseUnavailable, rule: "skill_fetch_failed",
			breakIt: func(w *skillInstallWorld) { delete(w.files, "charlie") }},
	}
	for _, tc := range cases {
		t.Run(tc.name+" on an empty instance", func(t *testing.T) {
			w := newSkillInstallWorld(t)
			tc.breakIt(w)
			_, err := w.in.Install(context.Background())
			var re *Error
			if !errors.As(err, &re) || re.Code != tc.code || re.Rule != tc.rule {
				t.Fatalf("err = %v, want %s %s", err, tc.code, tc.rule)
			}
			if !strings.Contains(re.Hint, "charlie") {
				t.Fatalf("hint %q does not name the skill", re.Hint)
			}
			if _, err := os.Stat(w.skillsDir()); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a refused install left %s: %v", w.skillsDir(), err)
			}
			if got := w.svc.list(); len(got) != 0 {
				t.Fatalf("a refused install called the service manager: %q", got)
			}
		})
		t.Run(tc.name+" over a drifted set", func(t *testing.T) {
			w := newSkillInstallWorld(t)
			ctx := context.Background()
			if _, err := w.in.Install(ctx); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(w.skillsDir(), "alpha", "SKILL.md"), []byte("edited by hand\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			before := tree(t, w.skillsDir())
			w.svc.events = nil
			tc.breakIt(w)
			_, err := w.in.Install(ctx)
			var re *Error
			if !errors.As(err, &re) || re.Code != tc.code || re.Rule != tc.rule {
				t.Fatalf("err = %v, want %s %s", err, tc.code, tc.rule)
			}
			if after := tree(t, w.skillsDir()); after != before {
				t.Fatalf("a refused install changed the skills:\nbefore\n%s\nafter\n%s", before, after)
			}
			if got := w.svc.list(); len(got) != 0 {
				t.Fatalf("a refused install called the service manager: %q", got)
			}
		})
	}
}

func TestC10CheckReportsTheSetAndInstallRepairsARunningHost(t *testing.T) {
	w := newSkillInstallWorld(t)
	ctx := context.Background()
	if _, err := w.in.Install(ctx); err != nil {
		t.Fatal(err)
	}
	// The unit is registered now; the fake manager writes no file.
	if err := os.WriteFile(w.svc.path, []byte("unit "+w.in.Layout.Dir), 0o644); err != nil {
		t.Fatal(err)
	}

	// Check names one skill missing and one that fails its hash, and
	// changes and fetches nothing.
	if err := os.RemoveAll(filepath.Join(w.skillsDir(), "alpha")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.skillsDir(), "charlie", "SKILL.md"), []byte("edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, served := tree(t, w.skillsDir()), w.served()
	w.svc.events = nil
	rep, err := w.in.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var skillDrift []string
	for _, d := range rep.Drift {
		if strings.Contains(d, "skill") {
			skillDrift = append(skillDrift, d)
		}
	}
	if len(skillDrift) != 2 || len(rep.Drift) != 2 ||
		!strings.Contains(skillDrift[0], "alpha") || !strings.Contains(skillDrift[0], "missing") ||
		!strings.Contains(skillDrift[1], "charlie") || !strings.Contains(skillDrift[1], "tree hash") {
		t.Fatalf("drift = %q, want one line for alpha missing and one for charlie's tree hash", rep.Drift)
	}
	if tree(t, w.skillsDir()) != before || !maps.Equal(w.served(), served) || len(w.svc.list()) != 0 {
		t.Fatal("check changed the skills, fetched, or called the service manager")
	}

	// Install places the set for the version current names, and restarts
	// the running service once.
	rep, err = w.in.Install(ctx)
	if err != nil || len(rep.Drift) != 0 {
		t.Fatalf("install: %v, drift %q", err, rep.Drift)
	}
	if got, want := w.svc.list(), []string{"register", "stop", "start"}; !slices.Equal(got, want) {
		t.Fatalf("service calls = %q, want %q", got, want)
	}

	// An install that places nothing does not restart it.
	w.svc.events = nil
	if _, err := w.in.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := w.svc.list(), []string{"register"}; !slices.Equal(got, want) {
		t.Fatalf("service calls = %q, want %q", got, want)
	}

	// Nor does one that places skills for a version current does not name.
	if err := os.MkdirAll(w.in.Layout.VersionDir("v1.1.0"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.in.Layout.Binary("v1.1.0"), []byte("loop binary v1.1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.in.Layout.SetCurrent("v1.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.in.Layout.DB(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(w.skillsDir(), "delta")); err != nil {
		t.Fatal(err)
	}
	w.svc.events = nil
	rep, err = w.in.Install(ctx)
	if err != nil || len(rep.Skills) != 4 {
		t.Fatalf("install: %v, skills %q", err, rep.Skills)
	}
	if got, want := w.svc.list(), []string{"register"}; !slices.Equal(got, want) {
		t.Fatalf("service calls = %q, want %q", got, want)
	}
}

// TestC8InstallPlacesTheEmbeddedPins installs the embedded pins from
// GitHub into an empty instance. It reads the network, so it runs only
// when LOOP_FETCH_PINS is set:
// LOOP_FETCH_PINS=1 go test ./internal/release -run EmbeddedPins
func TestC8InstallPlacesTheEmbeddedPins(t *testing.T) {
	if os.Getenv("LOOP_FETCH_PINS") == "" {
		t.Skip("set LOOP_FETCH_PINS=1 to fetch the embedded pins from GitHub")
	}
	w := newInstallWorld(t)
	w.in.Skills, w.in.Fetcher = pack.ThirdPartyPins(), SkillFetcher{}
	rep, err := w.in.Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skills) != 4 || len(rep.Drift) != 0 {
		t.Fatalf("skills %q, drift %q", rep.Skills, rep.Drift)
	}
	if got := CheckSkills(w.in.Skills, w.in.Layout.Skills("v1.0.0")); len(got) != 0 {
		t.Fatalf("CheckSkills = %q", got)
	}
	t.Logf("placed %q in %s", rep.Skills, w.in.Layout.Skills("v1.0.0"))
}
