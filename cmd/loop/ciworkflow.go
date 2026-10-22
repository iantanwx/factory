package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// CIWorkflowPath is the workflow `loop init` commits to a repository whose
// default branch has no CI, and CIJob is the job the loop ruleset requires.
const (
	CIWorkflowPath = ".github/workflows/loop-ci.yml"
	CIJob          = "ci"
)

// ciWorkflow writes a CI workflow for the clone at root: it finds the
// stack from the files there and runs its usual checks. It is a starting
// point the operator can edit; only the job name must stay.
func ciWorkflow(root, branch string) []byte {
	var b strings.Builder
	b.WriteString(`# loop init made this file. The loop ruleset requires the ci job, so keep
# its name; change the steps as the project needs.
name: loop-ci

on:
  pull_request:
  push:
    branches: [` + branch + `]
  merge_group:

jobs:
  ci:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
`)
	for _, step := range ciSteps(root) {
		b.WriteString("      - " + strings.ReplaceAll(step, "\n", "\n        ") + "\n")
	}
	return []byte(b.String())
}

// ciSteps are the steps after checkout, each one YAML step without its
// leading "- ".
func ciSteps(root string) []string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	switch {
	case has("go.mod"):
		return []string{"uses: actions/setup-go@v6\nwith:\n  go-version-file: go.mod", "run: go vet ./...", "run: go test ./..."}
	case has("package.json"):
		return nodeSteps(root, has)
	case has("Cargo.toml"):
		return []string{"run: cargo test"}
	case has("pyproject.toml") || has("requirements.txt"):
		return pythonSteps(has)
	case makeHasTest(filepath.Join(root, "Makefile")):
		return []string{"run: make test"}
	}
	return []string{`run: echo "loop init found no build or test command; add one to .github/workflows/loop-ci.yml"`}
}

func nodeSteps(root string, has func(string) bool) []string {
	var steps []string
	pm, install := "npm", "npm install"
	switch {
	case has("bun.lock") || has("bun.lockb"):
		pm, install = "bun", "bun install"
		steps = append(steps, "uses: oven-sh/setup-bun@v2")
	case has("pnpm-lock.yaml"):
		pm, install = "pnpm", "pnpm install --frozen-lockfile"
	case has("yarn.lock"):
		pm, install = "yarn", "yarn install"
	case has("package-lock.json"):
		install = "npm ci"
	}
	if pm != "bun" {
		steps = append(steps, "uses: actions/setup-node@v6\nwith:\n  node-version: lts/*")
	}
	if pm == "pnpm" || pm == "yarn" {
		steps = append(steps, "run: corepack enable")
	}
	steps = append(steps, "run: "+install)
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		_ = json.Unmarshal(data, &pkg)
	}
	for _, s := range []string{"lint", "typecheck", "test", "build"} {
		if _, ok := pkg.Scripts[s]; ok {
			steps = append(steps, "run: "+pm+" run "+s)
		}
	}
	return steps
}

func pythonSteps(has func(string) bool) []string {
	tests := has("tests") || has("test")
	if has("uv.lock") {
		steps := []string{"uses: astral-sh/setup-uv@v6", "run: uv sync"}
		if tests {
			steps = append(steps, "run: uv run --with pytest pytest")
		}
		return steps
	}
	steps := []string{"uses: actions/setup-python@v6\nwith:\n  python-version: \"3.x\""}
	if has("requirements.txt") {
		steps = append(steps, "run: pip install -r requirements.txt")
	}
	if has("pyproject.toml") {
		steps = append(steps, "run: pip install -e .")
	}
	if tests {
		steps = append(steps, "run: pip install pytest && pytest")
	} else {
		steps = append(steps, "run: python -m compileall -q .")
	}
	return steps
}

var makeTest = regexp.MustCompile(`(?m)^test\s*:`)

func makeHasTest(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && makeTest.Match(data)
}
