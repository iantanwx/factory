// Package gittest runs the git of a test with auto maintenance and auto gc
// off. A detached auto maintenance outlives the git that started it, and
// it writes into objects/ while t.TempDir's cleanup removes the repository
// (factorio-yaxt, factorio-d14g, factorio-05um). Every test runs git
// through Command or CommandContext, and TestEveryTestGitRunsThroughGittest
// holds them to it.
package gittest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
