package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs git in dir and returns its trimmed output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// runRelease runs the run steps of the release job in checkout, as a
// runner would for the push of sha. The checkout and setup steps are the
// test's. make and gh are stubs, and gh writes its arguments to the file
// it returns.
func runRelease(t *testing.T, checkout, sha string) string {
	t.Helper()
	j, ok := ciWorkflow(t).Jobs[ReleaseJob]
	if !ok {
		t.Fatalf("ci.yml has no %s job", ReleaseJob)
	}
	tmp := t.TempDir()
	stub := filepath.Join(tmp, "stub")
	if err := os.Mkdir(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	ghArgs := filepath.Join(tmp, "gh-args")
	stubs := map[string]string{
		"make": "#!/bin/sh\nfor a; do case $a in VERSION=*) v=${a#VERSION=}; mkdir -p dist/$v && touch dist/$v/loop;; esac; done\n",
		"gh":   "#!/bin/sh\nfor a; do echo \"$a\"; done >> " + ghArgs + "\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(stub, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ghEnv := filepath.Join(tmp, "github_env")
	if err := os.WriteFile(ghEnv, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{
		"secrets.LOOP_RELEASE_KEY": "not-a-key",
		"github.token":             "not-a-token",
	}
	for _, s := range j.Steps {
		if s.Run == "" {
			continue
		}
		// The steps share env through GITHUB_ENV, as on a runner.
		env := map[string]string{}
		data, err := os.ReadFile(ghEnv)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				env[k] = v
			}
		}
		switch s.If {
		case "":
		case "env.VERSION != ''":
			if env["VERSION"] == "" {
				continue
			}
		default:
			t.Fatalf("release step %q runs if %q, which this test cannot resolve", s.Name, s.If)
		}
		for k, v := range s.Env {
			expr := strings.TrimSuffix(strings.TrimPrefix(v, "${{ "), " }}")
			val, ok := vars[expr]
			if !ok {
				t.Fatalf("release step %q reads %s=%s, which this test does not set", s.Name, k, v)
			}
			env[k] = val
		}
		cmd := exec.Command("bash", "-e", "-c", s.Run)
		cmd.Dir = checkout
		cmd.Env = append(os.Environ(),
			"PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GITHUB_ENV="+ghEnv, "GITHUB_SHA="+sha, "GITHUB_REF=refs/heads/main",
			"GITHUB_REPOSITORY=slothware-labs/loop", "GITHUB_EVENT_NAME=push",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("release step %q failed: %v\n%s", s.Name, err, out)
		}
	}
	return ghArgs
}

// releaseTarget is the commit the gh release create call names, or ""
// when the job created no release.
func releaseTarget(t *testing.T, ghArgs string) string {
	t.Helper()
	data, err := os.ReadFile(ghArgs)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i, a := range args {
		if a == "--target" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("gh ran with no --target: %q", args)
	return ""
}

// Five merges land in seconds, and the release jobs share one concurrency
// group. The group keeps one pending job, so the job of an older commit
// can cancel the job of main's tip (loop fault F-48). The job that runs
// last must then release the tip of main, not its own older commit.
func TestReleaseJobOfAnOlderCommitReleasesTheTipOfMain(t *testing.T) {
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	origin := filepath.Join(tmp, "origin.git")
	gitIn(t, tmp, "init", "-q", "-b", "main", work)
	gitIn(t, work, "commit", "-q", "--allow-empty", "-m", "c0")
	gitIn(t, work, "tag", "v0.0.1")
	gitIn(t, work, "commit", "-q", "--allow-empty", "-m", "c1")
	older := gitIn(t, work, "rev-parse", "HEAD")
	gitIn(t, work, "commit", "-q", "--allow-empty", "-m", "c2")
	tip := gitIn(t, work, "rev-parse", "HEAD")
	gitIn(t, tmp, "clone", "-q", "--bare", work, origin)

	// actions/checkout with fetch-depth 0 checks out the run's own commit.
	checkout := filepath.Join(tmp, "checkout")
	gitIn(t, tmp, "clone", "-q", origin, checkout)
	gitIn(t, checkout, "checkout", "-q", "--detach", older)

	ghArgs := runRelease(t, checkout, older)
	if got := releaseTarget(t, ghArgs); got != tip {
		t.Fatalf("the release job of %s, which ran last, released %q, want the tip of main %s", older[:7], got, tip[:7])
	}
	if v0 := gitIn(t, origin, "rev-parse", "v0^{commit}"); v0 != tip {
		t.Errorf("v0 points at %s, want the tip of main %s", v0[:7], tip[:7])
	}
}

// When main's tip already holds a release, the job of an older commit
// makes no second release.
func TestReleaseJobOfAnOlderCommitSkipsAReleasedTip(t *testing.T) {
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	origin := filepath.Join(tmp, "origin.git")
	gitIn(t, tmp, "init", "-q", "-b", "main", work)
	gitIn(t, work, "commit", "-q", "--allow-empty", "-m", "c0")
	gitIn(t, work, "tag", "v0.0.1")
	gitIn(t, work, "commit", "-q", "--allow-empty", "-m", "c1")
	older := gitIn(t, work, "rev-parse", "HEAD")
	gitIn(t, work, "commit", "-q", "--allow-empty", "-m", "c2")
	gitIn(t, work, "tag", "v0.0.2")
	gitIn(t, tmp, "clone", "-q", "--bare", work, origin)

	checkout := filepath.Join(tmp, "checkout")
	gitIn(t, tmp, "clone", "-q", origin, checkout)
	gitIn(t, checkout, "checkout", "-q", "--detach", older)

	if got := releaseTarget(t, runRelease(t, checkout, older)); got != "" {
		t.Fatalf("the release job of %s released %s, want no release: the tip of main holds v0.0.2", older[:7], got)
	}
}
