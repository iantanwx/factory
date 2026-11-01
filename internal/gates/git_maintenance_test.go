package gates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The git helper of this package's tests starts no detached git
// maintenance. A detached maintenance outlives the helper and writes into
// .git/objects while t.TempDir's cleanup removes it (factorio-d14g).
func TestGitHelperStartsNoAutoMaintenance(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)
	r := testRepo(t, map[string]string{"a.txt": "a"})
	git(t, r.Root, "switch", "-q", "-c", "work")
	commit(t, r, "feat: b", map[string]string{"b.txt": "b"})
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "maintenance run --auto") || strings.Contains(line, "gc --auto") {
			t.Fatalf("the git helper started auto maintenance: %s", line)
		}
	}
}
