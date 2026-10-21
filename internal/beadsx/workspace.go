package beadsx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

// Layout names every beads path under one instance state directory. No beads
// workspace ever lives in a project checkout (KTD3).
type Layout struct {
	StateDir string
}

// Root is the beads directory of the instance.
func (l Layout) Root() string { return filepath.Join(l.StateDir, "beads") }

// Home is the isolated HOME of every bd call. It holds bd's user config.
func (l Layout) Home() string { return filepath.Join(l.Root(), "home") }

// DoltDir is the supervisor's directory (DoltConfig.Dir).
func (l Layout) DoltDir() string { return filepath.Join(l.Root(), "dolt") }

// Workspace is the directory that holds one project's .beads.
func (l Layout) Workspace(project string) string {
	return filepath.Join(l.Root(), "ws", project)
}

// homeConfig is the whole of bd's user config in the isolated HOME. bd 1.3.1
// writes metrics.disabled=false here on its first run when the file is
// missing, and then sends anonymous usage events.
const homeConfig = "metrics:\n    disabled: true\n    notice_shown: true\n"

// EnsureHome writes the isolated bd user config with metrics off. It runs
// before any bd call, so bd never writes its default of metrics on.
func EnsureHome(home string) error {
	dir := filepath.Join(home, ".config", "bd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.yaml")
	if b, err := os.ReadFile(path); err == nil && string(b) == homeConfig {
		return nil
	}
	return writeFileAtomic(path, []byte(homeConfig), 0o600)
}

var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)

// WorkspaceSpec describes one project's beads workspace.
type WorkspaceSpec struct {
	BD       string // path to the pinned bd
	Layout   Layout
	Project  string // the project id; also the issue prefix and the database name
	Socket   string // the supervisor's socket
	Actor    string // empty means "loop"
	Statuses []StatusDef
	Types    []string
}

// workspaceConfig is the configuration every workspace holds. The guard
// checks the effective value of each key.
var workspaceConfig = [][2]string{
	{"events-journal", "true"},
	{"export.auto", "false"},
	{"import.auto", "false"},
	{"backup.enabled", "false"},
	{"dolt.auto-push", "false"},
	{"dolt.auto-start", "false"},
}

// EnsureWorkspace creates the project's workspace under the instance state
// directory, or adopts the one that exists. It configures the custom
// statuses and types, then runs the guard. It is idempotent.
//
// bd init writes a git repository and a .gitignore into the workspace
// directory. --skip-agents and --skip-hooks stop the agent files and the git
// hooks.
func EnsureWorkspace(ctx context.Context, spec WorkspaceSpec) (*BD, error) {
	if !projectPattern.MatchString(spec.Project) {
		return nil, fmt.Errorf("%w: project id %q must match %s", ErrInvalid, spec.Project, projectPattern)
	}
	if spec.Socket == "" {
		return nil, fmt.Errorf("%w: a Dolt socket is required", ErrInvalid)
	}
	home := spec.Layout.Home()
	if err := EnsureHome(home); err != nil {
		return nil, err
	}
	ws := spec.Layout.Workspace(spec.Project)
	if err := os.MkdirAll(ws, 0o700); err != nil {
		return nil, err
	}
	b, err := NewBD(ctx, BDConfig{Binary: spec.BD, Workspace: ws, Home: home, Actor: spec.Actor})
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(ws, ".beads", "metadata.json")); errors.Is(err, os.ErrNotExist) {
		if _, err := b.run(ctx, "init", "--server", "--external", "--server-socket", spec.Socket,
			"--prefix", spec.Project, "--non-interactive", "-q",
			"--skip-agents", "--skip-hooks", "--role", "maintainer", "--init-if-missing"); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	setGitRole(ctx, ws)
	for _, kv := range workspaceConfig {
		if _, err := b.run(ctx, "config", "set", kv[0], kv[1], "--json"); err != nil {
			return nil, err
		}
	}
	if len(spec.Statuses) > 0 {
		if err := b.ConfigureStatuses(ctx, spec.Statuses); err != nil {
			return nil, err
		}
	}
	if len(spec.Types) > 0 {
		if err := b.ConfigureTypes(ctx, spec.Types); err != nil {
			return nil, err
		}
	}
	if err := Guard(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

// setGitRole writes beads.role into the workspace's own git repository. bd
// 1.3.1 warns on every call without it. The warning is harmless, so a
// missing git binary is no error.
func setGitRole(ctx context.Context, ws string) {
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	cmd := exec.CommandContext(ctx, "git", "-C", ws, "config", "beads.role", "maintainer")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + ws, "GIT_CONFIG_NOSYSTEM=1"}
	_ = cmd.Run()
}
