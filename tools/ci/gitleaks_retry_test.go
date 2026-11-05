package ci

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// github.com release downloads return a transient 5xx at times. Run the
// action's gitleaks step with the real curl against a server that answers
// 504 once and then serves the tarball: the step must still put gitleaks
// on the job's PATH.
func TestActionSurvivesATransientGitleaksDownloadFailure(t *testing.T) {
	var a action
	load(t, "action.yml", &a)
	i := slices.IndexFunc(a.Runs.Steps, func(s step) bool { return strings.Contains(s.Run, "gitleaks") })
	if i < 0 {
		t.Fatal("action.yml has no step that installs gitleaks")
	}
	s := a.Runs.Steps[i]
	gitleaks := a.Inputs["gitleaks-version"].Default

	archive := tarball(t, "gitleaks", "#!/bin/sh\necho gitleaks\n")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	realCurl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("no curl on PATH")
	}
	tmp := t.TempDir()
	stub := filepath.Join(tmp, "stub")
	runner := filepath.Join(tmp, "runner")
	for _, d := range []string{stub, runner} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The wrapper sends each github.com URL to the test server and runs
	// the real curl with every other argument unchanged.
	curl := fmt.Sprintf("#!/bin/bash\nargs=()\nfor a; do args+=(\"${a/https:\\/\\/github.com/%s}\"); done\nexec %q \"${args[@]}\"\n", srv.URL, realCurl)
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
			t.Fatalf("the gitleaks step reads %s=%s, which this test does not set", k, v)
		}
		cmd.Env = append(cmd.Env, k+"="+val)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("one 504 from the gitleaks download failed the step: %v\n%s", err, out)
	}
	added, err := os.ReadFile(ghPath)
	if err != nil {
		t.Fatal(err)
	}
	bin := strings.TrimSpace(string(added))
	out, err := exec.Command(filepath.Join(bin, "gitleaks")).Output()
	if err != nil || string(out) != "gitleaks\n" {
		t.Errorf("gitleaks on PATH printed %q, %v; want \"gitleaks\\n\"", out, err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("the server saw %d requests, want 2: one 504, then the tarball", got)
	}
}
