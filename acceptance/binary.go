//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
)

// Binary is the loop binary every CLI process runs. The scenarios'
// TestMain builds it with BuildBinary, or takes the one LoopBinaryEnv names.
var Binary string

// LoopBinaryEnv names the loop binary a compiled scenario binary runs. A
// compiled binary on the host has no module and no toolchain to build one.
const LoopBinaryEnv = "LOOP_ACCEPTANCE_LOOP"

// SkipMessage says why a world skips.
const SkipMessage = "acceptance: the pinned bd and dolt were not found; set LOOP_BD and LOOP_DOLT, " +
	"or install them in $HOME/.cache/loop-dev/bin"

// BeadsBinaries finds the pinned bd and dolt: LOOP_BD and LOOP_DOLT, or
// $HOME/.cache/loop-dev/bin, as the beads contract suite does.
func BeadsBinaries() (bd, dolt string, ok bool) {
	home, _ := os.UserHomeDir()
	find := func(env, name string) string {
		if v := os.Getenv(env); v != "" {
			return v
		}
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".cache", "loop-dev", "bin", name)
	}
	bd, dolt = find("LOOP_BD", "bd"), find("LOOP_DOLT", "dolt")
	for _, p := range []string{bd, dolt} {
		if st, err := os.Stat(p); p == "" || err != nil || st.IsDir() {
			return "", "", false
		}
	}
	return bd, dolt, true
}

// BuildBinary builds cmd/loop into dir. Its version is the version this
// test process serves, so the CLI and the server pass the handshake.
func BuildBinary(dir string) (string, error) {
	return BuildVersion(dir, cli.Version)
}

// BuildVersion builds cmd/loop into dir with one version.
func BuildVersion(dir, version string) (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "loop-"+version)
	cmd := exec.Command("go", "build", "-trimpath", "-o", out,
		"-ldflags", "-X github.com/iantanwx/loop/internal/cli.Version="+version, "./cmd/loop")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build loop: %w\n%s", err, b)
	}
	return out, nil
}

// CheckLoop refuses a loop binary whose `loop version` is not the version
// this test process serves, because the CLI and the server would then fail
// the handshake.
func CheckLoop(bin string) error {
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return fmt.Errorf("acceptance: %s version: %w", bin, err)
	}
	var env struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("acceptance: %s version printed no envelope: %w", bin, err)
	}
	if env.Data.Version != cli.Version {
		return fmt.Errorf("acceptance: %s is loop %s, and this scenario binary is %s", bin, env.Data.Version, cli.Version)
	}
	return nil
}

func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		return "", fmt.Errorf("acceptance: not inside the loop module")
	}
	return filepath.Dir(mod), nil
}
