package main

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/gitx/gittest"
)

// gatesCheckout makes a git checkout whose config names lint, test and
// build, makes it the working directory, and returns its root. lint ratchets
// against lint.json, written from lint when lint is not empty. Each other
// command touches ran, so a test can tell that a gate ran.
func gatesCheckout(t *testing.T, lintRun, lint string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		".loop/config.yaml": "merge:\n  mechanism: direct\ngates:\n" +
			"  - name: lint\n    run: " + lintRun + "\n    baseline: lint.json\n" +
			"  - name: test\n    run: touch ran\n" +
			"  - name: build\n    run: touch ran\n",
	}
	if lint != "" {
		files["lint.json"] = lint
	}
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "chore: start"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
	} {
		c := gittest.Command(args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(root)
	return root
}

// C13: the verb refuses --update with no --only, --update on a gate with
// no baseline to lower, and --full with any selection but --only audit,
// before any gate runs.
func TestC13GatesRefusesAFlagThatCannotApply(t *testing.T) {
	cases := []struct {
		name  string
		flags map[string]string
		rule  string
	}{
		{"no --only", map[string]string{"update": "true"}, "update_needs_only"},
		{"a stack gate with no baseline", map[string]string{"update": "true", "only": "test"}, "no_baseline"},
		{"a loop gate that is not standards", map[string]string{"update": "true", "only": "commits"}, "no_baseline"},
		{"--full with no --only", map[string]string{"full": "true"}, "full_needs_audit"},
		{"--full on another gate", map[string]string{"full": "true", "only": "lint"}, "full_needs_audit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := gatesCheckout(t, "touch ran", "")
			res := gatesVerb(context.Background(), cli.Invocation{Flags: c.flags})
			if res.Refusal == nil || res.Refusal.Code != cli.CodeUsage || res.Refusal.Rule != c.rule {
				t.Fatalf("result = %+v, want USAGE %s", res.Refusal, c.rule)
			}
			for _, f := range []string{"ran", "lint.json"} {
				if _, err := os.Stat(filepath.Join(root, f)); !os.IsNotExist(err) {
					t.Fatalf("%s exists after the refusal: %v", f, err)
				}
			}
		})
	}
}

// C9: loop gates --only lint --update lowers the baseline of lint, and the
// gate passes against what it wrote.
func TestC9GatesUpdateLowersTheBaseline(t *testing.T) {
	root := gatesCheckout(t, `printf 'a::r\nloop:end\n'`, `{"allowed": {"a::r": 3, "b::r": 1}}`)
	res := gatesVerb(context.Background(), cli.Invocation{Flags: map[string]string{"update": "true", "only": "lint"}})
	if res.Refusal != nil {
		t.Fatalf("refusal = %+v, want a pass", res.Refusal)
	}
	raw, err := os.ReadFile(filepath.Join(root, "lint.json"))
	if want := "{\n  \"allowed\": {\n    \"a::r\": 1\n  }\n}\n"; err != nil || string(raw) != want {
		t.Fatalf("lint.json = %s, %v; want\n%s", raw, err, want)
	}
}

// C14: loop gates --only audit --full runs the audit command in a checkout
// whose change touches no lock file, and ratchets its ids against the
// accepted set.
func TestC14GatesFullAuditsWithNoChangedLockFile(t *testing.T) {
	cases := []struct {
		name     string
		accepted string
		pass     bool
	}{
		{"an accepted id", `{"accepted": {"GHSA-1": {"reason": "no fix upstream", "date": "2026-10-24"}}}`, true},
		{"no accepted set", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := gatesCheckout(t, "touch ran", "")
			files := map[string]string{".loop/config.yaml": "merge:\n  mechanism: direct\ngates:\n" +
				"  - name: audit\n    run: touch audited; printf 'GHSA-1\\nloop:end\\n'\n    baseline: audit.json\n"}
			if c.accepted != "" {
				files["audit.json"] = c.accepted
			}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			res := gatesVerb(context.Background(), cli.Invocation{Flags: map[string]string{"only": "audit", "full": "true"}})
			if c.pass && res.Refusal != nil {
				t.Fatalf("refusal = %+v, want a pass", res.Refusal)
			}
			if !c.pass && (res.Refusal == nil || res.Refusal.Code != cli.CodeGatesFailed || res.Refusal.Rule != "audit") {
				t.Fatalf("refusal = %+v, want GATES_FAILED audit", res.Refusal)
			}
			if _, err := os.Stat(filepath.Join(root, "audited")); err != nil {
				t.Fatalf("the audit command did not run: %v", err)
			}
		})
	}
}

// C15: the scheduled workflow runs on a schedule and by hand, never on a
// pull request or a push, and its one loop step audits every dependency.
func TestC15ScheduledWorkflowAuditsTheBaseAndNothingElse(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "audit.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Steps []struct {
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(w.On)); !slices.Equal(got, []string{"schedule", "workflow_dispatch"}) {
		t.Fatalf("triggers = %v, want [schedule workflow_dispatch]", got)
	}
	var runs []string
	built := false
	for _, j := range w.Jobs {
		for _, s := range j.Steps {
			built = built || s.Uses == "./"
			if strings.Contains(s.Run, "loop ") {
				runs = append(runs, strings.TrimSpace(s.Run))
				if !built {
					t.Fatalf("step %q runs loop before the local action builds it", s.Run)
				}
			}
		}
	}
	if !slices.Equal(runs, []string{"loop gates --only audit --full"}) {
		t.Fatalf("loop steps = %q, want one step that runs loop gates --only audit --full", runs)
	}
}
