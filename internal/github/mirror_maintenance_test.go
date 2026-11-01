package github

import (
	"os"
	"path/filepath"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// A fetch into the mirror starts no auto maintenance. A detached
// maintenance outlives the fetch and writes into objects/ while
// t.TempDir's cleanup removes the mirror. The mirror's own config makes
// the commit-graph task run on every `maintenance run --auto`, and in the
// foreground, so a commit-graph file shows that auto maintenance started.
func TestMirrorFetchStartsNoAutoMaintenance(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	r := e.reactor()
	e.land(r, map[string]string{"a.md": "a\n"})
	mirror := filepath.Join(e.root, testProject+".git")
	for _, kv := range [][2]string{
		{"maintenance.autoDetach", "false"},
		{"maintenance.commit-graph.enabled", "true"},
		{"maintenance.commit-graph.auto", "-1"},
	} {
		gt.Git(t, mirror, "config", kv[0], kv[1])
	}
	e.land(r, map[string]string{"b.md": "b\n"})
	for _, p := range []string{"objects/info/commit-graph", "objects/info/commit-graphs"} {
		if _, err := os.Stat(filepath.Join(mirror, p)); err == nil {
			t.Fatalf("a fetch into the mirror ran auto maintenance: %s exists", p)
		}
	}
}
