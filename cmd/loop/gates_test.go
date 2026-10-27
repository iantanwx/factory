package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
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
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(root)
	return root
}

// C13: the verb refuses --update with no --only, and --update on a gate
// with no baseline to lower, before any gate runs.
func TestC13GatesRefusesAnUpdateThatCannotApply(t *testing.T) {
	cases := []struct {
		name  string
		flags map[string]string
		rule  string
	}{
		{"no --only", map[string]string{"update": "true"}, "update_needs_only"},
		{"a stack gate with no baseline", map[string]string{"update": "true", "only": "test"}, "no_baseline"},
		{"a loop gate that is not standards", map[string]string{"update": "true", "only": "commits"}, "no_baseline"},
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
