package github

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// requires answers branch rules that require each context, from any
// source, and loop/eligible from the loop App.
func requires(contexts ...any) []any {
	checks := []any{map[string]any{"context": EligibleContext, "integration_id": testAppID}}
	for _, c := range contexts {
		if s, ok := c.(string); ok {
			c = map[string]any{"context": s}
		}
		checks = append(checks, c)
	}
	return []any{map[string]any{"type": "pull_request"},
		map[string]any{"type": "required_status_checks", "parameters": map[string]any{"required_status_checks": checks}}}
}

// Parity 10.8, alpha premerge.test.ts L122 "every required check still
// has a job defined in ci.yml": loop doctor fails when the default
// branch requires a check that no workflow job on that branch reports.
// Such a check never reports on a PR head, so no review can pass.
func TestDoctorFailsOnARequiredCheckNoWorkflowJobReports(t *testing.T) {
	cases := map[string]struct {
		rules     []any
		workflows map[string]string
		ok        bool
		detail    []string // each must be in the line's detail
	}{
		"a renamed job": {requires("ci"), map[string]string{"ci.yml": "jobs:\n  gates:\n    runs-on: ubuntu-latest\n"},
			false, []string{`"ci"`, "gates"}},
		"the job id":   {requires("ci"), map[string]string{"ci.yml": "jobs:\n  ci:\n    runs-on: ubuntu-latest\n"}, true, nil},
		"the job name": {requires("ci"), map[string]string{"ci.yml": "jobs:\n  build:\n    name: ci\n"}, true, nil},
		"the job name wins over its id": {requires("build"), map[string]string{"ci.yml": "jobs:\n  build:\n    name: ci\n"},
			false, []string{`"build"`}},
		"a job in a second workflow": {requires("ci", "lint"), map[string]string{
			"ci.yml": "jobs:\n  ci: {}\n", "lint.yaml": "jobs:\n  lint: {}\n"}, true, nil},
		"one of two missing": {requires("ci", "lint"), map[string]string{"ci.yml": "jobs:\n  ci: {}\n"},
			false, []string{`"lint"`}},
		"a matrix job": {requires("test (ubuntu-latest, 1.26)"),
			map[string]string{"ci.yml": "jobs:\n  test:\n    strategy:\n      matrix:\n        os: [ubuntu-latest]\n"}, true, nil},
		"a matrix suffix on a job with no matrix": {requires("test (ubuntu-latest)"),
			map[string]string{"ci.yml": "jobs:\n  test: {}\n"}, false, []string{`"test (ubuntu-latest)"`}},
		"an expression in the name": {requires("test on ubuntu"),
			map[string]string{"ci.yml": "jobs:\n  t:\n    name: test on ${{ matrix.os }}\n    strategy:\n      matrix:\n        os: [ubuntu]\n"}, true, nil},
		"a reusable workflow": {requires("ci / lint"),
			map[string]string{"ci.yml": "jobs:\n  ci:\n    uses: ./.github/workflows/lint.yml\n"}, true, nil},
		// Another App reports its own check, never a workflow job.
		"a check from another app": {requires("ci", map[string]any{"context": "deploy", "integration_id": 999}),
			map[string]string{"ci.yml": "jobs:\n  ci: {}\n"}, true, nil},
		"a check from github actions": {requires(map[string]any{"context": "deploy", "integration_id": 15368}),
			map[string]string{"ci.yml": "jobs:\n  ci: {}\n"}, false, []string{`"deploy"`}},
		// With no workflow the required checks come from another CI.
		"no workflows": {requires("ci"), nil, true, []string{"no workflow"}},
		// With no rulesets the loop requires no check by name.
		"no rulesets on the plan": {nil, map[string]string{"ci.yml": "jobs:\n  gates: {}\n"}, true, []string{"no rulesets"}},
		"a workflow that does not parse": {requires("ci"), map[string]string{"ci.yml": "jobs: [\n"},
			false, []string{".github/workflows/ci.yml"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.api.SetRules(c.rules)
			for f, body := range c.workflows {
				e.api.World.SetFile("main", ".github/workflows/"+f, body)
			}
			var line *CheckLine
			lines := e.svc.Doctor(context.Background(), e.project)
			if i := slices.IndexFunc(lines, func(l CheckLine) bool { return l.Check == "required_jobs" }); i >= 0 {
				line = &lines[i]
			}
			if line == nil {
				t.Fatalf("doctor has no required_jobs line: %+v", lines)
			}
			if line.OK != c.ok {
				t.Fatalf("required_jobs ok = %v, want %v: %s", line.OK, c.ok, line.Detail)
			}
			for _, d := range c.detail {
				if !strings.Contains(line.Detail, d) {
					t.Errorf("detail %q lacks %q", line.Detail, d)
				}
			}
		})
	}
}
