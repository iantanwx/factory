package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/hexid"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/server"
)

// upgradeEnv names the variables `loop upgrade` reads from the server's
// environment: the tools on PATH, the gh login and the release location.
// systemd-run copies each by name, so no value lands in a command line.
var upgradeEnv = []string{"PATH", "HOME", "GH_TOKEN", "GITHUB_TOKEN", release.ReleaseBaseEnv}

// upgradeStarter starts `loop upgrade` on the operator's decision, which
// the coordinator relays with `loop coordinator upgrade` (ADR 0010). The
// upgrade stops this server's service, so it runs outside the service: on
// Linux as a transient systemd user unit, because a stop kills every
// process in the service's cgroup, and elsewhere in a session of its own.
// It works in the home directory, outside every checkout, so the host-verb
// check admits it. Each part is a seam for a test; the zero value uses
// the host.
type upgradeStarter struct {
	instance string
	goos     string
	self     func() (string, error)
	home     func() (string, error)
	env      func(string) string
	run      func(ctx context.Context, name string, args ...string) error
	unitName func() string
}

func newUpgradeStarter(instance string) upgradeStarter {
	return upgradeStarter{instance: instance, goos: runtime.GOOS, self: os.Executable, home: os.UserHomeDir, env: os.Getenv,
		run: release.ExecRunner, unitName: func() string { return "loop-upgrade-" + hexid.Random(4) }}
}

// Start begins the upgrade to version, the latest when empty, and returns
// once its process started.
func (u upgradeStarter) Start(ctx context.Context, version string) (server.UpgradeStart, error) {
	if version != "" && !release.ValidVersion(version) {
		return server.UpgradeStart{}, fmt.Errorf("%w: %q", server.ErrBadVersion, version)
	}
	self, err := u.self()
	if err != nil {
		return server.UpgradeStart{}, err
	}
	home, err := u.home()
	if err != nil {
		return server.UpgradeStart{}, err
	}
	args := []string{"upgrade"}
	if version != "" {
		args = append(args, version)
	}
	unit := u.unitName()
	if u.goos != "linux" {
		return u.detach(self, home, unit, args)
	}
	cmd := []string{"systemd-run", "--user", "--collect", "--quiet", "--unit", unit, "--working-directory", home,
		"--setenv", cli.InstanceEnv + "=" + u.instance}
	for _, name := range upgradeEnv {
		if u.env(name) != "" {
			cmd = append(cmd, "--setenv", name)
		}
	}
	cmd = append(append(cmd, self), args...)
	if err := u.run(ctx, cmd[0], cmd[1:]...); err != nil {
		return server.UpgradeStart{}, err
	}
	return server.UpgradeStart{Unit: unit, Command: cmd}, nil
}

// detach starts the upgrade in a session of its own, which outlives the
// service's stop. Its output lands in a log file in the instance.
func (u upgradeStarter) detach(self, home, unit string, args []string) (server.UpgradeStart, error) {
	log, err := os.OpenFile(filepath.Join(u.instance, unit+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return server.UpgradeStart{}, err
	}
	defer func() { _ = log.Close() }()
	c := exec.Command(self, args...)
	c.Dir, c.Stdout, c.Stderr = home, log, log
	c.Env = append(os.Environ(), cli.InstanceEnv+"="+u.instance)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return server.UpgradeStart{}, err
	}
	_ = c.Process.Release()
	return server.UpgradeStart{Unit: log.Name(), Command: append([]string{self}, args...)}, nil
}
