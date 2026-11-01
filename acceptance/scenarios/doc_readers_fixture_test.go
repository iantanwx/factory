//go:build acceptance

package scenarios

import (
	"context"
	_ "embed"
	"encoding/json"
	"regexp"
	"slices"
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
	gt.Commit(t, dir, "docs(prd): add the PRD of Greeting file", map[string]string{docReadersPRDPath: docReadersPRD})
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

// A reply that holds no report of the schema's shape is a failed reader,
// and a reader that fails twice blocks the review. So the C14 stub holds
// one report for each lens, in start order, each with one finding whose
// four fields hold text.
func TestC14StubReaderHoldsOneFalseFindingForEachLens(t *testing.T) {
	var lenses []string
	for _, m := range regexp.MustCompile("(?s)```json\n(.*?)\n```").FindAllStringSubmatch(docReadersStub, -1) {
		var r struct {
			Reader   string `json:"reader"`
			Findings []struct {
				Where, Rule, Edit, Why string
			} `json:"findings"`
			Questions []any `json:"questions"`
		}
		if err := json.Unmarshal([]byte(m[1]), &r); err != nil {
			t.Fatalf("a report of the stub does not parse: %v\n%s", err, m[1])
		}
		if len(r.Findings) != 1 || r.Questions == nil || slices.Contains([]string{r.Findings[0].Where, r.Findings[0].Rule, r.Findings[0].Edit, r.Findings[0].Why}, "") {
			t.Fatalf("the %s report = %+v, want one finding with four fields and no question", r.Reader, r)
		}
		lenses = append(lenses, r.Reader)
	}
	if want := []string{"unslop", "asd-ste100", "content-lens"}; !slices.Equal(lenses, want) {
		t.Fatalf("the stub reports for %q, want %q", lenses, want)
	}
}
