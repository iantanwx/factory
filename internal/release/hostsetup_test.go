package release

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// withSetup gives the install world a host setup whose App step returns
// app, and a home with Claude Code and Codex settings the doctor fails.
func withSetup(t *testing.T, w *installWorld, app func(context.Context) (CreatedApp, error)) string {
	t.Helper()
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"model": "opus"}`)
	writeFile(t, filepath.Join(home, ".claude.json"), `{"numStartups": 3, "projects": {"/elsewhere": {"hasTrustDialogAccepted": true}}}`)
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"o5\"\n\n[projects.\"/elsewhere\"]\ntrust_level = \"trusted\"\n")
	w.in.Setup = &HostSetup{Home: home, User: "op", GOOS: "linux", Run: w.rec.run, App: app, Orca: "/opt/orca/bin/orca"}
	return home
}

func TestInstallSetsUpTheHostSoTheDoctorPasses(t *testing.T) {
	w := newInstallWorld(t)
	calls := 0
	home := withSetup(t, w, func(context.Context) (CreatedApp, error) {
		calls++
		return CreatedApp{ID: 42, Slug: "loop-abc", Owner: "op", PEM: []byte("-----BEGIN RSA PRIVATE KEY-----\nk\n-----END RSA PRIVATE KEY-----\n"),
			InstallURL: "https://github.com/apps/loop-abc/installations/new"}, nil
	})
	ctx := context.Background()
	rep, err := w.in.Install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(filepath.Join(home, ".local", "bin", "loop")); err != nil || got != filepath.Join(w.dir, "current", BinaryName) {
		t.Fatalf("~/.local/bin/loop = %q %v", got, err)
	}
	if !slices.Contains(w.rec.list(), "loginctl enable-linger op") {
		t.Fatalf("linger was not set: %q", w.rec.list())
	}
	if !slices.ContainsFunc(rep.Setup, func(s string) bool { return strings.Contains(s, "installations/new") }) {
		t.Fatalf("the report does not say where to install the App: %q", rep.Setup)
	}

	cfg, err := config.LoadHost(w.in.Layout.HostConfig())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "orca", "workspaces")
	if cfg.AgentHost.Kind != "orca" || cfg.AgentHost.Binary != "/opt/orca/bin/orca" || cfg.AgentHost.CheckoutRoot != root {
		t.Fatalf("agent host = %+v", cfg.AgentHost)
	}
	if a, ok := cfg.Credentials.App("op"); !ok || a != (config.GitHubAppCredentials{Owner: "op", AppID: 42, PrivateKeySecret: "GITHUB_APP_KEY_OP"}) {
		t.Fatalf("App credentials = %+v", cfg.Credentials.Apps())
	}
	keys, err := secrets.NewFileKeyProvider(w.in.Layout.KeyFile())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, w.in.Layout.DB())
	if err != nil {
		t.Fatal(err)
	}
	pem, err := secrets.New(db, keys).Get(ctx, config.HostScope, "GITHUB_APP_KEY_OP")
	_ = db.Close()
	if err != nil || !strings.Contains(string(pem), "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("stored key = %q %v", pem, err)
	}

	// The harness lines of loop doctor pass, and the vendors' other keys
	// survive.
	d := &Doctor{Layout: w.in.Layout, Home: home}
	for _, l := range d.harnesses(cfg) {
		if !l.OK {
			t.Errorf("doctor %s: %s", l.Check, l.Detail)
		}
	}
	var settings map[string]any
	if err := readJSON(filepath.Join(home, ".claude", "settings.json"), &settings); err != nil || settings["model"] != "opus" {
		t.Fatalf("settings.json lost a key: %v %v", settings, err)
	}
	var global map[string]any
	if err := readJSON(filepath.Join(home, ".claude.json"), &global); err != nil || global["numStartups"] != float64(3) {
		t.Fatalf(".claude.json lost a key: %v %v", global, err)
	}
	toml, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.HasPrefix(string(toml), "check_for_update_on_startup = false\nmodel = \"o5\"\n") {
		t.Fatalf("config.toml:\n%s", toml)
	}

	// A second install changes no file, creates no App, and restarts
	// nothing.
	before := snapshotFiles(t, home)
	n := len(w.rec.list())
	if rep, err = w.in.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("the App was created %d times", calls)
	}
	if len(rep.Setup) != 0 {
		t.Fatalf("the second install changed %q", rep.Setup)
	}
	if after := snapshotFiles(t, home); !mapsEqual(before, after) {
		t.Fatal("the second install rewrote a harness file")
	}
	for _, c := range w.rec.list()[n:] {
		if strings.Contains(c, " stop ") || strings.Contains(c, " start ") {
			t.Fatalf("the second install ran %q", c)
		}
	}
}

func TestInstallRestartsARunningServiceForANewApp(t *testing.T) {
	w := newInstallWorld(t)
	ctx := context.Background()
	if _, err := w.in.Install(ctx); err != nil {
		t.Fatal(err)
	}
	withSetup(t, w, func(context.Context) (CreatedApp, error) {
		return CreatedApp{ID: 7, Slug: "loop-x", Owner: "op", PEM: []byte("pem"), InstallURL: "https://github.com/apps/loop-x/installations/new"}, nil
	})
	n := len(w.rec.list())
	if _, err := w.in.Install(ctx); err != nil {
		t.Fatal(err)
	}
	unit := ServiceName(w.dir) + ".service"
	got := w.rec.list()[n:]
	if !slices.Contains(got, "systemctl --user stop "+unit) || !slices.Contains(got, "systemctl --user start "+unit) {
		t.Fatalf("the service was not restarted: %q", got)
	}
}

func TestInstallStopsWhenNoAppIsCreated(t *testing.T) {
	w := newInstallWorld(t)
	withSetup(t, w, func(context.Context) (CreatedApp, error) {
		return CreatedApp{}, &Error{Code: CodeAppNotCreated, Rule: "app_not_created"}
	})
	_, err := w.in.Install(context.Background())
	var re *Error
	if !errors.As(err, &re) || re.Code != CodeAppNotCreated {
		t.Fatalf("install = %v", err)
	}
	if _, err := os.Stat(w.svc.UnitPath()); err == nil {
		t.Fatal("the service started with no App")
	}
}

func TestInstallKeepsGoingWithoutLinger(t *testing.T) {
	w := newInstallWorld(t)
	withSetup(t, w, nil)
	w.in.Setup.Run = func(_ context.Context, name string, args ...string) error {
		if name == "loginctl" {
			return errors.New("no logind")
		}
		return w.rec.run(context.Background(), name, args...)
	}
	rep, err := w.in.Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(rep.Setup, func(s string) bool { return strings.Contains(s, "loginctl enable-linger op") }) {
		t.Fatalf("the report does not name the linger command: %q", rep.Setup)
	}
}

func TestSetTOMLTableKey(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"no file":         {"", "[t]\nk = v\n"},
		"other table":     {"a = 1\n[u]\nx = 2\n", "a = 1\n[u]\nx = 2\n\n[t]\nk = v\n"},
		"table, no key":   {"[t]\nx = 2\n", "[t]\nk = v\nx = 2\n"},
		"table, old key":  {"[t]\nk = old\n[u]\nk = keep\n", "[t]\nk = v\n[u]\nk = keep\n"},
		"no last newline": {"a = 1", "a = 1\n\n[t]\nk = v\n"},
	} {
		if got := setTOMLTableKey(tc.in, "t", "k", "v"); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
	if got := setTOMLTopKey("k = true\n[t]\nk = x\n", "k", "false"); got != "k = false\n[t]\nk = x\n" {
		t.Errorf("top key: %q", got)
	}
}

func snapshotFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		out[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mapsEqual(a, b map[string]string) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
