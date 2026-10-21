// Package gitx runs local git with no user or system config. The github
// package keeps its own hardened runner for the mirror.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Error is a failed git command. Its text names the arguments, the cause
// and git's trimmed stderr.
type Error struct {
	Args   []string
	Err    error
	Stderr string
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, strings.TrimSpace(e.Stderr))
}

func (e *Error) Unwrap() error { return e.Err }

// Run runs `git -C dir args...` with no user or system config, so no hook
// or alias the operator set changes the answer. It returns stdout. A
// failure is an *Error.
func Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, &Error{Args: args, Err: err, Stderr: stderr.String()}
	}
	return out, nil
}

// TreeFiles parses the output of `git ls-tree -r -z`. It maps each path
// that starts with one of prefixes to its object id.
func TreeFiles(out []byte, prefixes []string) map[string]string {
	files := map[string]string{}
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta) // mode, type, object id
		if len(f) != 3 {
			continue
		}
		for _, pre := range prefixes {
			if strings.HasPrefix(path, pre) {
				files[path] = f[2]
				break
			}
		}
	}
	return files
}
