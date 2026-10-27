package release

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// hostWithConfig is a doctor over an instance whose host config exists, so
// every host line runs.
func hostWithConfig(t *testing.T) *Doctor {
	t.Helper()
	layout := Layout{Dir: t.TempDir()}
	if err := config.SaveHost(layout.HostConfig(), &config.HostConfig{StateDir: layout.Dir, Throughput: config.DefaultThroughput()}); err != nil {
		t.Fatal(err)
	}
	return &Doctor{Layout: layout, Home: t.TempDir(), Requires: HostRequirements("linux")}
}

// A PATH that lacks orca fails requires_orca, and its detail names the
// install hint. An optional binary that is missing passes and says what it
// would add.
func TestDoctorRequiresOrcaWithItsInstallHint(t *testing.T) {
	d := hostWithConfig(t)
	path := t.TempDir()
	for _, bin := range []string{"git", "python3", "gh", "systemctl", "systemd-run"} {
		fakeBin(t, path, bin)
	}
	t.Setenv("PATH", path)

	got := byCheck(d.Run(context.Background()))
	orca := got["requires_orca"]
	if orca.Check == "" || orca.OK || !strings.Contains(orca.Detail, "orca") ||
		!strings.Contains(orca.Detail, "install the Orca app; it places its CLI at ~/.config/orca/cli/bin/orca") {
		t.Fatalf("requires_orca with no orca = %+v", orca)
	}
	if git := got["requires_git"]; !git.OK {
		t.Fatalf("requires_git with git on the path = %+v", git)
	}
	if ts := got["requires_tailscale"]; !ts.OK || !strings.Contains(ts.Detail, "it would add") {
		t.Fatalf("requires_tailscale with no tailscale = %+v", ts)
	}
}

// A full PATH passes every requirement line, one for each host row of
// Linux: no CI-only, dev-only or macOS-only row, and no judge.
func TestDoctorPassesEveryRequirementOnAFullPath(t *testing.T) {
	d := hostWithConfig(t)
	path := t.TempDir()
	for _, bin := range []string{"git", "python3", "orca", "gh", "systemctl", "systemd-run", "loginctl", "tailscale", "op"} {
		fakeBin(t, path, bin)
	}
	t.Setenv("PATH", path)

	var checks []string
	for _, l := range d.Run(context.Background()) {
		if !strings.HasPrefix(l.Check, "requires_") {
			continue
		}
		checks = append(checks, l.Check)
		if !l.OK {
			t.Errorf("%s on a full path = %+v", l.Check, l)
		}
	}
	want := "requires_git requires_python3 requires_orca requires_gh requires_systemctl requires_systemd-run " +
		"requires_loginctl requires_tailscale requires_op"
	if strings.Join(checks, " ") != want {
		t.Fatalf("requirement lines = %v, want %s", checks, want)
	}
}

// Install refuses with exit 3 before it writes anything when a required
// binary is missing, and the refusal names each one with its hint.
func TestInstallRefusesAMissingRequiredBinaryAndWritesNothing(t *testing.T) {
	w := newInstallWorld(t)
	w.in.Requires = HostRequirements("linux")
	w.in.Home = t.TempDir()
	path := t.TempDir()
	for _, bin := range []string{"git", "python3", "orca", "systemctl", "systemd-run"} {
		fakeBin(t, path, bin)
	}
	t.Setenv("PATH", path)

	_, err := w.in.Install(context.Background())
	var re *Error
	if !errors.As(err, &re) || re.Code != "MISSING_BINARY" {
		t.Fatalf("install with no gh = %v", err)
	}
	if !strings.Contains(re.Hint, "gh (install the GitHub CLI from https://cli.github.com, then run gh auth login)") ||
		strings.Contains(re.Hint, "tailscale") || strings.Contains(re.Hint, "orca") {
		t.Fatalf("hint = %q", re.Hint)
	}
	exit := cli.ExitCode(-1)
	for _, c := range Refusals() {
		if c.Code == re.Code {
			exit = c.Exit
		}
	}
	if exit != cli.ExitRefused {
		t.Fatalf("MISSING_BINARY exits %d", exit)
	}
	if _, err := os.Stat(w.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install wrote the instance: %v", err)
	}
	if _, err := os.Stat(w.svc.UnitPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install wrote the unit: %v", err)
	}
}

// Install finds orca where the Orca app puts its CLI, off the path.
func TestInstallFindsOrcaUnderTheHome(t *testing.T) {
	w := newInstallWorld(t)
	w.in.Requires = HostRequirements("linux")
	w.in.Home = t.TempDir()
	path := t.TempDir()
	for _, bin := range []string{"git", "python3", "gh", "systemctl", "systemd-run"} {
		fakeBin(t, path, bin)
	}
	t.Setenv("PATH", path)
	orcaDir := w.in.Home + "/.config/orca/cli/bin"
	if err := os.MkdirAll(orcaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeBin(t, orcaDir, "orca")

	if _, err := w.in.Install(context.Background()); err != nil {
		var re *Error
		if errors.As(err, &re) && re.Code == "MISSING_BINARY" {
			t.Fatalf("install refused orca under the home: %v", err)
		}
	}
}
