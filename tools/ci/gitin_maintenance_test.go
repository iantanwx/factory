package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A local clone hardlinks the object files of its source. A git that
// still packs objects in the source makes the clone fail with "hardlink
// different from source" (factorio-s7l9). So no git that gitIn runs may
// leave an auto maintenance behind, whatever the repository asks for.
func TestGitInStartsNoAutoMaintenance(t *testing.T) {
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	gitIn(t, tmp, "init", "-q", "-b", "main", work)
	// A repository whose auto maintenance packs at the first loose object,
	// as the git of a CI runner may.
	gitIn(t, work, "config", "maintenance.loose-objects.enabled", "true")
	gitIn(t, work, "config", "maintenance.loose-objects.auto", "1")
	trace := filepath.Join(tmp, "trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	gitIn(t, work, "commit", "-q", "--allow-empty", "-m", "c0")
	gitIn(t, work, "tag", "v0.0.1")
	gitIn(t, work, "commit", "-q", "--allow-empty", "-m", "c1")
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, `"event":"child_start"`) && (strings.Contains(line, `"maintenance"`) || strings.Contains(line, `"gc"`)) {
			t.Fatalf("gitIn left an auto maintenance running in %s: %s", work, line)
		}
	}
}
