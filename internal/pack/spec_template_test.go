package pack_test

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

const specTemplate = "skills/loop-sdlc-documents/spec.md"

// contractsSub returns the level-3 subsection of the spec template's
// Contracts section whose title is exactly title, with each run of white
// space as one space, so a wrapped line still matches.
func contractsSub(t *testing.T, title string) string {
	t.Helper()
	contracts := pack.Section(read(t, specTemplate), 2, func(s string) bool { return s == "Contracts" })
	if contracts == "" {
		t.Fatalf("%s holds no ## Contracts section", specTemplate)
	}
	sub := pack.Section(contracts, 3, func(s string) bool { return s == title })
	if sub == "" {
		t.Fatalf("the Contracts section of %s holds no ### %s", specTemplate, title)
	}
	return sub
}

// The spec template's Contracts section keeps the rules a contract obeys
// under a ### Rules heading of its own, so a spec author and a
// doc-reviewer find them by name (tickertown docs/templates/spec.md).
func TestSpecTemplateContractsHoldARulesSubsection(t *testing.T) {
	rules := strings.Join(strings.Fields(contractsSub(t, "Rules")), " ")
	for _, want := range []string{
		"**Number them.**",
		"**Cite the criterion.**",
		"**Each one must be falsifiable.**",
		"**State the promise, never the implementation.**",
		"**Include what must fail.**",
		"**Cover the boundaries between work entries.**",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("### Rules does not hold the rule %q", want)
		}
	}
}

// The kinds of contract and the proof each one takes sit under a
// ### Kinds, and how each is proven heading, with the rule that picks the
// strongest kind the problem allows (tickertown docs/templates/spec.md).
func TestSpecTemplateContractsHoldAKindsSubsection(t *testing.T) {
	sub := contractsSub(t, "Kinds, and how each is proven")
	rows := pack.Table(sub, "kind", "shape", "proven by")
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, strings.Trim(pack.Bare(r.Cells[0]), "*"))
	}
	if got, want := strings.Join(kinds, ","), "Invariant,Behaviour,Refusal,Equivalence,Boundary"; got != want {
		t.Errorf("the kinds table holds %q, want %q", got, want)
	}
	if !strings.Contains(strings.Join(strings.Fields(sub), " "), "Prefer the strongest kind the problem allows.") {
		t.Error("### Kinds, and how each is proven does not say to prefer the strongest kind")
	}
}
