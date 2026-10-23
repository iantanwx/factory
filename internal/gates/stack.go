package gates

import (
	"context"
	"os/exec"
	"path"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
)

// lockfiles are the dependency lock files that the audit gate reads as a
// change to the dependency graph.
var lockfiles = []string{
	"go.sum", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb",
	"uv.lock", "poetry.lock", "Pipfile.lock", "requirements.txt", "Cargo.lock", "Gemfile.lock",
	"composer.lock", "mix.lock", "pubspec.lock", "Package.resolved",
}

// stackGate runs the command that the project config names for the gate.
// A config with no such command fails the gate: every project runs every
// stack gate.
//
// audit runs only when the change touches a lock file. An advisory
// database moves on its own, so a blanket audit fails a change for news
// its author cannot fix. A change to the dependency graph owns what it
// brings in.
func stackGate(name string) func(context.Context, *Repo) ([]Finding, error) {
	return func(ctx context.Context, r *Repo) ([]Finding, error) {
		if r.Config == nil {
			return []Finding{{File: ".loop/config.yaml", Rule: "no_config", Message: "the project has no loop config; say \"set up my loop\" in a harness session"}}, nil
		}
		i := slices.IndexFunc(r.Config.Gates, func(c config.Command) bool { return c.Name == name })
		if i < 0 {
			return []Finding{{File: ".loop/config.yaml", Rule: "gate_missing", Message: "gates names no " + name + " command; every project runs " + strings.Join(StackGates, ", ")}}, nil
		}
		if name == "audit" {
			changed, err := r.Changed(ctx)
			if err != nil {
				return nil, err
			}
			if !slices.ContainsFunc(changed, func(f string) bool { return slices.Contains(lockfiles, path.Base(f)) }) {
				_, _ = r.Out.Write([]byte("no lock file changed\n"))
				return nil, nil
			}
		}
		c := exec.CommandContext(ctx, "sh", "-c", r.Config.Gates[i].Run)
		c.Dir, c.Stdout, c.Stderr = r.Root, r.Out, r.Out
		if err := c.Run(); err != nil {
			if _, ok := err.(*exec.ExitError); ok {
				return []Finding{{Rule: "command_failed", Message: r.Config.Gates[i].Run + ": " + err.Error()}}, nil
			}
			return nil, err
		}
		return nil, nil
	}
}
