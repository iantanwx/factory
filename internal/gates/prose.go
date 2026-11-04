package gates

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
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
// over the project's agent-facing documents. Each linter reads a copy of
// the documents with the project's prose exemptions blanked out.
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
	tree := filepath.Join(dir, "tree")
	var exempt []config.ProseExempt
	if r.Config != nil {
		exempt = r.Config.Prose.Exempt
	}
	var findings []Finding
	for _, set := range proseSets {
		files, err := r.glob(set.globs)
		if err != nil {
			return nil, err
		}
		if files, err = r.copyProse(tree, files, exempt); err != nil {
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
			c.Dir, c.Stdout, c.Stderr = tree, r.Out, r.Out
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

// copyProse writes each file under tree at its own relative path, with
// each exempt region blanked, so a finding keeps its line number. It
// returns the files that no entry exempts whole.
func (r *Repo) copyProse(tree string, files []string, exempt []config.ProseExempt) ([]string, error) {
	var out []string
	for _, f := range files {
		var regions []string
		whole := false
		for _, e := range exempt {
			if e.Path == filepath.ToSlash(f) {
				whole = whole || e.Region == ""
				regions = append(regions, e.Region)
			}
		}
		if whole {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.Root, f))
		if err != nil {
			return nil, err
		}
		lines := strings.SplitAfter(string(data), "\n")
		for _, region := range regions {
			blankRegion(lines, region)
		}
		dst := filepath.Join(tree, f)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, []byte(strings.Join(lines, "")), 0o600); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// blankRegion empties each line from a <!-- BEGIN:<region> --> line to the
// next <!-- END:<region> --> line, both included. A BEGIN line with no END
// line after it blanks nothing.
func blankRegion(lines []string, region string) {
	begin, end := "<!-- BEGIN:"+region+" -->", "<!-- END:"+region+" -->"
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != begin {
			continue
		}
		j := slices.IndexFunc(lines[i:], func(l string) bool { return strings.TrimSpace(l) == end })
		if j < 0 {
			return
		}
		for k := i; k <= i+j; k++ {
			lines[k] = "\n"
		}
		i += j
	}
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
