package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A commit and a push through gitT start no detached git maintenance. A
// detached maintenance outlives gitT and writes objects/maintenance.lock
// into the repository while t.TempDir's cleanup removes it.
func TestGitTStartsNoDetachedMaintenance(t *testing.T) {
	repo := projectRepo(t, "merge:\n  mechanism: direct\n")
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "f")
	gitT(t, repo, "commit", "-q", "-m", "f")
	gitT(t, repo, "push", "-q", "origin", "main")
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "maintenance run --auto") {
			t.Fatalf("gitT started auto maintenance: %s", line)
		}
	}
}
