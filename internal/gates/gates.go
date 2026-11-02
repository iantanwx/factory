// Package gates is the single definition of "done" in a project: one list
// of gates that an agent runs before it pushes, and that the project's CI
// runs on each pull request with `loop gates`. The loop ships every gate
// that does not depend on the stack. The project's config names a command
// for each stack gate.
//
// The order is fail-fast and cheapest first. commits fetches the base and
// runs two git commands, and goes first. test and build go last.
package gates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// Finding is one fault that a gate found.
type Finding struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

func (f Finding) String() string {
	at := f.File
	if at != "" && f.Line > 0 {
		at = fmt.Sprintf("%s:%d", at, f.Line)
	}
	if at != "" {
		at += ": "
	}
	return at + f.Rule + ": " + f.Message
}

// Repo is the checkout that the gates read.
type Repo struct {
	// Root is the top of the git checkout.
	Root string
	// Base is the ref that the branch is compared with, such as
	// origin/main. A gate that reads "this change" reads Base...HEAD and
	// the working tree.
	Base string
	// Config is the project config in the checkout. Nil when the file is
	// absent.
	Config *config.ProjectConfig
	// Out takes the output of each command that a gate runs.
	Out io.Writer
	// Update lowers each baseline to what the gate found (--update).
	Update bool
	// Full skips the lock-file scope of the audit gate (--full).
	Full bool
}

// Git runs git in the checkout and returns its stdout.
func (r *Repo) Git(ctx context.Context, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = r.Root
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// Changed lists the files that the change touches: the commits since the
// merge base with Base, and the working tree.
func (r *Repo) Changed(ctx context.Context) ([]string, error) {
	var files []string
	for _, args := range [][]string{
		{"diff", "--name-only", r.Base + "...HEAD"},
		{"diff", "--name-only", "HEAD"},
		{"ls-files", "--others", "--exclude-standard"},
	} {
		out, err := r.Git(ctx, args...)
		if err != nil {
			return nil, err
		}
		files = append(files, strings.Fields(out)...)
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

// Gate is one check of the list.
type Gate struct {
	Name string
	// Docs puts the gate in the --docs list, for a stage that writes only
	// Markdown and has no stack installed.
	Docs bool
	// Slow keeps the gate out of the --fast list.
	Slow bool
	// Stack marks a gate whose command the project config names.
	Stack bool
	Check func(ctx context.Context, r *Repo) ([]Finding, error)
}

// StackGates are the gates whose command each project's config names, in
// list order. A stack with no tool for one names a command that checks
// what it can, for example `go vet ./...` as the type check of Go.
var StackGates = []string{"format", "lint", "audit", "typecheck", "test", "build"}

// List is every gate, in the order they run: the loop's own gates, then
// the stack gates. A gate that cfg names beyond the stack gates runs after
// audit and typecheck, before test and build, so --fast keeps it.
func List(cfg *config.ProjectConfig) []Gate {
	gates := []Gate{
		{Name: "commits", Docs: true, Check: Commits},
		{Name: "standards", Docs: true, Check: Standards},
		{Name: "runbooks", Docs: true, Check: Runbooks},
		{Name: "adr", Docs: true, Check: ADR},
		{Name: "prd", Docs: true, Check: PRD},
		{Name: "spec", Docs: true, Check: Spec},
		{Name: "lessons", Docs: true, Check: Lessons},
		{Name: "prose", Docs: true, Check: Prose},
		{Name: "citations", Docs: true, Check: Citations},
		{Name: "requirements", Docs: true, Check: Requirements},
		{Name: "design", Docs: true, Check: Design},
		{Name: "secrets", Check: Secrets},
		{Name: "pixel", Slow: true, Check: Pixel},
	}
	stack := func(name string) Gate {
		return Gate{Name: name, Stack: true, Slow: name == "test" || name == "build", Check: stackGate(name)}
	}
	for _, name := range StackGates {
		if name == "test" && cfg != nil {
			for _, c := range cfg.Gates {
				if !slices.Contains(StackGates, c.Name) && !slices.ContainsFunc(gates, func(g Gate) bool { return g.Name == c.Name }) {
					gates = append(gates, stack(c.Name))
				}
			}
		}
		gates = append(gates, stack(name))
	}
	return gates
}

// Select picks the gates of one list: every gate, --docs, --fast, or the
// one gate that only names.
func Select(cfg *config.ProjectConfig, docs, fast bool, only string) ([]Gate, error) {
	var out []Gate
	for _, g := range List(cfg) {
		switch {
		case only != "":
			if g.Name == only {
				return []Gate{g}, nil
			}
		case docs && !g.Docs, fast && g.Slow:
		default:
			out = append(out, g)
		}
	}
	if only != "" {
		return nil, fmt.Errorf("gates: no gate is named %q", only)
	}
	return out, nil
}

// Updatable reports whether --update has a baseline to lower in g: the
// suppressions of standards, or the baseline that a stack gate's command
// names.
func Updatable(cfg *config.ProjectConfig, g Gate) bool {
	return g.Name == "standards" || g.Stack && cfg != nil &&
		slices.ContainsFunc(cfg.Gates, func(c config.Command) bool { return c.Name == g.Name && c.Baseline != "" })
}

// Result is the outcome of one gate.
type Result struct {
	Gate     string    `json:"gate"`
	OK       bool      `json:"ok"`
	Seconds  float64   `json:"seconds"`
	Findings []Finding `json:"findings,omitempty"`
	// Error is set when the gate could not run.
	Error string `json:"error,omitempty"`
}

// Run runs gates in order and stops at the first one that fails. It
// writes a line for each gate, and each finding, to r.Out.
func Run(ctx context.Context, r *Repo, gates []Gate) []Result {
	var results []Result
	for _, g := range gates {
		_, _ = fmt.Fprintf(r.Out, "== %s\n", g.Name)
		start := time.Now()
		findings, err := g.Check(ctx, r)
		res := Result{Gate: g.Name, OK: err == nil && len(findings) == 0, Seconds: time.Since(start).Round(time.Millisecond).Seconds(), Findings: findings}
		if err != nil {
			res.Error = err.Error()
			_, _ = fmt.Fprintf(r.Out, "%s could not run: %v\n", g.Name, err)
		}
		for _, f := range findings {
			_, _ = fmt.Fprintln(r.Out, f)
		}
		results = append(results, res)
		if !res.OK {
			_, _ = fmt.Fprintf(r.Out, "FAIL %s\n", g.Name)
			return results
		}
	}
	return results
}

// Open finds the checkout around dir, its base and its project config.
// An empty base reads GITHUB_BASE_REF in a pull request's CI, then the
// remote's default branch.
func Open(ctx context.Context, dir, base string, out io.Writer) (*Repo, error) {
	r := &Repo{Root: dir, Out: out}
	top, err := r.Git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	r.Root = strings.TrimSpace(top)
	if base == "" {
		base = defaultBase(ctx, r)
	}
	r.Base = base
	raw, err := os.ReadFile(r.Root + "/" + config.ProjectConfigPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if r.Config, err = config.ParseProjectConfig(raw); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func defaultBase(ctx context.Context, r *Repo) string {
	if ref := os.Getenv("GITHUB_BASE_REF"); ref != "" {
		return "origin/" + ref
	}
	if out, err := r.Git(ctx, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimSpace(out)
	}
	return "origin/main"
}
