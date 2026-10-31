package gates

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/pack"
)

const adrFile = "docs/adr/0042-a-decision.md"

// whyLines answer the four parts of the test, one line each.
const whyLines = "- **More than one epic:** every epic that writes a gate.\n" +
	"- **Rejected alternative:** the exciting option.\n" +
	"- **Costly to undo:** it changes the stored format.\n" +
	"- **How, not what:** it decides how, for US-01."

// adrText is a minimal ADR that passes, so each case changes one part.
func adrText(over map[string]string) string {
	parts := []struct{ key, text string }{
		{"heading", "# ADR 0042: We choose the boring option"},
		{"status", "**Status:** Accepted"},
		{"date", "**Date:** 2026-09-28"},
		{"ticket", "**Ticket:** loop-a3f8"},
		{"binds", "**Binds:** all"},
		{"context", "## Context\n\nSomething forced a choice."},
		{"decision", "## Decision\n\nThe boring option. We rejected the exciting one, because it is slow."},
		{"why", "## Why an ADR\n\n" + whyLines},
		{"consequences", "## Consequences\n\nThis makes one thing easy and another hard."},
		{"criteria", "## Acceptance criteria\n\n- `loop gates` passes."},
	}
	var out []string
	for _, p := range parts {
		t, ok := over[p.key]
		if !ok {
			t = p.text
		}
		if t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n\n") + "\n"
}

func adrRules(file, text string) []string { return rules(checkADR(file, text)) }

func TestADRWellFormedReportsNothing(t *testing.T) {
	if got := checkADR(adrFile, adrText(nil)); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestADRTicketLine(t *testing.T) {
	// The Ticket line joins an ADR to its architecture epic. With no line
	// the epic waits, with no error anywhere.
	if got := adrRules(adrFile, adrText(map[string]string{"ticket": ""})); !slices.Contains(got, "ticket") {
		t.Fatalf("no Ticket line = %v", got)
	}
	// none is legal, for a decision recorded outside the pipeline.
	if got := checkADR(adrFile, adrText(map[string]string{"ticket": "**Ticket:** none (recorded outside)"})); len(got) != 0 {
		t.Fatalf("none = %v", got)
	}
	// A child id, and an id that ends a sentence, are ids.
	for _, id := range []string{"loop-a3f8.1", "alpha-9.", "bd-1z"} {
		if got := checkADR(adrFile, adrText(map[string]string{"ticket": "**Ticket:** " + id})); len(got) != 0 {
			t.Fatalf("%s = %v", id, got)
		}
	}
	// A malformed id looks present, and the server joins it to nothing.
	for _, id := range []string{"LOOP-42", "42", "#42", "loop_42"} {
		if got := adrRules(adrFile, adrText(map[string]string{"ticket": "**Ticket:** " + id})); !slices.Contains(got, "ticket") {
			t.Fatalf("%s = %v", id, got)
		}
	}
}

func TestADRNumber(t *testing.T) {
	if got := adrRules(adrFile, adrText(map[string]string{"heading": "# ADR 0043: We choose the boring option"})); !slices.Contains(got, "number") {
		t.Fatalf("heading and filename disagree = %v", got)
	}
	if got := adrRules("docs/adr/a-decision.md", adrText(nil)); !slices.Contains(got, "name") {
		t.Fatalf("no number in the filename = %v", got)
	}
	if got := adrRules(adrFile, adrText(map[string]string{"heading": "# Runbook: architect"})); !slices.Contains(got, "heading") {
		t.Fatalf("no ADR heading = %v", got)
	}
	if adrNumber("docs/adr/0011-fmp.md") != "0011" || adrNumber("docs/adr/fmp.md") != "" {
		t.Fatal("adrNumber")
	}
}

func TestADRHeader(t *testing.T) {
	if got := adrRules(adrFile, adrText(map[string]string{"status": "**Status:** Probably"})); !slices.Contains(got, "status") {
		t.Fatalf("unknown status = %v", got)
	}
	// An ADR is never deleted, so Superseded is a legal status.
	if got := checkADR(adrFile, adrText(map[string]string{"status": "**Status:** Superseded by ADR 0099"})); len(got) != 0 {
		t.Fatalf("superseded = %v", got)
	}
	if got := adrRules(adrFile, adrText(map[string]string{"date": "**Date:** September 2026"})); !slices.Contains(got, "date") {
		t.Fatalf("date = %v", got)
	}
}

func TestADRBody(t *testing.T) {
	cases := map[string]struct {
		over map[string]string
		rule string
	}{
		"template block left in place": {map[string]string{"context": "> Copy this file to `docs/adr/NNNN-<slug>.md`.\n\n## Context\n\nx."}, "template"},
		"missing Consequences":         {map[string]string{"consequences": ""}, "section"},
		"missing Acceptance criteria":  {map[string]string{"criteria": ""}, "section"},
		// A choice with no alternative is an assumption, not a decision.
		"no rejected alternative":  {map[string]string{"decision": "## Decision\n\nThe boring option. It is the one we will use."}, "rejection"},
		"criteria with no item":    {map[string]string{"criteria": "## Acceptance criteria\n\nIt should work well enough."}, "criteria"},
		"decision in another part": {map[string]string{"decision": "## Decision\n\nThe boring option.", "consequences": "## Consequences\n\nWe rejected nothing here."}, "rejection"},
	}
	for name, c := range cases {
		if got := adrRules(adrFile, adrText(c.over)); !slices.Contains(got, c.rule) {
			t.Errorf("%s = %v, want %s", name, got, c.rule)
		}
	}
}

func TestSectionBody(t *testing.T) {
	body, ok := sectionBody(adrText(nil), "Context")
	if !ok || !strings.Contains(body, "Something forced a choice") || strings.Contains(body, "boring option") {
		t.Fatalf("Context = %q %v", body, ok)
	}
	// Absent is not empty: only one of the two is a defect worth a finding.
	if _, ok := sectionBody(adrText(nil), "Alternatives"); ok {
		t.Fatal("an absent section reads as present")
	}
	if body, ok := sectionBody("## Context\n## Decision\nx\n", "Context"); !ok || strings.TrimSpace(body) != "" {
		t.Fatalf("empty section = %q %v", body, ok)
	}
}

func TestADRGateReadsTheSet(t *testing.T) {
	other := strings.Replace(adrText(nil), "ADR 0042", "ADR 0007", 1)
	r := testRepo(t, map[string]string{
		adrFile:                     strings.Replace(adrText(nil), "Something forced a choice.", "See docs/adr/0007 and `docs/adr/0009`.", 1),
		"docs/adr/0007-earlier.md":  other,
		"docs/adr/0042-again.md":    adrText(nil),
		"docs/adr/README.txt":       "not an ADR",
		"docs/prd/unrelated-prd.md": "# x\n",
	})
	got, err := ADR(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"duplicate", "citation"}; !slices.Equal(rules(got), want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
	if got[0].File != "docs/adr/0042-again.md" || got[1].File != adrFile || !strings.Contains(got[1].Message, "0009") {
		t.Fatalf("findings = %v", got)
	}
}

// The pack's ADR template and the gate agree: the raw template fails, and
// the template with its block deleted and its header filled passes, also
// as a new ADR.
func TestADRTemplateAgreesWithTheGate(t *testing.T) {
	raw, err := fs.ReadFile(pack.Files, "skills/loop-sdlc-documents/adr.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if got := adrRules(adrFile, text); !slices.Contains(got, "template") || !slices.Contains(got, "ticket") {
		t.Fatalf("raw template = %v", got)
	}
	var kept []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(l, ">") {
			kept = append(kept, l)
		}
	}
	filled := strings.NewReplacer(
		"# ADR NNNN: <the decision, in one line>", "# ADR 0042: We choose the boring option",
		"**Status:** Proposed | Accepted", "**Status:** Proposed",
		"**Date:** YYYY-MM-DD", "**Date:** 2026-10-23",
		"**Ticket:** <epic id>", "**Ticket:** loop-a3f8",
		"**Binds:** <epic ids, comma-separated> | all", "**Binds:** all",
	).Replace(strings.Join(kept, "\n"))
	if got := append(checkADR(adrFile, filled), checkNewADR(adrFile, filled)...); len(got) != 0 {
		t.Fatalf("filled template = %v", got)
	}
}

func TestADRGateWithNoDirectory(t *testing.T) {
	r := testRepo(t, nil)
	if got, err := ADR(context.Background(), r); err != nil || len(got) != 0 {
		t.Fatalf("no docs/adr = %v %v", got, err)
	}
}

func newADRRules(text string) []string { return rules(checkNewADR(adrFile, text)) }

func TestNewADRWellFormedReportsNothing(t *testing.T) {
	if got := checkNewADR(adrFile, adrText(nil)); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestC6NewADRWithNoBindsLineFails(t *testing.T) {
	got := checkNewADR(adrFile, adrText(map[string]string{"binds": ""}))
	if !slices.Equal(rules(got), []string{"binds"}) || !strings.Contains(got[0].Message, "**Binds:**") {
		t.Fatalf("no Binds line = %v", got)
	}
	for _, v := range []string{"", "some epics", "ALL", "loop-a3f8 and loop-b1", "loop-a3f8,,loop-b1", "LOOP-42", "all, loop-a3f8"} {
		got := checkNewADR(adrFile, adrText(map[string]string{"binds": "**Binds:** " + v}))
		if !slices.Equal(rules(got), []string{"binds"}) || !strings.Contains(got[0].Message, "**Binds:**") {
			t.Errorf("Binds %q = %v", v, got)
		}
	}
	for _, v := range []string{"all", "loop-a3f8", "loop-a3f8, loop-b1.2", "loop-a3f8,factorio-ku99"} {
		if got := checkNewADR(adrFile, adrText(map[string]string{"binds": "**Binds:** " + v})); len(got) != 0 {
			t.Errorf("Binds %q = %v", v, got)
		}
	}
}

func TestC7NewADRWithNoCompleteWhyAnADRSectionFails(t *testing.T) {
	cases := map[string]struct {
		why     string
		missing []string
	}{
		"no section":      {"", []string{"## Why an ADR"}},
		"three of four":   {"## Why an ADR\n\n" + strings.Replace(whyLines, "- **Costly to undo:** it changes the stored format.\n", "", 1), []string{"**Costly to undo:**"}},
		"one empty label": {"## Why an ADR\n\n" + strings.Replace(whyLines, "the exciting option.", "", 1), []string{"**Rejected alternative:**"}},
		"labels as prose": {"## Why an ADR\n\nMore than one epic: all of them.", []string{"**More than one epic:**", "**Rejected alternative:**", "**Costly to undo:**", "**How, not what:**"}},
	}
	for name, c := range cases {
		got := checkNewADR(adrFile, adrText(map[string]string{"why": c.why}))
		if !slices.Equal(rules(got), []string{"why_an_adr"}) || !strings.Contains(got[0].Message, "## Why an ADR") {
			t.Errorf("%s = %v", name, got)
			continue
		}
		for _, m := range c.missing {
			if !strings.Contains(got[0].Message, m) {
				t.Errorf("%s: message %q does not name %s", name, got[0].Message, m)
			}
		}
	}
	// A label in another section does not answer the test.
	moved := adrText(map[string]string{"why": "## Why an ADR\n\nSee below.", "consequences": "## Consequences\n\n" + whyLines})
	if got := newADRRules(moved); !slices.Equal(got, []string{"why_an_adr"}) {
		t.Errorf("labels under Consequences = %v", got)
	}
}

func TestC8ADROnTheBasePassesWithNoEdit(t *testing.T) {
	old := strings.Replace(adrText(map[string]string{"binds": "", "why": ""}), "ADR 0042", "ADR 0007", 1)
	r := testRepo(t, map[string]string{"docs/adr/0007-earlier.md": old})
	ctx := context.Background()
	if got, err := ADR(ctx, r); err != nil || len(got) != 0 {
		t.Fatalf("on the base = %v %v", got, err)
	}
	// An edit on the branch keeps an ADR on the base. A new ADR, committed
	// or not, needs the Binds line and the Why an ADR section.
	commit(t, r, "docs: edit an ADR and add one", map[string]string{
		"docs/adr/0007-earlier.md": old + "\nA later note.\n",
		adrFile:                    adrText(map[string]string{"binds": ""}),
	})
	write(t, r.Root, map[string]string{"docs/adr/0043-unstaged.md": strings.Replace(adrText(map[string]string{"why": ""}), "ADR 0042", "ADR 0043", 1)})
	got, err := ADR(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rules(got), []string{"binds", "why_an_adr"}) || got[0].File != adrFile || got[1].File != "docs/adr/0043-unstaged.md" {
		t.Fatalf("new ADRs = %v", got)
	}
	// With no merge base, every ADR counts as one on the base.
	r.Base = "origin/nope"
	if got, err := ADR(ctx, r); err != nil || len(got) != 0 {
		t.Fatalf("no merge base = %v %v", got, err)
	}
}
