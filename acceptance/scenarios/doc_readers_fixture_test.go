//go:build acceptance

package scenarios

import (
	"context"
	_ "embed"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gates"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// The C14 fixture: a PRD with no fault, and a stub reader that returns one
// false finding for each lens. The lane binary runs outside this
// directory, so both are embedded. The doc-reviewer fires once a year, so
// a tick on the real clock starts no run that the scenario does not
// dispatch.
const (
	docReadersPRDPath = "docs/prd/greeting.md"
	docReadersConfig  = `merge:
  mechanism: direct
stages:
  doc-reviewer:
    schedule: "0 0 1 1 *"
`
)

var (
	//go:embed testdata/doc_readers/greeting.md
	docReadersPRD string
	//go:embed testdata/doc_readers/loop-doc-reader.md
	docReadersStub string
)

// A real doc-reviewer runs the document gates on the PRD, and a gate
// finding would be a true fault. So the C14 PRD passes every document gate.
// This runs with no orca, so CI proves the fixture the lane runs.
func TestC14FixturePRDPassesEveryDocumentGate(t *testing.T) {
	dir := t.TempDir()
	gt.Git(t, dir, "init", "-q", "-b", "main")
	base := gt.Commit(t, dir, "seed the fixture project", map[string]string{config.ProjectConfigPath: docReadersConfig})
	gt.Commit(t, dir, "docs(prd): greeting file", map[string]string{docReadersPRDPath: docReadersPRD})
	cfg, err := config.ParseProjectConfig([]byte(docReadersConfig))
	if err != nil {
		t.Fatal(err)
	}
	var docs []gates.Gate
	for _, g := range gates.List(cfg) {
		if g.Docs {
			docs = append(docs, g)
		}
	}
	var out strings.Builder
	for _, r := range gates.Run(context.Background(), &gates.Repo{Root: dir, Base: base, Config: cfg, Out: &out}, docs) {
		if !r.OK {
			t.Errorf("the C14 PRD fails the %s gate", r.Gate)
		}
	}
	if t.Failed() {
		t.Log(out.String())
	}
}
