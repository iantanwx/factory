// Package gittest runs the git of a test with auto maintenance and auto gc
// off. A detached auto maintenance outlives the git that started it, and
// it writes into objects/ while t.TempDir's cleanup removes the repository
// (factorio-yaxt, factorio-d14g, factorio-05um). Every test runs git
// through Command or CommandContext, and TestEveryTestGitRunsThroughGittest
// holds them to it. A package whose tests run git also runs them through
// Main, which reaches the git that production code starts in a test.
package gittest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Config holds the entries that turn off auto maintenance and auto gc.
var Config = [][2]string{
	{"maintenance.auto", "false"},
	{"gc.auto", "0"},
}

// Command returns a git command whose environment is Env(os.Environ()).
// A caller that sets its own environment passes it through Env.
func Command(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Env = Env(os.Environ())
	return cmd
}

// CommandContext is Command with a context.
func CommandContext(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = Env(os.Environ())
	return cmd
}

// Env returns env with Config in force, through a global config file that
// holds it. A local push drops -c flags and GIT_CONFIG_COUNT entries before
// receive-pack runs, and only a config file reaches the receiving
// repository. The file replaces the user's global config and the system
// config, as GIT_CONFIG_GLOBAL=/dev/null and GIT_CONFIG_NOSYSTEM=1 do. The
// last entry of a key wins in exec.Cmd.Env, so Env overrides both keys in
// env.
func Env(env []string) []string {
	return append(append([]string(nil), env...), "GIT_CONFIG_GLOBAL="+globalConfig(), "GIT_CONFIG_NOSYSTEM=1")
}

// globalConfig writes the global config file once for each test process.
// It lives in a private temporary directory, so no other user can plant a
// config there.
var globalConfig = sync.OnceValue(func() string {
	dir, err := os.MkdirTemp("", "loop-gittest-")
	if err != nil {
		panic(err)
	}
	body := ""
	for _, kv := range Config {
		section, key, _ := strings.Cut(kv[0], ".")
		body += fmt.Sprintf("[%s]\n\t%s = %s\n", section, key, kv[1])
	}
	path := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		panic(err)
	}
	return path
})

// Main runs the tests of m and returns their exit code. It sets Config in
// the process environment as GIT_CONFIG_COUNT entries, so it reaches each
// git that appends os.Environ(), production git and its children included.
// It also traces every such git, and it fails the run when one of them
// started an auto maintenance or an auto gc. A TestMain calls it as
// os.Exit(gittest.Main(m)).
func Main(m *testing.M) int {
	dir, err := os.MkdirTemp("", "loop-gittrace-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer func() { _ = os.RemoveAll(dir) }()
	env := map[string]string{"GIT_CONFIG_COUNT": strconv.Itoa(len(Config)), "GIT_TRACE2_EVENT": dir}
	for i, kv := range Config {
		env["GIT_CONFIG_KEY_"+strconv.Itoa(i)], env["GIT_CONFIG_VALUE_"+strconv.Itoa(i)] = kv[0], kv[1]
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	code := m.Run()
	starts, err := AutoMaintenance(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if len(starts) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: test git started %d auto maintenance or auto gc:\n%s\n", len(starts), strings.Join(starts, "\n"))
		if code == 0 {
			code = 1
		}
	}
	return code
}

// AutoMaintenance reads the trace2 event files in dir, one for each git
// process, and returns one line for each auto maintenance or auto gc that
// a git started. The line names the git that started it.
func AutoMaintenance(dir string) ([]string, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var starts []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return nil, err
		}
		var parent []string
		for _, line := range strings.Split(string(data), "\n") {
			var ev struct {
				Event string   `json:"event"`
				Argv  []string `json:"argv"`
			}
			if json.Unmarshal([]byte(line), &ev) != nil {
				continue
			}
			switch {
			case ev.Event == "start":
				parent = ev.Argv
			case ev.Event == "child_start" && len(ev.Argv) > 1 &&
				(ev.Argv[1] == "maintenance" || ev.Argv[1] == "gc") && slices.Contains(ev.Argv, "--auto"):
				starts = append(starts, strings.Join(parent, " ")+" started "+strings.Join(ev.Argv, " "))
			}
		}
	}
	return starts, nil
}
