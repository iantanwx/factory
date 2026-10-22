package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/iantanwx/loop/internal/config"
)

// fakeGH answers gh calls from a table keyed by the joined arguments and
// records each call with its stdin.
type fakeGH struct {
	answers map[string]string
	calls   []string
	stdin   map[string][]byte
}

func (f *fakeGH) run(_ context.Context, _ string, stdin []byte, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if stdin != nil {
		f.stdin[key] = stdin
	}
	out, ok := f.answers[key]
	if !ok {
		return nil, errors.New(key + ": HTTP 404")
	}
	return []byte(out), nil
}

func TestInitSteps(t *testing.T) {
	gh := &fakeGH{stdin: map[string][]byte{}, answers: map[string]string{
		"gh api repos/me/app/commits/main/check-runs":                              `{"check_runs":[{"name":"test"},{"name":"loop/eligible"},{"name":"test"}]}`,
		"gh api repos/me/app/commits/main/status":                                  `{"statuses":[{"context":"lint"}]}`,
		"gh api repos/me/app/rulesets":                                             `[{"id":7,"name":"other"}]`,
		"gh api -X POST repos/me/app/rulesets --input - --silent":                  ``,
		"gh api -X PUT repos/me/app/contents/.loop/config.yaml --input - --silent": ``,
	}}
	h := &hostVerbs{cmd: gh.run}
	ctx := context.Background()
	rep := InitReport{Repo: "me/app", Branch: "main", Path: "/src/app"}

	ci, err := h.ciChecks(ctx, rep)
	if err != nil || !slices.Equal(ci, []string{"lint", "test"}) {
		t.Fatalf("ci = %v %v", ci, err)
	}
	rep.CI = ci

	got, err := h.ensureProjectConfig(ctx, rep)
	if err != nil || got != "created" {
		t.Fatalf("config = %q %v", got, err)
	}
	var put struct{ Branch, Content string }
	if err := json.Unmarshal(gh.stdin["gh api -X PUT repos/me/app/contents/.loop/config.yaml --input - --silent"], &put); err != nil {
		t.Fatal(err)
	}
	body, _ := base64.StdEncoding.DecodeString(put.Content)
	cfg, err := config.ParseProjectConfig(body)
	if err != nil || put.Branch != "main" || cfg.Merge.Mechanism != config.MergeDirect || !cfg.StageEnabled(config.StageImplementer) {
		t.Fatalf("config %+v on %q: %v\n%s", cfg, put.Branch, err, body)
	}

	got, err = h.ensureRuleset(ctx, rep, 42)
	if err != nil || got != "created" {
		t.Fatalf("ruleset = %q %v", got, err)
	}
	raw := string(gh.stdin["gh api -X POST repos/me/app/rulesets --input - --silent"])
	for _, want := range []string{`"context":"lint"`, `"context":"test"`, `"context":"loop/eligible","integration_id":42`,
		`"required_approving_review_count":0`, `"~DEFAULT_BRANCH"`, `"actor_type":"RepositoryRole"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("the ruleset lacks %s: %s", want, raw)
		}
	}

	// A second run finds the config and updates the ruleset.
	gh.answers["gh api repos/me/app/contents/.loop/config.yaml?ref=main --silent"] = ``
	gh.answers["gh api repos/me/app/rulesets"] = `[{"id":9,"name":"loop"}]`
	gh.answers["gh api -X PUT repos/me/app/rulesets/9 --input - --silent"] = ``
	if got, err := h.ensureProjectConfig(ctx, rep); err != nil || got != "present" {
		t.Fatalf("config again = %q %v", got, err)
	}
	if got, err := h.ensureRuleset(ctx, rep, 42); err != nil || got != "updated" {
		t.Fatalf("ruleset again = %q %v", got, err)
	}
}

func TestProjectID(t *testing.T) {
	for repo, want := range map[string]string{"me/My.App": "my-app", "me/2048": "p-2048", "me/tool_x": "tool_x"} {
		if got := projectID(repo); got != want {
			t.Errorf("%s: %q, want %q", repo, got, want)
		}
	}
}

func TestCIWorkflowFitsTheStack(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  []string
	}{
		"go":     {map[string]string{"go.mod": "module x\n"}, []string{"go-version-file: go.mod", "run: go test ./..."}},
		"pnpm":   {map[string]string{"package.json": `{"scripts":{"test":"vitest","build":"vite build","dev":"vite"}}`, "pnpm-lock.yaml": ""}, []string{"run: corepack enable", "run: pnpm install --frozen-lockfile", "run: pnpm run test", "run: pnpm run build"}},
		"python": {map[string]string{"pyproject.toml": "", "tests/test_x.py": ""}, []string{"run: pip install -e .", "run: pip install pytest && pytest"}},
		"make":   {map[string]string{"Makefile": "build:\n\tcc x.c\ntest: build\n\t./x\n"}, []string{"run: make test"}},
		"none":   {map[string]string{"README.md": ""}, []string{"loop init found no build or test command"}},
	} {
		root := t.TempDir()
		for f, body := range tc.files {
			p := filepath.Join(root, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		data := ciWorkflow(root, "main")
		var wf struct {
			On   map[string]any `yaml:"on"`
			Jobs map[string]struct {
				Steps []map[string]any `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(data, &wf); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, data)
		}
		if len(wf.Jobs[CIJob].Steps) < 2 || wf.On["pull_request"] == nil && !strings.Contains(string(data), "pull_request:") {
			t.Fatalf("%s: workflow %+v\n%s", name, wf, data)
		}
		for _, w := range tc.want {
			if !strings.Contains(string(data), w) {
				t.Errorf("%s: lacks %q:\n%s", name, w, data)
			}
		}
		if name == "pnpm" && strings.Contains(string(data), "run dev") {
			t.Errorf("pnpm: runs the dev script:\n%s", data)
		}
	}
}

func TestInitCommitsCIWhenNoneRan(t *testing.T) {
	gh := &fakeGH{stdin: map[string][]byte{}, answers: map[string]string{
		"gh api -X PUT repos/me/app/contents/.github/workflows/loop-ci.yml --input - --silent": ``,
	}}
	h := &hostVerbs{cmd: gh.run}
	rep := InitReport{Repo: "me/app", Branch: "trunk", Path: t.TempDir()}
	got, err := h.ensureFile(context.Background(), rep, CIWorkflowPath, "ci", func() ([]byte, error) { return ciWorkflow(rep.Path, rep.Branch), nil })
	if err != nil || got != "created" {
		t.Fatalf("ensureFile = %q %v", got, err)
	}
	var put struct{ Branch, Content string }
	if err := json.Unmarshal(gh.stdin["gh api -X PUT repos/me/app/contents/.github/workflows/loop-ci.yml --input - --silent"], &put); err != nil {
		t.Fatal(err)
	}
	body, _ := base64.StdEncoding.DecodeString(put.Content)
	if put.Branch != "trunk" || !strings.Contains(string(body), "branches: [trunk]") {
		t.Fatalf("workflow on %q:\n%s", put.Branch, body)
	}
}
