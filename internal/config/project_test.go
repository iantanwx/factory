package config

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validProjectConfig = `
setup: pnpm install
tracker:
  adapter: beads
merge:
  mechanism: mergify
stages:
  implementer:
    model: claude-opus-5-5
    harness: claude
    run_budget: 90m
    schedule: 5m
    effort: max
    escalated:
      model: claude-opus-5-5
      effort: xhigh
  code-reviewer:
    enabled: false
gates:
  - name: gates
    run: make gates
policy_hooks:
  - name: trust-paths
    run: ./scripts/policy.sh
signoff:
  readers: [octocat, mona-lisa]
`

func TestParseProjectConfig(t *testing.T) {
	cfg, err := ParseProjectConfig([]byte(validProjectConfig))
	if err != nil {
		t.Fatalf("ParseProjectConfig: %v", err)
	}
	if cfg.ManifestPath() != DefaultManifestPath {
		t.Fatalf("ManifestPath = %q", cfg.ManifestPath())
	}
	if cfg.Merge.Mechanism != MergeMergify {
		t.Fatalf("mechanism = %q", cfg.Merge.Mechanism)
	}
	if !cfg.StageEnabled("implementer") || cfg.StageEnabled("code-reviewer") || cfg.StageEnabled("brainstorm") {
		t.Fatalf("stage enablement is wrong: %+v", cfg.Stages)
	}
	if got := cfg.Signoff.Readers; len(got) != 2 || got[0] != "octocat" || got[1] != "mona-lisa" {
		t.Fatalf("sign-off readers = %v", got)
	}
	if cfg.Stages["implementer"].Effort != "max" {
		t.Fatalf("effort = %q", cfg.Stages["implementer"].Effort)
	}
	if e := cfg.Stages["implementer"].Escalated; e == nil || *e != (EscalatedProfile{Model: "claude-opus-5-5", Effort: "xhigh"}) {
		t.Fatalf("escalated profile = %+v", e)
	}
	if cfg.Stages["implementer"].RunBudget != 90*time.Minute {
		t.Fatalf("run budget = %v", cfg.Stages["implementer"].RunBudget)
	}
	if err := cfg.CheckStages(Known{RunStages: []string{"implementer", "code-reviewer"}}); err != nil {
		t.Fatalf("CheckStages: %v", err)
	}
	if err := cfg.CheckStages(Known{RunStages: []string{"implementer"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CheckStages with an unknown stage: %v", err)
	}
}

func TestParseProjectConfigRefuses(t *testing.T) {
	cases := map[string]string{
		"unknown field":               validProjectConfig + "organisation: acme\n",
		"unknown stage field":         strings.Replace(validProjectConfig, "harness: claude", "harness: claude\n    modle: sonnet", 1),
		"unknown mechanism":           strings.Replace(validProjectConfig, "mechanism: mergify", "mechanism: bors", 1),
		"no mechanism":                strings.Replace(validProjectConfig, "mechanism: mergify", "", 1),
		"escaping manifest":           validProjectConfig + "manifest: ../secrets.yaml\n",
		"absolute manifest":           validProjectConfig + "manifest: /etc/secrets.yaml\n",
		"empty gate command":          strings.Replace(validProjectConfig, "run: make gates", "run: \"\"", 1),
		"negative budget":             strings.Replace(validProjectConfig, "run_budget: 90m", "run_budget: -5m", 1),
		"effort not a word":           strings.Replace(validProjectConfig, "effort: max", "effort: \"high --dangerously-skip-permissions\"", 1),
		"escalated effort not a word": strings.Replace(validProjectConfig, "effort: xhigh", "effort: \"xhigh --yolo\"", 1),
		"escalated harness not ident": strings.Replace(validProjectConfig, "effort: xhigh", "effort: xhigh\n      harness: Claude!", 1),
		"unknown escalated field":     strings.Replace(validProjectConfig, "effort: xhigh", "effort: xhigh\n      modle: opus", 1),
		"reader not a login":          strings.Replace(validProjectConfig, "mona-lisa", "slothware-labs/loop", 1),
		"reader twice":                strings.Replace(validProjectConfig, "mona-lisa", "OctoCat", 1),
		"empty check":                 strings.Replace(validProjectConfig, "mechanism: mergify", "mechanism: mergify\n  required_checks: [\"\"]", 1),
		"check twice":                 strings.Replace(validProjectConfig, "mechanism: mergify", "mechanism: mergify\n  required_checks: [gates, gates]", 1),
		"check control char":          strings.Replace(validProjectConfig, "mechanism: mergify", "mechanism: mergify\n  required_checks: [\"ga\\ttes\"]", 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseProjectConfig([]byte(doc)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	for _, m := range []MergeMechanism{MergeMergify, MergeGitHubQueue, MergeDirect} {
		doc := strings.Replace(validProjectConfig, "mechanism: mergify", "mechanism: "+string(m), 1)
		if _, err := ParseProjectConfig([]byte(doc)); err != nil {
			t.Fatalf("mechanism %s refused: %v", m, err)
		}
	}
}

// C7: a Claude stage, or a stage that names no harness, takes a full
// Claude model id. A Claude Code alias resolves to a model the loop cannot
// compare with the receipt, so config refuses it. A Codex model is not
// checked.
func TestC7StageConfigRefusesAClaudeCodeAlias(t *testing.T) {
	cases := []struct {
		harness, model string
		ok             bool
	}{
		{"claude", "opus", false},
		{"claude", "sonnet", false},
		{"claude", "haiku", false},
		{"claude", "default", false},
		{"claude", "opusplan", false},
		{"claude", "opus[1m]", false},
		{"", "opus", false},
		{"", "sonnet", false},
		{"", "haiku", false},
		{"", "default", false},
		{"", "opusplan", false},
		{"", "opus[1m]", false},
		{"claude", "claude-opus-5-5", true},
		{"claude", "claude-haiku-4-5-20251001", true},
		{"", "claude-opus-5-5", true},
		{"", "claude-haiku-4-5-20251001", true},
		{"codex", "gpt-5-codex", true},
	}
	for _, c := range cases {
		t.Run(c.harness+"/"+c.model, func(t *testing.T) {
			doc := "merge:\n  mechanism: mergify\nstages:\n  implementer:\n    model: \"" + c.model + "\"\n"
			if c.harness != "" {
				doc += "    harness: " + c.harness + "\n"
			}
			_, err := ParseProjectConfig([]byte(doc))
			if c.ok {
				if err != nil {
					t.Fatalf("ParseProjectConfig: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "stages.implementer.model") {
				t.Fatalf("err = %v, want ErrInvalid naming stages.implementer.model", err)
			}
		})
	}
}

// The health block is optional. Its audit_workflow names a workflow file
// under .github/workflows, and it has no default.
func TestProjectConfigHealthAuditWorkflow(t *testing.T) {
	cfg, err := ParseProjectConfig([]byte(validProjectConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Health.AuditWorkflow != "" {
		t.Fatalf("audit_workflow with no health block = %q, want none", cfg.Health.AuditWorkflow)
	}
	for _, file := range []string{"audit.yml", "nightly-audit.yaml", "deps_audit.v2.yml"} {
		cfg, err := ParseProjectConfig([]byte(validProjectConfig + "health:\n  audit_workflow: " + file + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if cfg.Health.AuditWorkflow != file {
			t.Fatalf("audit_workflow = %q, want %q", cfg.Health.AuditWorkflow, file)
		}
	}
	for _, bad := range []string{"audit", ".github/workflows/audit.yml", "../audit.yml", "audit .yml", "\"audit\\u0007.yml\""} {
		_, err := ParseProjectConfig([]byte(validProjectConfig + "health:\n  audit_workflow: " + bad + "\n"))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "health.audit_workflow") {
			t.Fatalf("audit_workflow %s: err = %v, want a refusal of health.audit_workflow", bad, err)
		}
	}
	if _, err := ParseProjectConfig([]byte(validProjectConfig + "health:\n  audit_wrokflow: audit.yml\n")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown health field: err = %v, want ErrInvalid", err)
	}
}

// health.ci_budget is an optional duration with no default. A negative
// budget is refused, since no run fits it.
func TestProjectConfigHealthCIBudget(t *testing.T) {
	cfg, err := ParseProjectConfig([]byte(validProjectConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Health.CIBudget != 0 {
		t.Fatalf("ci_budget with no health block = %v, want none", cfg.Health.CIBudget)
	}
	cfg, err = ParseProjectConfig([]byte(validProjectConfig + "health:\n  ci_budget: 5m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Health.CIBudget != 5*time.Minute {
		t.Fatalf("ci_budget = %v, want 5m", cfg.Health.CIBudget)
	}
	if _, err := ParseProjectConfig([]byte(validProjectConfig + "health:\n  ci_budget: -1m\n")); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "health.ci_budget") {
		t.Fatalf("ci_budget -1m: err = %v, want a refusal of health.ci_budget", err)
	}
	if _, err := ParseProjectConfig([]byte(validProjectConfig + "health:\n  ci_budget: five minutes\n")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a ci_budget that is no duration: err = %v, want ErrInvalid", err)
	}
}

// health.operator_reminder is an optional duration with no default. A
// negative limit is refused, since every entry passes it (C-bug-1).
func TestCBug1ProjectConfigHealthOperatorReminder(t *testing.T) {
	cfg, err := ParseProjectConfig([]byte(validProjectConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Health.OperatorReminder != 0 {
		t.Fatalf("operator_reminder with no health block = %v, want none", cfg.Health.OperatorReminder)
	}
	cfg, err = ParseProjectConfig([]byte(validProjectConfig + "health:\n  operator_reminder: 12h\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Health.OperatorReminder != 12*time.Hour {
		t.Fatalf("operator_reminder = %v, want 12h", cfg.Health.OperatorReminder)
	}
	if _, err := ParseProjectConfig([]byte(validProjectConfig + "health:\n  operator_reminder: -1h\n")); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "health.operator_reminder") {
		t.Fatalf("operator_reminder -1h: err = %v, want a refusal of health.operator_reminder", err)
	}
}

// merge.required_checks names the CI checks a ticket's head must pass
// before it is eligible and before any mechanism merges it. A config
// that names none leaves the list empty, and the GitHub service then
// requires the base's ruleset checks, or gates.
func TestParseProjectConfigRequiredChecks(t *testing.T) {
	doc := strings.Replace(validProjectConfig, "mechanism: mergify", "mechanism: direct\n  required_checks: [gates, acceptance]", 1)
	cfg, err := ParseProjectConfig([]byte(doc))
	if err != nil {
		t.Fatalf("ParseProjectConfig: %v", err)
	}
	if got := cfg.Merge.RequiredChecks; len(got) != 2 || got[0] != "gates" || got[1] != "acceptance" {
		t.Fatalf("required_checks = %v; want [gates acceptance]", got)
	}
	cfg, err = ParseProjectConfig([]byte(validProjectConfig))
	if err != nil {
		t.Fatalf("ParseProjectConfig: %v", err)
	}
	if got := cfg.Merge.RequiredChecks; len(got) != 0 {
		t.Fatalf("required_checks = %v; want none named", got)
	}
}

// gitRepo is a clone with an origin. Its main branch and a feature branch
// each hold their own .loop/config.yaml on origin.
type gitRepo struct {
	t     *testing.T
	clone string
}

func (g gitRepo) git(args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = g.clone
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (g gitRepo) commit(files map[string]string, msg string) {
	g.t.Helper()
	for name, body := range files {
		p := filepath.Join(g.clone, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			g.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			g.t.Fatal(err)
		}
	}
	g.git("add", "-A")
	g.git("commit", "-q", "-m", msg)
}

func newGitRepo(t *testing.T) gitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	g := gitRepo{t: t, clone: root}
	g.git("init", "-q", "--bare", bare)
	g.clone = filepath.Join(root, "clone")
	if err := os.Mkdir(g.clone, 0o755); err != nil {
		t.Fatal(err)
	}
	g.git("init", "-q")
	g.git("remote", "add", "origin", bare)
	return g
}

// reader reads a file at a ref with git show. U10 supplies the production
// reader over the bare mirror.
func (g gitRepo) reader() RefReader {
	return func(ctx context.Context, ref, path string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", "show", ref+":"+path)
		cmd.Dir = g.clone
		out, err := cmd.Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && (strings.Contains(string(ee.Stderr), "does not exist") || strings.Contains(string(ee.Stderr), "exists on disk")) {
				return nil, fs.ErrNotExist
			}
			return nil, err
		}
		return out, nil
	}
}

func TestBranchOnlyConfigEditHasNoEffect(t *testing.T) {
	ctx := context.Background()
	g := newGitRepo(t)
	g.commit(map[string]string{ProjectConfigPath: validProjectConfig}, "config")
	g.git("push", "-q", "origin", "main")
	g.git("checkout", "-q", "-b", "agent/edit")
	edited := strings.Replace(validProjectConfig, "mechanism: mergify", "mechanism: direct", 1)
	g.commit(map[string]string{ProjectConfigPath: edited}, "agent edits config")
	g.git("push", "-q", "origin", "agent/edit")
	g.git("fetch", "-q", "origin")

	snap, err := ReadProjectSnapshot(ctx, g.reader(), "main")
	if err != nil {
		t.Fatalf("ReadProjectSnapshot: %v", err)
	}
	if snap.Config.Merge.Mechanism != MergeMergify {
		t.Fatalf("mechanism = %q: the branch edit took effect", snap.Config.Merge.Mechanism)
	}
	if len(snap.Manifest.Secrets) != 0 {
		t.Fatalf("an absent default manifest should read as empty: %+v", snap.Manifest)
	}
	for _, bad := range []string{"", "-x", "a..b", "main:evil", "a b"} {
		if _, err := ReadProjectSnapshot(ctx, g.reader(), bad); err == nil {
			t.Fatalf("default branch %q accepted", bad)
		}
	}
}

func TestReadProjectSnapshotReadsManifest(t *testing.T) {
	ctx := context.Background()
	files := map[string]string{
		ProjectConfigPath: validProjectConfig + "manifest: .loop/env.yaml\n",
		".loop/env.yaml":  "secrets:\n  - name: STRIPE_KEY\n    source: op://Loop/Stripe/key\n",
	}
	read := func(_ context.Context, ref, path string) ([]byte, error) {
		if ref != "origin/main" {
			t.Fatalf("read ref %q, want origin/main", ref)
		}
		if b, ok := files[path]; ok {
			return []byte(b), nil
		}
		return nil, fs.ErrNotExist
	}
	snap, err := ReadProjectSnapshot(ctx, read, "main")
	if err != nil {
		t.Fatalf("ReadProjectSnapshot: %v", err)
	}
	if len(snap.Manifest.Secrets) != 1 || snap.Manifest.Secrets[0].Name != "STRIPE_KEY" {
		t.Fatalf("manifest = %+v", snap.Manifest)
	}
	delete(files, ".loop/env.yaml")
	if _, err := ReadProjectSnapshot(ctx, read, "main"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a configured manifest that is missing: %v, want ErrInvalid", err)
	}
}

// C1: a baseline belongs to one ratcheted gate. Config load refuses it on a
// gate that prints no findings, on a policy hook, on a path outside the
// repository, on a path two gates share and on the standards gate's own
// file, and names the field each time.
func TestProjectConfigBaseline(t *testing.T) {
	gates := func(lines ...string) string {
		return strings.Replace(validProjectConfig, "gates:\n  - name: gates\n    run: make gates\n", "gates:\n"+strings.Join(lines, ""), 1)
	}
	gate := func(name, baseline string) string {
		return "  - name: " + name + "\n    run: make " + name + "\n    baseline: " + baseline + "\n"
	}
	refused := map[string]struct{ doc, field string }{
		"on format":             {gates(gate("format", "lint.json")), "gates[0].baseline"},
		"on typecheck":          {gates(gate("typecheck", "lint.json")), "gates[0].baseline"},
		"on test":               {gates(gate("test", "lint.json")), "gates[0].baseline"},
		"on build":              {gates(gate("build", "lint.json")), "gates[0].baseline"},
		"on a policy hook":      {strings.Replace(validProjectConfig, "run: ./scripts/policy.sh", "run: ./scripts/policy.sh\n    baseline: policy.json", 1), "policy_hooks[0].baseline"},
		"absolute":              {gates(gate("lint", "/etc/lint.json")), "gates[0].baseline"},
		"escaping":              {gates(gate("lint", "../lint.json")), "gates[0].baseline"},
		"dot-dot inside":        {gates(gate("lint", ".loop/../lint.json")), "gates[0].baseline"},
		"not clean":             {gates(gate("lint", ".loop//lint.json")), "gates[0].baseline"},
		"dot":                   {gates(gate("lint", ".")), "gates[0].baseline"},
		"shared by two gates":   {gates(gate("lint", ".loop/lint.json"), gate("audit", ".loop/lint.json")), "gates[1].baseline"},
		"the suppressions file": {gates(gate("lint", ".loop/suppressions.json")), "gates[0].baseline"},
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := ParseProjectConfig([]byte(c.doc))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.field+":") {
				t.Fatalf("err = %v, want a refusal of %s", err, c.field)
			}
		})
	}
	cfg, err := ParseProjectConfig([]byte(gates(gate("lint", ".loop/lint.json"), gate("audit", ".loop/audit.json"), gate("e2e", "e2e/baseline.json"))))
	if err != nil {
		t.Fatalf("baselines on lint, audit and an extra gate: %v", err)
	}
	var got []string
	for _, g := range cfg.Gates {
		got = append(got, g.Name+"="+g.Baseline)
	}
	if want := "lint=.loop/lint.json audit=.loop/audit.json e2e=e2e/baseline.json"; strings.Join(got, " ") != want {
		t.Fatalf("baselines = %v, want %s", got, want)
	}
}
