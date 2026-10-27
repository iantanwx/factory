//go:build acceptance

package scenarios

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gates"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// The F1 fixture project. Its test gate fails until greeting.txt holds
// the greeting, so the code-reviewer can prove the check red at the merge
// base. Every project runs every stack gate, so the others run true. The
// job runs the compiled binary outside this directory, so the files are
// inline. Each stage fires once a year, so a tick on the real clock starts
// no run that the scenario does not dispatch.
const (
	f1Check  = "#!/bin/sh\ntest \"$(cat greeting.txt 2>/dev/null)\" = \"hello, world\"\n"
	f1Config = `merge:
  mechanism: direct
gates:
  - name: format
    run: "true"
  - name: lint
    run: "true"
  - name: audit
    run: "true"
  - name: typecheck
    run: "true"
  - name: test
    run: sh check.sh
  - name: build
    run: "true"
stages:
  implementer:
    schedule: "0 0 1 1 *"
  code-reviewer:
    schedule: "0 0 1 1 *"
`
)

// The real implementer of F1 runs loop gates before it pushes, and a stack
// gate that the fixture config names no command for refuses the push. So
// the fixture passes every stack gate once greeting.txt holds the greeting.
// This runs with no orca, so CI proves the fixture the lane runs.
func TestF1FixtureConfigPassesEveryStackGate(t *testing.T) {
	dir := t.TempDir()
	gt.Git(t, dir, "init", "-q", "-b", "main")
	base := gt.Commit(t, dir, "seed the fixture project", map[string]string{config.ProjectConfigPath: f1Config, "check.sh": f1Check})
	gt.Commit(t, dir, "add the greeting", map[string]string{"greeting.txt": "hello, world\n"})
	cfg, err := config.ParseProjectConfig([]byte(f1Config))
	if err != nil {
		t.Fatal(err)
	}
	var stack []gates.Gate
	for _, g := range gates.List(cfg) {
		if g.Stack {
			stack = append(stack, g)
		}
	}
	var out strings.Builder
	results := gates.Run(context.Background(), &gates.Repo{Root: dir, Base: base, Config: cfg, Out: &out}, stack)
	var passed []string
	for _, r := range results {
		if r.OK {
			passed = append(passed, r.Gate)
		}
	}
	if want := []string{"format", "lint", "audit", "typecheck", "test", "build"}; !slices.Equal(passed, want) {
		t.Fatalf("the F1 fixture passed the stack gates %q, want %q\n%s", passed, want, out.String())
	}
}
