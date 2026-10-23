package gates

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// source is a cited file. Its heading trails a gloss, as real ones do.
const source = "# Rules\n\n## Universal constants — inherited by every strategy\n\nx\n\n## Build sequence\n"

// prdWith is a PRD with two stories and the requirement lines given.
func prdWith(reqs ...string) string {
	return "# Engine\n\n## Stories\n\n### US-01 Run a backtest\n\n> \"run it\"\n\n### US-10 See the result\n\n" +
		"## Requirements\n\n" + strings.Join(reqs, "\n") + "\n"
}

func cite(t *testing.T, text string) []Finding {
	t.Helper()
	r := testRepo(t, map[string]string{"docs/rules.md": source})
	return citeFindings(r.Root, prdFile, text)
}

func TestCitationsFullyCitedPasses(t *testing.T) {
	got := cite(t, prdWith(
		"- Fills happen at the next open.",
		"  → docs/rules.md §Universal constants",
		"- A run returns a result.",
		"  → § US-01",
		"- A result shows its value,",
		"  and its return.",
		"  → docs/rules.md",
	))
	if len(got) != 0 {
		t.Fatal(got)
	}
}

func TestCitationsThatDoNotResolve(t *testing.T) {
	cases := map[string]struct {
		line, rule string
	}{
		"missing file":           {"  → docs/nope.md", "missing_file"},
		"outside the repository": {"  → ../elsewhere.md", "missing_file"},
		"missing heading":        {"  → docs/rules.md §No such heading", "missing_heading"},
		"missing story":          {"  → §US-07", "missing_heading"},
		// A prefix match must end a word, or §US-1 reads as US-10.
		"label prefix": {"  → §US-1", "missing_heading"},
	}
	for name, c := range cases {
		got := cite(t, prdWith("- Invented.", c.line))
		if !slices.Equal(rules(got), []string{c.rule}) || got[0].Line != 14 {
			t.Errorf("%s = %v", name, got)
		}
	}
}

func TestCitationsUncitedRequirementBesideCitedOnes(t *testing.T) {
	// Citations are present, so a check for "cites anything" would pass.
	got := cite(t, prdWith("- Cited.", "  → docs/rules.md §Build sequence", "- Uncited, and so invented."))
	if !slices.Equal(rules(got), []string{"uncited_requirement"}) || got[0].Line != 15 {
		t.Fatalf("got %v", got)
	}
}

func TestCitationsHeadingMatchIgnoresCaseAndTakesAPrefix(t *testing.T) {
	if got := cite(t, prdWith("- Cited.", "  → docs/rules.md §universal CONSTANTS")); len(got) != 0 {
		t.Fatal(got)
	}
	if !headingMatches([]string{"us-10 see the result"}, "US-10") || headingMatches([]string{"us-10 see the result"}, "US-1") {
		t.Fatal("headingMatches")
	}
}

func TestCitationsBulletsOutsideRequirementsAreFree(t *testing.T) {
	text := prdWith("- Cited.", "  → § US-01") + "\n## Out of scope\n\n- Free text.\n"
	if got := cite(t, text); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestCitationsInAFenceAreExamples(t *testing.T) {
	text := prdWith("- Cited.", "  → § US-01", "", "```", "→ <path> § <heading>", "## Not a heading", "```")
	if got := cite(t, text); len(got) != 0 {
		t.Fatal(got)
	}
	if got := rules(cite(t, "# Engine\n\n## Requirements\n\n```\n- x\n→ § US-01\n```\n")); !slices.Equal(got, []string{"no_citations"}) {
		t.Fatalf("only a fenced citation = %v", got)
	}
}

func TestCitationsNoCitationsAtAll(t *testing.T) {
	if got := rules(cite(t, "# Engine\n\n## Requirements\n\nProse with no bullets.\n")); !slices.Equal(got, []string{"no_citations"}) {
		t.Fatalf("got %v", got)
	}
}

func TestStoryFindings(t *testing.T) {
	if got := rules(storyFindings(prdFile, "# Engine\n\nNo stories here.\n")); !slices.Equal(got, []string{"no_stories"}) {
		t.Fatalf("no label = %v", got)
	}
	if got := storyFindings(prdFile, prdWith()); len(got) != 0 {
		t.Fatalf("two stories = %v", got)
	}
	got := storyFindings(prdFile, prdWith()+"\n### US-01 Run it again\n")
	if !slices.Equal(rules(got), []string{"duplicate_story"}) || !strings.Contains(got[0].Message, "line 5") {
		t.Fatalf("a label defined twice = %v", got)
	}
}

func TestCitationsDroppedStory(t *testing.T) {
	ok := prdWith("- Cited.", "  → § US-01")
	r := testRepo(t, map[string]string{prdFile: ok, "docs/prd/loop-b1-gone.md": ok})
	ctx := context.Background()
	if got, err := Citations(ctx, r); err != nil || len(got) != 0 {
		t.Fatalf("unchanged = %v %v", got, err)
	}
	// A label can be added. One that the base held stays.
	commit(t, r, "docs: add a story", map[string]string{prdFile: ok + "\n### US-11 Share it\n"})
	if got, err := Citations(ctx, r); err != nil || len(got) != 0 {
		t.Fatalf("added = %v %v", got, err)
	}
	commit(t, r, "docs: drop a story", map[string]string{prdFile: strings.Replace(ok, "### US-10 See the result\n", "", 1)})
	git(t, r.Root, "rm", "-q", "docs/prd/loop-b1-gone.md")
	got, err := Citations(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rules(got), []string{"dropped_story", "dropped_story", "dropped_story"}) {
		t.Fatalf("dropped = %v", got)
	}
	if got[0].File != prdFile || !strings.Contains(got[0].Message, "US-10") || got[1].File != "docs/prd/loop-b1-gone.md" || !strings.Contains(got[1].Message, "gone") {
		t.Fatalf("dropped = %v", got)
	}
}

func TestCitationsWithNoMergeBase(t *testing.T) {
	r := testRepo(t, map[string]string{prdFile: prdWith("- Cited.", "  → § US-01")})
	r.Base = "origin/nope"
	if got, err := Citations(context.Background(), r); err != nil || len(got) != 0 {
		t.Fatalf("got %v %v", got, err)
	}
}
