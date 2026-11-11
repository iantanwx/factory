package ci

import (
	"encoding/json"
	"fmt"
	"go/version"
	"os/exec"
	"path/filepath"
	"testing"
)

// The newest releases that carry every fix the audit gate reports as
// called by this module's code, at govulncheck's database of 2026-11-11.
const (
	patchedGo   = "go1.26.11"
	patchedXNet = "v0.60.0"
)

type modFile struct {
	Go        string
	Toolchain string
	Require   []struct{ Path, Version string }
}

func goMod(t *testing.T) modFile {
	t.Helper()
	cmd := exec.Command("go", "mod", "edit", "-json", filepath.Join("..", "..", "go.mod"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var m modFile
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// semverAtLeast reports whether module version a is at least b, for the
// plain vX.Y.Z versions the floor holds.
func semverAtLeast(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y [3]int
	if _, err := fmt.Sscanf(a, "v%d.%d.%d", &x[0], &x[1], &x[2]); err != nil {
		t.Fatalf("%s: %v", a, err)
	}
	if _, err := fmt.Sscanf(b, "v%d.%d.%d", &y[0], &y[1], &y[2]); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return true
}

func TestActionBuildsWithAPatchedGoAndXNet(t *testing.T) {
	m := goMod(t)
	// action.yml gives setup-go the go.mod file, and setup-go installs its
	// toolchain line, else its go line.
	resolved := m.Toolchain
	if resolved == "" {
		resolved = "go" + m.Go
	}
	if version.Compare(resolved, patchedGo) < 0 {
		t.Errorf("CI builds and scans with %s, want at least %s", resolved, patchedGo)
	}
	for _, r := range m.Require {
		if r.Path == "golang.org/x/net" && !semverAtLeast(t, r.Version, patchedXNet) {
			t.Errorf("golang.org/x/net = %s, want at least %s", r.Version, patchedXNet)
		}
	}
}
