package gates

import (
	"context"
	"fmt"
	"maps"
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
// A command with no baseline passes on exit 0. A command with a baseline
// prints keys on stdout and ends them with EndLine; its stderr streams to
// the log, and ratchetStack holds the keys to the baseline.
//
// audit runs only when the change touches a lock file, unless r.Full is
// set. An advisory database moves on its own, so a blanket audit fails a
// change for news its author cannot fix. A change to the dependency graph
// owns what it brings in. The scheduled audit sets r.Full to read that
// news on the base.
func stackGate(name string) func(context.Context, *Repo) ([]Finding, error) {
	return func(ctx context.Context, r *Repo) ([]Finding, error) {
		if r.Config == nil {
			return []Finding{{File: ".loop/config.yaml", Rule: "no_config", Message: "the project has no loop config; say \"set up my loop\" in a harness session"}}, nil
		}
		i := slices.IndexFunc(r.Config.Gates, func(c config.Command) bool { return c.Name == name })
		if i < 0 {
			return []Finding{{File: ".loop/config.yaml", Rule: "gate_missing", Message: "gates names no " + name + " command; every project runs " + strings.Join(StackGates, ", ")}}, nil
		}
		if name == "audit" && !r.Full {
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
		if r.Config.Gates[i].Baseline != "" {
			var stdout strings.Builder
			c.Stdout = &stdout
			err := c.Run()
			if _, ok := err.(*exec.ExitError); err != nil && !ok {
				return nil, err
			}
			return ratchetStack(r, r.Config.Gates[i], stdout.String(), err)
		}
		if err := c.Run(); err != nil {
			if _, ok := err.(*exec.ExitError); ok {
				return []Finding{{Rule: "command_failed", Message: r.Config.Gates[i].Run + ": " + err.Error()}}, nil
			}
			return nil, err
		}
		return nil, nil
	}
}

// ratchetStack holds the keys of a ratcheted gate's stdout to the gate's
// baseline. The exit code decides nothing: oxlint exits 1 and govulncheck
// exits 3 when they find something. The end line tells a clean run from a
// silent one, and exit names the exit status in that finding. With
// r.Update, it first lowers the baseline to the keys it found.
func ratchetStack(r *Repo, gate config.Command, stdout string, exit error) ([]Finding, error) {
	keys, ok, bad := parseKeys(stdout)
	if !ok {
		msg := fmt.Sprintf("%s: the last non-blank line of stdout is not %s, so the gate cannot tell a clean run from a silent one", gate.Run, EndLine)
		if exit != nil {
			msg += " (" + exit.Error() + ")"
		}
		return []Finding{{Rule: "no_end_line", Message: msg}}, nil
	}
	if gate.Name == "audit" {
		findings, err := ratchetAudit(r, gate.Baseline, keys)
		return append(bad, findings...), err
	}
	baseline, exists, err := readCounts(r.Root, gate.Baseline)
	if err != nil {
		return nil, err
	}
	found := Counts{}
	for _, k := range keys {
		found[k]++
	}
	if r.Update {
		if baseline, err = updateCounts(r.Root, gate.Baseline, found, baseline, exists); err != nil {
			return nil, err
		}
		exists = true
	}
	findings := ratchet(found, baseline, gate.Baseline, "new_finding", "findings")
	if !exists {
		for i := range findings {
			findings[i].Message += fmt.Sprintf(". %s does not exist: adopt the ratchet with loop gates --only %s --update", gate.Baseline, gate.Name)
		}
	}
	return append(bad, findings...), nil
}

// ratchetAudit holds the advisory ids that an audit printed to the accepted
// set in file. An audit tool prints one advisory once for each path to the
// package, and that count follows the dependency graph, not the risk. So
// each id counts once.
//
// With r.Update, it deletes each accepted id that the audit no longer
// prints, and keeps every other entry as it is. It never accepts an id: an
// accepted advisory needs a reason that a person writes.
func ratchetAudit(r *Repo, file string, ids []string) ([]Finding, error) {
	accepted, exists, err := readAccepted(r.Root, file)
	if err != nil {
		return nil, err
	}
	var findings []Finding
	found, baseline := Counts{}, Counts{}
	for _, id := range ids {
		found[id] = 1
	}
	if r.Update && !exists {
		_, _ = fmt.Fprintf(r.Out, "%s does not exist, and --update writes no accepted advisory: write each id the project accepts in it by hand, with a reason and a date\n", file)
	}
	if r.Update && exists {
		kept := maps.Clone(accepted)
		maps.DeleteFunc(kept, func(id string, _ Advisory) bool { return found[id] == 0 })
		if len(kept) < len(accepted) {
			if err := writeAccepted(r.Root, file, kept); err != nil {
				return nil, err
			}
			accepted = kept
		}
	}
	for _, id := range slices.Sorted(maps.Keys(accepted)) {
		baseline[id] = 1
		findings = append(findings, accepted[id].faults(id, file)...)
	}
	for _, f := range ratchet(found, baseline, file, "new_advisory", "advisories") {
		id := f.File // an advisory id holds no "::", so ratchet puts all of it in File
		f.File = file
		if f.Rule == "new_advisory" {
			f.Message = fmt.Sprintf("%s is not accepted. Upgrade the package, or accept the advisory in %s with a reason and a date", id, file)
		} else {
			f.Rule, f.Message = "stale_advisory", fmt.Sprintf("%s accepts %s, and the audit no longer prints it. Delete its entry", file, id)
		}
		findings = append(findings, f)
	}
	return findings, nil
}
