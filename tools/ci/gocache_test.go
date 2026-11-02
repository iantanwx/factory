package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// setup-go restores the module cache only when its cache pattern holds no
// "." or ".." segment. A job that uses the local action, `uses: ./`, gets
// github.action_path as the workspace followed by "/./", as run
// 41492558611 showed. Expand each setup-go step of ci.yml, through
// action.yml for `uses: ./`, and check the pattern setup-go receives.
func TestEverySetupGoStepRestoresTheModuleCache(t *testing.T) {
	const workspace = "/home/runner/work/factorio/factorio"
	var a action
	load(t, "action.yml", &a)
	w := ciWorkflow(t)
	for name, j := range w.Jobs {
		for _, s := range j.Steps {
			steps, actionPath := []step{s}, workspace
			if s.Uses == "./" {
				steps, actionPath = a.Runs.Steps, workspace+"/./"
			}
			for _, g := range steps {
				if !strings.HasPrefix(g.Uses, "actions/setup-go@") {
					continue
				}
				if c, ok := g.With["cache"]; ok && c != true {
					t.Errorf("job %s: setup-go sets cache: %v, want the module cache on", name, c)
				}
				dep, _ := g.With["cache-dependency-path"].(string)
				if dep == "" {
					dep = "go.sum"
				}
				pattern := strings.ReplaceAll(dep, "${{ github.action_path }}", actionPath)
				if !strings.HasPrefix(pattern, "/") {
					pattern = workspace + "/" + pattern
				}
				segments := strings.Split(pattern, "/")
				if slices.Contains(segments, ".") || slices.Contains(segments, "..") {
					t.Errorf("job %s: setup-go gets cache pattern %q, which setup-go refuses: Relative pathing '.' and '..' is not allowed", name, pattern)
				}
			}
		}
	}
}

// Run the action's id steps as a runner would, then expand the cache
// pattern that setup-go receives. A job with `uses: ./` and a project at
// @v0 must both get the action's own go.sum, with no "." segment.
func TestActionGivesSetupGoItsOwnGoSum(t *testing.T) {
	var a action
	load(t, "action.yml", &a)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	expr := regexp.MustCompile(`\$\{\{ (.+?) \}\}`)
	for _, actionPath := range []string{root + "/./", root} {
		vars := map[string]string{"github.action_path": actionPath}
		expand := func(s string) string {
			return expr.ReplaceAllStringFunc(s, func(m string) string {
				v, ok := vars[expr.FindStringSubmatch(m)[1]]
				if !ok {
					t.Fatalf("action.yml reads %s, which this test does not set", m)
				}
				return v
			})
		}
		var pattern string
		for _, s := range a.Runs.Steps {
			if strings.HasPrefix(s.Uses, "actions/setup-go@") {
				dep, _ := s.With["cache-dependency-path"].(string)
				pattern = expand(dep)
				break
			}
			if s.ID == "" || s.Run == "" {
				continue
			}
			out := filepath.Join(t.TempDir(), "output")
			cmd := exec.Command("bash", "-e", "-c", s.Run)
			cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+out)
			for k, v := range s.Env {
				cmd.Env = append(cmd.Env, k+"="+expand(v))
			}
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("step %s failed: %v\n%s", s.ID, err, b)
			}
			b, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				k, v, _ := strings.Cut(line, "=")
				vars["steps."+s.ID+".outputs."+k] = v
			}
		}
		if want := root + "/go.sum"; pattern != want {
			t.Errorf("action path %q: setup-go gets cache pattern %q, want %q", actionPath, pattern, want)
		}
	}
}
