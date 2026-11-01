package pack_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

const contentLens = "skills/loop-content-lens/SKILL.md"

// lenses returns the title and the text of each level-3 heading in the
// level-2 section whose title starts with prefix, in document order.
func lenses(t *testing.T, text, prefix string) (titles, bodies []string) {
	t.Helper()
	s := pack.Section(text, 2, func(title string) bool { return strings.HasPrefix(title, prefix) })
	if s == "" {
		t.Fatalf("the content lens holds no section %q", prefix)
	}
	lines := strings.Split(s, "\n")
	for i, level := range pack.Headings(lines) {
		switch {
		case level == 3:
			titles = append(titles, strings.TrimSpace(lines[i][4:]))
			bodies = append(bodies, "")
		case level == 0 && len(bodies) > 0:
			bodies[len(bodies)-1] += lines[i] + "\n"
		}
	}
	return titles, bodies
}

// C13: the fork comment names each upstream with the commit the fork
// read, in the shape of loop-unslop: an upstream line, then a commit line
// of 40 hex characters.
func TestContentLensForkCommentNamesEachUpstreamAndItsCommit(t *testing.T) {
	text := read(t, contentLens)
	_, rest, ok := strings.Cut(text, "<!--")
	comment, _, closed := strings.Cut(rest, "-->")
	if !ok || !closed {
		t.Fatal("the content lens holds no fork comment")
	}
	for repo, want := range map[string]string{
		"EveryInc/compound-engineering-plugin": "8f7eea958c8f1318147db6c93320cae3930ad09a",
		"cursor/plugins":                       "a58628271271837ef5f386adca29c0812683a19a",
	} {
		re := regexp.MustCompile(`upstream: +https://github\.com/` + regexp.QuoteMeta(repo) + `/\S*\n +commit: +([0-9a-f]{40})\n`)
		m := re.FindStringSubmatch(comment)
		if m == nil {
			t.Errorf("the fork comment names no upstream %s with a 40-character commit", repo)
			continue
		}
		if m[1] != want {
			t.Errorf("the fork comment pins %s at %s, want %s", repo, m[1], want)
		}
	}
	if !strings.Contains(comment, "pstack/skills/architect/references/design-red-flags.md") {
		t.Error("the fork comment does not name the design red flags file of cursor/plugins")
	}
}

// C13: coherence and feasibility run on every document, by the
// operator's "2 always, rest by signal" answer.
func TestContentLensRunsCoherenceAndFeasibilityAlways(t *testing.T) {
	titles, _ := lenses(t, read(t, contentLens), "Lenses that always run")
	if want := []string{"Coherence", "Feasibility"}; !slices.Equal(titles, want) {
		t.Errorf("the lenses that always run are %q, want %q", titles, want)
	}
}

// C13: each other lens names the signal that turns it on.
func TestContentLensTurnsEachOtherLensOnBySignal(t *testing.T) {
	titles, bodies := lenses(t, read(t, contentLens), "Lenses that run on a signal")
	if want := []string{"Product", "Design", "Security", "Scope", "Adversarial"}; !slices.Equal(titles, want) {
		t.Fatalf("the lenses that run on a signal are %q, want %q", titles, want)
	}
	signal := regexp.MustCompile(`(?m)^\*\*Signal:\*\* \S`)
	for i, body := range bodies {
		if !signal.MatchString(body) {
			t.Errorf("lens %s names no signal on a line that starts with **Signal:**", titles[i])
		}
	}
}

// C13: the design red flags check a spec and an ADR, and a PRD gets none,
// because a PRD names no module design.
func TestContentLensAppliesTheRedFlagsToASpecAndAnADROnly(t *testing.T) {
	text := read(t, contentLens)
	titles, _ := lenses(t, text, "Design red flags")
	if len(titles) != 8 {
		t.Errorf("the content lens holds %d design red flags, want 8: %q", len(titles), titles)
	}
	s := strings.Join(strings.Fields(pack.Section(text, 2, func(title string) bool {
		return strings.HasPrefix(title, "Design red flags")
	})), " ")
	for _, want := range []string{"a spec and an ADR", "A PRD gets no red flag"} {
		if !strings.Contains(s, want) {
			t.Errorf("the design red flags section does not hold %q", want)
		}
	}
}

// The doc-reviewer's run receives the skill, and its runbook names the
// path and leaves the lenses to the reader.
func TestDocReviewerReceivesTheContentLens(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Runbooks["doc-reviewer"].Skills, "loop-content-lens") {
		t.Error("the doc-reviewer manifest entry does not list loop-content-lens")
	}
	s := strings.Join(strings.Fields(section(t, read(t, "runbooks/doc-reviewer.md"), "1. Before you review")), " ")
	for _, want := range []string{"`../loop-content-lens/SKILL.md`", "You do not apply them yourself"} {
		if !strings.Contains(s, want) {
			t.Errorf("the doc-reviewer runbook § 1 does not hold %q", want)
		}
	}
}
