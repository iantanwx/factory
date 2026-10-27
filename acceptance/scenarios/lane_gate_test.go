//go:build acceptance && !orca

package scenarios

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
)

// The scenario binary is a lane binary, so this test builds it the way the
// lane does and runs its TestMain path against a stub orca. The constraint
// keeps this test out of the binary it builds.

// laneStubOrca answers `account list --json` with a claude session window
// at %PERCENT% %, fails every other command, and records each command line.
const laneStubOrca = `#!/bin/sh
echo "$*" >> '%RECORD%'
if [ "$*" = "account list --json" ]; then
  echo '{"id":"x","ok":true,"result":{"rateLimits":{"claude":{"session":{"usedPercent":%PERCENT%,"resetsAt":1792800000000},"updatedAt":1792560000000,"status":"ok"}}}}'
  exit 0
fi
exit 1
`

// C25: the scenario binary runs the gate first, and under the lane it
// never passes on a missing tool. C12: it runs only a loop of its own
// version.
func TestC25TheScenarioBinaryRunsTheLaneGateFirst(t *testing.T) {
	bd, dolt, _ := acceptance.BeadsBinaries()
	dir := t.TempDir()
	bin := filepath.Join(dir, "orca-acceptance.test")
	build := exec.Command("go", "test", "-c", "-tags", "acceptance orca", "-o", bin,
		"-ldflags", "-X github.com/iantanwx/loop/internal/cli.Version="+cli.Version, ".")
	build.Env = append(build.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go test -c -tags 'acceptance orca': %v\n%s", err, out)
	}
	otherLoop := filepath.Join(dir, "loop")
	if err := os.WriteFile(otherLoop, []byte("#!/bin/sh\n"+
		`echo '{"ok":true,"exit":0,"verb":"version","data":{"version":"v9.9.9"}}'`+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		percent  string
		unset    string
		env      map[string]string
		exit     int
		line     string
		commands []string
	}{
		{name: "a missing value exits 1 and calls no orca", percent: "10", unset: "LOOP_ORCA_ROOT", exit: 1,
			line: "LOOP_ORCA_ROOT is not set"},
		{name: "a spent window exits 3 after one quota read", percent: "93", exit: 3,
			line:     "quota: claude window session at 93% resets 2026-10-24T00:00:00Z",
			commands: []string{"account list --json"}},
		{name: "a missing bd or dolt exits 1 under the lane", percent: "10", exit: 1,
			env:      map[string]string{"LOOP_BD": filepath.Join(dir, "no-bd"), "LOOP_DOLT": filepath.Join(dir, "no-dolt")},
			line:     acceptance.SkipMessage,
			commands: []string{"account list --json"}},
		{name: "a loop of another version exits 1", percent: "10", exit: 1,
			env:      map[string]string{"LOOP_BD": bd, "LOOP_DOLT": dolt, acceptance.LoopBinaryEnv: otherLoop},
			line:     "acceptance: " + otherLoop + " is loop v9.9.9, and this scenario binary is " + cli.Version,
			commands: []string{"account list --json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			record := filepath.Join(tmp, "orca.log")
			orcaBin := filepath.Join(tmp, "orca")
			stub := strings.NewReplacer("%RECORD%", record, "%PERCENT%", tc.percent).Replace(laneStubOrca)
			if err := os.WriteFile(orcaBin, []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{
				"HOME":            tmp,
				"LOOP_ORCA_BIN":   orcaBin,
				"LOOP_ORCA_ROOT":  filepath.Join(tmp, "worktrees"),
				"LOOP_ORCA_REPO":  "path:" + tmp,
				"LOOP_ORCA_MODEL": "claude-opus-5-5",
			}
			for k, v := range tc.env {
				env[k] = v
			}
			delete(env, tc.unset)
			cmd := exec.Command(bin, "-test.run", "^TestF1RealAgentsTakeATicketFromClaimToMerge$")
			cmd.Dir = tmp
			for _, kv := range os.Environ() {
				name, _, _ := strings.Cut(kv, "=")
				switch {
				case name == "HOME", name == "CI", name == "LOOP_BD", name == "LOOP_DOLT", name == acceptance.LoopBinaryEnv,
					strings.HasPrefix(name, "LOOP_ORCA_"):
				default:
					cmd.Env = append(cmd.Env, kv)
				}
			}
			for k, v := range env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != tc.exit {
				t.Fatalf("the scenario binary ended with %v; want exit %d\n%s", err, tc.exit, out)
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
