package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/iantanwx/loop/deploy"
	"github.com/iantanwx/loop/internal/cli"
)

// ServiceManager registers, starts and stops the loop service. Production
// uses systemd on Linux and launchd on macOS. A test passes a fake, so no
// test touches the host's service manager.
type ServiceManager interface {
	// Unit renders the unit file for the instance.
	Unit(instance string) ([]byte, error)
	// UnitPath is where the manager reads the unit.
	UnitPath() string
	// Register writes the unit, reloads the manager, and enables and
	// starts the service.
	Register(ctx context.Context, unit []byte) error
	// Start starts the service and returns when the manager reports it
	// started.
	Start(ctx context.Context) error
	// Stop stops the service and returns when its process exited. Stopping
	// a stopped or failed service is no error.
	Stop(ctx context.Context) error
}

// Runner runs one service-manager command.
type Runner func(ctx context.Context, name string, args ...string) error

// ExecRunner runs the command and folds its output into the error.
func ExecRunner(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ServiceName is the unit name of an instance: "loop" for the default
// instance, and "loop-<digest>" for any other, so two instances on one
// host never share a unit.
func ServiceName(instance string) string {
	if def, err := cli.DefaultInstanceDir(); err == nil && filepath.Clean(def) == filepath.Clean(instance) {
		return "loop"
	}
	sum := sha256.Sum256([]byte(filepath.Clean(instance)))
	return "loop-" + hex.EncodeToString(sum[:4])
}

// checkInstancePath refuses an instance path that a unit file cannot hold
// without quoting.
func checkInstancePath(instance string) error {
	if !filepath.IsAbs(instance) {
		return fmt.Errorf("release: the instance directory %q is not absolute", instance)
	}
	if strings.ContainsAny(instance, " \t\r\n\"'\\%$&<>;`") {
		return fmt.Errorf("release: the instance directory %q holds a character a unit file cannot hold unquoted", instance)
	}
	return nil
}

func render(text string, data any) ([]byte, error) {
	t, err := template.New("unit").Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Systemd is the systemd user manager.
type Systemd struct {
	// Dir is the user unit directory, ~/.config/systemd/user.
	Dir  string
	Name string
	Run  Runner
}

func (s Systemd) unit() string { return s.Name + ".service" }

// Unit renders deploy/loop.service.tmpl.
func (s Systemd) Unit(instance string) ([]byte, error) {
	if err := checkInstancePath(instance); err != nil {
		return nil, err
	}
	return render(deploy.ServiceTemplate, map[string]string{"Instance": instance})
}

// UnitPath is <Dir>/<name>.service.
func (s Systemd) UnitPath() string { return filepath.Join(s.Dir, s.unit()) }

// Register writes the unit, runs daemon-reload, and enables and starts it.
func (s Systemd) Register(ctx context.Context, unit []byte) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(s.UnitPath(), unit, 0o644); err != nil {
		return err
	}
	if err := s.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return s.Run(ctx, "systemctl", "--user", "enable", "--now", s.unit())
}

// Start runs systemctl start, which waits for READY=1 (Type=notify).
func (s Systemd) Start(ctx context.Context) error {
	return s.Run(ctx, "systemctl", "--user", "start", s.unit())
}

// Stop runs systemctl stop, which waits for the process to exit.
func (s Systemd) Stop(ctx context.Context) error {
	return s.Run(ctx, "systemctl", "--user", "stop", s.unit())
}

// Launchd is the launchd per-user agent manager.
type Launchd struct {
	// Dir is ~/Library/LaunchAgents.
	Dir   string
	Label string
	UID   int
	Run   Runner
}

func (l Launchd) domain() string  { return fmt.Sprintf("gui/%d", l.UID) }
func (l Launchd) service() string { return l.domain() + "/" + l.Label }

// Unit renders deploy/loop.plist.tmpl.
func (l Launchd) Unit(instance string) ([]byte, error) {
	if err := checkInstancePath(instance); err != nil {
		return nil, err
	}
	return render(deploy.PlistTemplate, map[string]string{"Instance": instance, "Label": l.Label})
}

// UnitPath is <Dir>/<label>.plist.
func (l Launchd) UnitPath() string { return filepath.Join(l.Dir, l.Label+".plist") }

// Register writes the plist and loads it, which starts the agent
// (RunAtLoad).
func (l Launchd) Register(ctx context.Context, unit []byte) error {
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(l.UnitPath(), unit, 0o644); err != nil {
		return err
	}
	_ = l.Run(ctx, "launchctl", "bootout", l.service()) // a first install has nothing loaded
	return l.Run(ctx, "launchctl", "bootstrap", l.domain(), l.UnitPath())
}

// Start loads the agent, which starts it. An agent that is still loaded
// is started with kickstart.
func (l Launchd) Start(ctx context.Context) error {
	err := l.Run(ctx, "launchctl", "bootstrap", l.domain(), l.UnitPath())
	if err == nil {
		return nil
	}
	if kerr := l.Run(ctx, "launchctl", "kickstart", l.service()); kerr != nil {
		return errors.Join(err, kerr)
	}
	return nil
}

// Stop unloads the agent, which stops it.
func (l Launchd) Stop(ctx context.Context) error {
	if err := l.Run(ctx, "launchctl", "bootout", l.service()); err != nil && !strings.Contains(err.Error(), "No such process") {
		return err
	}
	return nil
}

// HostServiceManager is the manager of this host for the instance.
func HostServiceManager(instance string) (ServiceManager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	name := ServiceName(instance)
	switch runtime.GOOS {
	case "linux":
		return Systemd{Dir: filepath.Join(home, ".config", "systemd", "user"), Name: name, Run: ExecRunner}, nil
	case "darwin":
		return Launchd{Dir: filepath.Join(home, "Library", "LaunchAgents"), Label: "dev.loop." + name, UID: os.Getuid(), Run: ExecRunner}, nil
	}
	return nil, errors.New("release: loop install supports systemd on Linux and launchd on macOS")
}
