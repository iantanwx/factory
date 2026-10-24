package ci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/iantanwx/loop/internal/cli"
)

type workflow struct {
	Jobs map[string]job `yaml:"jobs"`
}

type job struct {
	Needs   any               `yaml:"needs"`
	If      string            `yaml:"if"`
	Outputs map[string]string `yaml:"outputs"`
	Steps   []step            `yaml:"steps"`
}

type step struct {
	Name            string            `yaml:"name"`
	ID              string            `yaml:"id"`
	If              string            `yaml:"if"`
	Uses            string            `yaml:"uses"`
	Run             string            `yaml:"run"`
	With            map[string]any    `yaml:"with"`
	Env             map[string]string `yaml:"env"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

// needs is the job's needs as a list, whether the YAML holds one name or
// a list.
func (j job) needs() []string {
	switch v := j.Needs.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, n := range v {
			out = append(out, fmt.Sprint(n))
		}
		return out
	}
	return nil
}

func load(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", path))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func ciWorkflow(t *testing.T) workflow {
	t.Helper()
	var w workflow
	load(t, ".github/workflows/ci.yml", &w)
	return w
}

// verifyStep is the one step of the verify job.
func verifyStep(t *testing.T, w workflow) step {
	t.Helper()
	v, ok := w.Jobs[VerifyJob]
	if !ok {
		t.Fatalf("ci.yml has no %s job", VerifyJob)
	}
	if len(v.Steps) != 1 || v.Steps[0].Run == "" {
		t.Fatalf("the %s job holds %d steps, want one run step", VerifyJob, len(v.Steps))
	}
	return v.Steps[0]
}

// The verify job is the one check that stands for the run. A skipped job
// is a green check to the loop (internal/github/reads.go), so verify
// must join every other job, and run when one of them failed.
func TestWorkflowVerifyJoinsEveryJob(t *testing.T) {
	w := ciWorkflow(t)
	v, ok := w.Jobs[VerifyJob]
	if !ok {
		t.Fatalf("ci.yml has no %s job", VerifyJob)
	}
	if v.If != "always()" {
		t.Errorf("%s runs if %q, want always(), so a failed job still reaches it", VerifyJob, v.If)
	}
	var want []string
	for name := range w.Jobs {
		if name != VerifyJob && name != ReleaseJob {
			want = append(want, name)
		}
	}
	slices.Sort(want)
	got := slices.Sorted(slices.Values(v.needs()))
	if !slices.Equal(got, want) {
		t.Errorf("%s needs %q, want every job but itself and %s: %q", VerifyJob, got, ReleaseJob, want)
	}
	env := verifyStep(t, w).Env
	for _, name := range want {
		ref := "${{ needs." + name + ".result }}"
		if !slices.Contains(slices.Collect(maps.Values(env)), ref) {
			t.Errorf("the %s step reads no %s", VerifyJob, ref)
		}
	}
	if got := w.Jobs[ReleaseJob].needs(); !slices.Equal(got, []string{VerifyJob}) {
		t.Errorf("%s needs %q, want [%s]", ReleaseJob, got, VerifyJob)
	}
}

// Run the verify script as Actions runs it, with each result it can read.
func TestWorkflowVerifyPassesOnlyEveryJobGreenAndOneDecision(t *testing.T) {
	s := verifyStep(t, ciWorkflow(t))
	for _, c := range []struct {
		name                 string
		gates, acceptance    string
		gatesDocs, accptDocs string
		pass                 bool
	}{
		{"full run", "success", "success", "false", "false", true},
		{"documents run", "success", "success", "true", "true", true},
		{"no decision runs in full", "success", "success", "", "", true},
		{"no decision and false agree", "success", "success", "", "false", true},
		{"gates failed", "failure", "success", "false", "false", false},
		{"acceptance failed", "success", "failure", "false", "false", false},
		{"gates skipped", "skipped", "success", "false", "false", false},
		{"acceptance skipped", "success", "skipped", "true", "true", false},
		{"acceptance cancelled", "success", "cancelled", "false", "false", false},
		{"the jobs disagree", "success", "success", "true", "false", false},
		{"the jobs disagree the other way", "success", "success", "", "true", false},
	} {
		env := map[string]string{}
		for k, v := range s.Env {
			switch v {
			case "${{ needs.gates.result }}":
				env[k] = c.gates
			case "${{ needs.acceptance.result }}":
				env[k] = c.acceptance
			case "${{ needs.gates.outputs.docs }}":
				env[k] = c.gatesDocs
			case "${{ needs.acceptance.outputs.docs }}":
				env[k] = c.accptDocs
			default:
				t.Fatalf("the %s step reads %s=%s, which this test does not set", VerifyJob, k, v)
			}
		}
		cmd := exec.Command("bash", "-e", "-c", s.Run)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, err := cmd.CombinedOutput()
		if pass := err == nil; pass != c.pass {
			t.Errorf("%s: verify passed=%v, want %v\n%s", c.name, pass, c.pass, out)
		}
	}
}

// Each job that runs a long step decides by itself whether the change is
// documents alone, so neither job waits for the other. Any doubt, a
// failed decision included, runs every step.
func TestWorkflowDocsOnlyPathSkipsOnlyTheCodeSteps(t *testing.T) {
	w := ciWorkflow(t)
	for _, name := range []string{"gates", "acceptance"} {
		j := w.Jobs[name]
		if got := j.Outputs["docs"]; got != "${{ steps.scope.outputs.docs }}" {
			t.Errorf("%s: output docs = %q, want the scope step's", name, got)
		}
		i := slices.IndexFunc(j.Steps, func(s step) bool { return s.ID == "scope" })
		if i < 0 {
			t.Errorf("%s: no step has the id scope", name)
			continue
		}
		scope := j.Steps[i]
		if scope.Run != "go run ./tools/ci/docsonly" || !scope.ContinueOnError {
			t.Errorf("%s: scope runs %q with continue-on-error %v, want go run ./tools/ci/docsonly that may fail", name, scope.Run, scope.ContinueOnError)
		}
		for k, v := range map[string]string{
			"PR_BASE": "${{ github.event.pull_request.base.sha }}",
			"BEFORE":  "${{ github.event.before }}",
			"FORCED":  "${{ github.event.forced }}",
		} {
			if scope.Env[k] != v {
				t.Errorf("%s: scope env %s = %q, want %q", name, k, scope.Env[k], v)
			}
		}
		checkout := j.Steps[0]
		if !strings.HasPrefix(checkout.Uses, "actions/checkout@") || fmt.Sprint(checkout.With["fetch-depth"]) != "0" {
			t.Errorf("%s: the first step is %q with fetch-depth %v, want a full checkout, so the base is there to compare", name, checkout.Uses, checkout.With["fetch-depth"])
		}
		for _, s := range j.Steps[i+1:] {
			if s.If != "steps.scope.outputs.docs != 'true'" && s.If != "steps.scope.outputs.docs == 'true'" {
				t.Errorf("%s: step %q after scope runs if %q, want a condition on the decision", name, s.Name+s.Run+s.Uses, s.If)
			}
		}
	}

	g := w.Jobs["gates"]
	runs := map[string]string{}
	for _, s := range g.Steps {
		if strings.HasPrefix(s.Run, "loop gates") {
			runs[s.Run] = s.If
		}
	}
	want := map[string]string{
		"loop gates":        "steps.scope.outputs.docs != 'true'",
		"loop gates --docs": "steps.scope.outputs.docs == 'true'",
	}
	if !maps.Equal(runs, want) {
		t.Errorf("gates runs %v, want %v", runs, want)
	}
	for _, s := range w.Jobs["acceptance"].Steps {
		if strings.HasPrefix(s.Run, "make ") && s.If != "steps.scope.outputs.docs != 'true'" {
			t.Errorf("acceptance: %q runs if %q, want it to skip a documents-only change", s.Run, s.If)
		}
	}
}

type action struct {
	Inputs map[string]struct {
		Default string `yaml:"default"`
	} `yaml:"inputs"`
	Runs struct {
		Using string `yaml:"using"`
		Steps []step `yaml:"steps"`
	} `yaml:"runs"`
}

// Projects run `loop gates` through action.yml. Run its build step as a
// runner would: it must build loop with the action's ref as its version,
// and put loop and gitleaks on the job's PATH.
func TestActionPutsAVersionedLoopAndGitleaksOnPath(t *testing.T) {
	var a action
	load(t, "action.yml", &a)
	if a.Runs.Using != "composite" {
		t.Fatalf("action.yml runs %q, want composite", a.Runs.Using)
	}
	setup := slices.IndexFunc(a.Runs.Steps, func(s step) bool { return strings.HasPrefix(s.Uses, "actions/setup-go@") })
	if setup < 0 || a.Runs.Steps[setup].With["go-version-file"] != "${{ github.action_path }}/go.mod" {
		t.Fatalf("action.yml sets up no Go from its own go.mod")
	}
	build := slices.IndexFunc(a.Runs.Steps, func(s step) bool { return strings.Contains(s.Run, "go build") })
	if build < setup {
		t.Fatalf("action.yml builds loop before it sets up Go")
	}
	s := a.Runs.Steps[build]
	gitleaks := a.Inputs["gitleaks-version"].Default
	if gitleaks == "" {
		t.Fatal("action.yml has no gitleaks-version default")
	}

	// The -X flag names a variable by its import path, and a wrong path
	// builds with no error. This reference fails to build when it moves.
	_ = &cli.Version

	tmp := t.TempDir()
	stub := filepath.Join(tmp, "stub")
	runner := filepath.Join(tmp, "runner")
	for _, d := range []string{stub, runner} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(tmp, "gitleaks.tar.gz")
	if err := os.WriteFile(archive, tarball(t, "gitleaks", "#!/bin/sh\necho gitleaks\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	urls := filepath.Join(tmp, "urls")
	curl := fmt.Sprintf("#!/bin/sh\nfor a; do echo \"$a\"; done >> %q\ncat %q\n", urls, archive)
	if err := os.WriteFile(filepath.Join(stub, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	ghPath := filepath.Join(tmp, "github_path")
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{
		"github.action_path":      root,
		"github.action_ref":       "v9.8.7",
		"inputs.gitleaks-version": gitleaks,
	}
	cmd := exec.Command("bash", "-e", "-c", s.Run)
	cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+runner, "GITHUB_PATH="+ghPath)
	for k, v := range s.Env {
		expr := strings.TrimSuffix(strings.TrimPrefix(v, "${{ "), " }}")
		val, ok := vars[expr]
		if !ok {
			t.Fatalf("the build step reads %s=%s, which this test does not set", k, v)
		}
		cmd.Env = append(cmd.Env, k+"="+val)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the build step failed: %v\n%s", err, out)
	}

	added, err := os.ReadFile(ghPath)
	if err != nil {
		t.Fatal(err)
	}
	bin := strings.TrimSpace(string(added))
	for _, tool := range []string{"loop", "gitleaks"} {
		if _, err := os.Stat(filepath.Join(bin, tool)); err != nil {
			t.Errorf("GITHUB_PATH names %s, which holds no %s: %v", bin, tool, err)
		}
	}
	out, err := exec.Command(filepath.Join(bin, "loop"), "version").Output()
	if err != nil {
		t.Fatalf("loop version: %v", err)
	}
	var env struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &env); err != nil || env.Data.Version != "v9.8.7" {
		t.Errorf("loop version = %s, want the action's ref v9.8.7", out)
	}
	got, err := os.ReadFile(urls)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/gitleaks/gitleaks/releases/download/v" + gitleaks + "/gitleaks_" + gitleaks + "_linux_x64.tar.gz"
	if !strings.Contains(string(got), want+"\n") {
		t.Errorf("curl fetched %q, want %s", got, want)
	}
}

func tarball(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
