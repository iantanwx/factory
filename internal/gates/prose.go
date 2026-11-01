package gates

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/iantanwx/loop/tools/prose"
)

// proseSet is one run of the two linters over a set of files.
type proseSet struct {
	globs []string
	// steDisable is the STE rules that the set turns off.
	steDisable string
}

// proseSets are the agent-facing documents of a project. An agent that runs
// unattended reads them as its whole instruction set, so an ambiguous
// sentence there is a defect in the same sense a type error is. A spec
// repeats the names of the things it specifies, so synonym rotation is off
// there.
var proseSets = []proseSet{
	{globs: []string{
		"AGENTS.md", "CLAUDE.md", "CODING_STANDARDS.md", ".loop/policy/*.md",
		"docs/adr/*.md", "docs/agents/*.md", "docs/commands/*.md", "docs/rules/*.md", "docs/templates/*.md",
		"skills/*/SKILL.md", ".claude/agents/*.md",
	}},
	{globs: append([]string{"docs/prd/*.md"}, specGlobs()...), steDisable: "synonym-rotation"},
}

// Prose runs the STE linter and the unslop linter, which the loop embeds,
// over the project's agent-facing documents.
func Prose(ctx context.Context, r *Repo) ([]Finding, error) {
	py, err := exec.LookPath("python3")
	if err != nil {
		return nil, errors.New("python3 is not on PATH")
	}
	dir, err := os.MkdirTemp("", "loop-prose-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ste, unslop := filepath.Join(dir, "ste-lint.py"), filepath.Join(dir, "unslop-lint.py")
	if err := os.WriteFile(ste, prose.STELint, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(unslop, prose.UnslopLint, 0o600); err != nil {
		return nil, err
	}
	var findings []Finding
	for _, set := range proseSets {
		files, err := r.glob(set.globs)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			continue
		}
		steArgs := []string{"-I", ste}
		if set.steDisable != "" {
			steArgs = append(steArgs, "--disable", set.steDisable)
		}
		for _, run := range []struct {
			rule string
			args []string
		}{
			{"ste", append(steArgs, files...)},
			{"unslop", append([]string{"-I", unslop}, files...)},
		} {
			c := exec.CommandContext(ctx, py, run.args...)
			c.Dir, c.Stdout, c.Stderr = r.Root, r.Out, r.Out
			if err := c.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					return nil, err
				}
				findings = append(findings, Finding{Rule: run.rule, Message: "the lines above name each sentence to rewrite"})
			}
		}
	}
	return findings, nil
}

// glob lists the tracked and new files of the checkout that match globs,
// relative to the root, in order.
func (r *Repo) glob(globs []string) ([]string, error) {
	var files []string
	for _, g := range globs {
		m, err := filepath.Glob(filepath.Join(r.Root, g))
		if err != nil {
			return nil, err
		}
		for _, f := range m {
			rel, err := filepath.Rel(r.Root, f)
			if err != nil {
				return nil, err
			}
			files = append(files, rel)
		}
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}
