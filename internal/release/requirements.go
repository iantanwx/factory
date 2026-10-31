package release

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/config"
)

// Level says which hosts need a binary.
type Level string

const (
	// Required: a host that lacks it cannot run the loop.
	Required Level = "required"
	// Optional: a host runs without it, and it adds one thing.
	Optional Level = "optional"
	// CIOnly: only the CI job runs it.
	CIOnly Level = "ci"
	// DevOnly: only a person who builds loop runs it.
	DevOnly Level = "dev"
)

// Requirement is one binary that the loop runs. The judges of the triage
// panel are not rows: the judge_binaries line checks them.
type Requirement struct {
	Bin string
	// Need is what runs the binary. For an optional binary, it is what the
	// binary adds.
	Need  string
	Level Level
	// OS is the one system whose hosts run the binary, or empty for each
	// system.
	OS   string
	Hint string
	// Home is where the binary's own installer puts it, under the home
	// directory and off the path. Empty when it has no such place.
	Home string
	// Server is true when the server process runs the binary. Only host
	// CLI verbs run the others, with the operator's own path.
	Server bool
}

// Requirements is every binary that the loop's non-test code runs. Each
// row names a version only where go.mod or the README states one.
var Requirements = []Requirement{
	{Bin: "git", Level: Required, Need: "every checkout, mirror and gate", Server: true,
		Hint: "install git with the system package manager"},
	{Bin: "python3", Level: Required, Need: "the prose rules of loop gates and of each body the server checks", Server: true,
		Hint: "install Python 3 with the system package manager"},
	{Bin: "orca", Level: Required, Need: "the agent host, which runs each run's worktree and terminal",
		Hint: "install the Orca app; it places its CLI at ~/.config/orca/cli/bin/orca", Home: ".config/orca/cli/bin/orca", Server: true},
	{Bin: "gh", Level: Required, Need: "loop register, and the release reads of loop upgrade",
		Hint: "install the GitHub CLI from https://cli.github.com, then run gh auth login"},
	{Bin: "systemctl", Level: Required, OS: "linux", Need: "the systemd user service of loop install",
		Hint: "run the loop on a Linux host with systemd"},
	{Bin: "systemd-run", Level: Required, OS: "linux", Need: "the upgrade and rollback that the coordinator starts outside its session",
		Hint: "run the loop on a Linux host with systemd"},
	{Bin: "launchctl", Level: Required, OS: "darwin", Need: "the launchd agent of loop install",
		Hint: "launchctl is part of macOS; put /bin on the path"},
	{Bin: "loginctl", Level: Optional, OS: "linux", Need: "linger, so the service keeps running after you log out",
		Hint: "run the loop on a Linux host with systemd-logind"},
	{Bin: "tailscale", Level: Optional, Need: "the tailnet address for the GitHub App flow of loop install, so a browser on another machine reaches it",
		Hint: "install Tailscale from https://tailscale.com/download"},
	{Bin: "op", Level: Optional, Need: "loop secret import, which reads each secret from 1Password", Server: true,
		Hint: "install the 1Password CLI from https://developer.1password.com/docs/cli"},
	{Bin: "gitleaks", Level: CIOnly, Need: "the secrets gate of loop gates",
		Hint: "install gitleaks from https://github.com/gitleaks/gitleaks/releases"},
	{Bin: "go", Level: DevOnly, Need: "the build of loop",
		Hint: "install Go 1.26 from https://go.dev/dl"},
	{Bin: "make", Level: DevOnly, Need: "make gates, make proto and make build",
		Hint: "install make with the system package manager"},
	{Bin: "buf", Level: DevOnly, Need: "make proto, which regenerates gen/",
		Hint: "install buf 1.71 from https://buf.build/docs/installation"},
	{Bin: "golangci-lint", Level: DevOnly, Need: "make lint",
		Hint: "install golangci-lint v2 from https://golangci-lint.run"},
	{Bin: "bd", Level: DevOnly, Need: "the parity suite of internal/beadsx",
		Hint: "set LOOP_BD to the pinned bd; loop install places it in the instance's bin directory"},
	{Bin: "dolt", Level: DevOnly, Need: "the parity suite of internal/beadsx",
		Hint: "set LOOP_DOLT to the pinned dolt; loop install places it in the instance's bin directory"},
}

// HostRequirements are the rows that a host of goos checks: each required
// and optional row for that system.
func HostRequirements(goos string) []Requirement {
	var out []Requirement
	for _, r := range Requirements {
		if (r.Level == Required || r.Level == Optional) && (r.OS == "" || r.OS == goos) {
			out = append(out, r)
		}
	}
	return out
}

// ServerRequirements are the host rows of goos that the server process
// runs. The overwatch part server checks these on the service's path.
func ServerRequirements(goos string) []Requirement {
	var out []Requirement
	for _, r := range HostRequirements(goos) {
		if r.Server {
			out = append(out, r)
		}
	}
	return out
}

// HostFinder finds a row's binary on the path, or where its installer puts
// it under home. When host config names the agent host's binary, orca is
// that binary, because the server runs that one.
func HostFinder(home string, cfg *config.HostConfig) func(Requirement) bool {
	return func(r Requirement) bool {
		if r.Bin == "orca" && cfg != nil && cfg.AgentHost.Binary != "" {
			_, err := exec.LookPath(cfg.AgentHost.Binary)
			return err == nil
		}
		if _, err := exec.LookPath(r.Bin); err == nil {
			return true
		}
		if r.Home == "" || home == "" {
			return false
		}
		_, err := exec.LookPath(filepath.Join(home, r.Home))
		return err == nil
	}
}

// RequirementLines is one line requires_<bin> for each row. A missing
// required binary fails its line. A missing optional one passes.
func RequirementLines(rows []Requirement, find func(Requirement) bool) []DoctorLine {
	var lines []DoctorLine
	for _, r := range rows {
		l := DoctorLine{Check: "requires_" + r.Bin, OK: true}
		switch {
		case find(r):
			l.Detail = fmt.Sprintf("%s is installed, for %s", r.Bin, r.Need)
		case r.Level == Optional:
			l.Detail = fmt.Sprintf("%s is optional and not on the path; it would add %s: %s", r.Bin, r.Need, r.Hint)
		default:
			l.OK, l.Detail = false, fmt.Sprintf("put %s on the path, for %s: %s", r.Bin, r.Need, r.Hint)
		}
		lines = append(lines, l)
	}
	return lines
}

// checkRequired refuses when a required row's binary is missing, and names
// each one with its hint.
func checkRequired(rows []Requirement, find func(Requirement) bool) error {
	var missing []string
	for _, r := range rows {
		if r.Level == Required && !find(r) {
			missing = append(missing, fmt.Sprintf("%s (%s)", r.Bin, r.Hint))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return refuse(CodeMissingBinary, "missing_binary", "install each missing binary, then run loop install again: %s", strings.Join(missing, "; "))
}
