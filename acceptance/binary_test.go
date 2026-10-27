//go:build acceptance

package acceptance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
)

// stubLoop writes a loop that answers `loop version` with version.
func stubLoop(t *testing.T, version string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "loop")
	script := "#!/bin/sh\n[ \"$*\" = version ] || exit 2\n" +
		`echo '{"ok":true,"exit":0,"verb":"version","data":{"version":"` + version + `"}}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// C12: the scenario binary runs only a loop of its own version.
func TestC12TheScenarioBinaryRunsOnlyALoopOfItsOwnVersion(t *testing.T) {
	if err := acceptance.CheckLoop(stubLoop(t, cli.Version)); err != nil {
		t.Fatalf("a loop of this version is refused: %v", err)
	}
	err := acceptance.CheckLoop(stubLoop(t, "v9.9.9"))
	if err == nil {
		t.Fatal("a loop of another version is taken")
	}
	for _, v := range []string{"v9.9.9", cli.Version} {
		if !strings.Contains(err.Error(), v) {
			t.Fatalf("the refusal %q does not name %s", err, v)
		}
	}
	if err := acceptance.CheckLoop(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("an absent loop is taken")
	}
}
