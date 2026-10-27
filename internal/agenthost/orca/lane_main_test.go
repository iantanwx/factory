//go:build !orca

package orca_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The contract binary is a lane binary, so this test builds it the way the
// lane does and runs its TestMain path against a stub orca. The constraint
// keeps this test out of the binary it builds.

// stubOrca answers `account list --json` with a claude session window at
// 93 %, fails every other command, and records each command line.
const stubOrca = `#!/bin/sh
echo "$*" >> '%RECORD%'
if [ "$*" = "account list --json" ]; then
  echo '{"id":"x","ok":true,"result":{"rateLimits":{"claude":{"session":{"usedPercent":93,"resetsAt":1792800000000},"updatedAt":1792560000000,"status":"ok"}}}}'
  exit 0
fi
exit 1
`

// C24: the contract binary runs the gate before any orca call.
func TestContractBinaryRunsTheLaneGateBeforeAnyOrcaCall(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the contract binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "orca-contract.test")
	build := exec.Command("go", "test", "-c", "-tags", "orca", "-o", bin, ".")
	build.Env = append(build.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go test -c -tags orca: %v\n%s", err, out)
	}

	cases := []struct {
		name     string
		unset    string
		exit     int
		line     string
		commands []string
	}{
		{name: "a missing value exits 1 and calls no orca", unset: "LOOP_ORCA_ROOT", exit: 1,
			line: "LOOP_ORCA_ROOT is not set"},
		{name: "a spent window exits 3 after one quota read", exit: 3,
			line:     "quota: claude window session at 93% resets 2026-10-24T00:00:00Z",
			commands: []string{"account list --json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			record := filepath.Join(tmp, "orca.log")
			orcaBin := filepath.Join(tmp, "orca")
			if err := os.WriteFile(orcaBin, []byte(strings.ReplaceAll(stubOrca, "%RECORD%", record)), 0o700); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{
				"HOME":            tmp,
				"LOOP_ORCA_BIN":   orcaBin,
				"LOOP_ORCA_ROOT":  filepath.Join(tmp, "worktrees"),
				"LOOP_ORCA_REPO":  "path:" + tmp,
				"LOOP_ORCA_MODEL": "claude-opus-5-5",
			}
			delete(env, tc.unset)
			cmd := exec.Command(bin, "-test.run", "^(TestOrcaContract|TestOrcaAgentRunsShellCommands)$")
			cmd.Dir = tmp
			for _, kv := range os.Environ() {
				name, _, _ := strings.Cut(kv, "=")
				if name != "HOME" && name != "CI" && !strings.HasPrefix(name, "LOOP_ORCA_") {
					cmd.Env = append(cmd.Env, kv)
				}
			}
			for k, v := range env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != tc.exit {
				t.Fatalf("the contract binary ended with %v; want exit %d\n%s", err, tc.exit, out)
			}
			if !slices.Contains(strings.Split(string(out), "\n"), tc.line) {
				t.Fatalf("the output holds no line %q:\n%s", tc.line, out)
			}
			var commands []string
			if b, err := os.ReadFile(record); err == nil {
				commands = strings.Split(strings.TrimSpace(string(b)), "\n")
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if !slices.Equal(commands, tc.commands) {
				t.Fatalf("the stub orca ran %q; want %q", commands, tc.commands)
			}
		})
	}
}
