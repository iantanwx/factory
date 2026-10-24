package bodygate

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// words is n words of plain prose, ten to a line.
func words(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString("word")
		if (i+1)%10 == 0 {
			b.WriteString("\n")
		} else {
			b.WriteString(" ")
		}
	}
	return b.String()
}

func rules(fs []Fault) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return out
}

// The budget counts 700 words of prose, and the 701st refuses.
func TestPRBudgetRefusesTheWordOverSevenHundred(t *testing.T) {
	if got := PRBudget(words(700)); got != "" {
		t.Fatalf("700 words refused:\n%s", got)
	}
	got := PRBudget(words(701))
	if !strings.Contains(got, "budget 700 words, actual 701") {
		t.Fatalf("701 words: refusal %q does not name the budget and the count", got)
	}
}

// A fenced block and a table row sit outside the whole-body count.
func TestPRBudgetLeavesFencesAndTableRowsOutOfTheCount(t *testing.T) {
	body := words(690) + "```\n" + words(200) + "```\n~~~\n" + words(50) + "~~~\n" +
		"| contract | test | red | green |\n| C1 | TestX | " + strings.Repeat("w ", 40) + "| ok |\n"
	if got := PRBudget(body); got != "" {
		t.Fatalf("690 prose words with fences and rows refused:\n%s", got)
	}
	if prose, _ := Parts(body); prose != 690 {
		t.Fatalf("Parts counted %d prose words, want 690", prose)
	}
}

// A table row holds 60 words at most, so a paragraph cannot hide in a table.
// The cell delimiter is no word.
func TestPRBudgetRefusesATableRowOverSixtyWords(t *testing.T) {
	ok := "intro\n\n| " + strings.Repeat("w | ", 60) + "\n"
	if got := PRBudget(ok); got != "" {
		t.Fatalf("a 60-word row with 61 delimiters refused:\n%s", got)
	}
	over := "intro\n\n| " + strings.Repeat("w ", 61) + "|\n"
	got := PRBudget(over)
	if !strings.Contains(got, "table row at line 3  budget 60 words, actual 61") {
		t.Fatalf("a 61-word row: refusal %q does not name line 3 and the count", got)
	}
}

// Rule 1: an opening ** right after a code span refuses. A closing ** there
// is the ordinary idiom, and a glob inside a code span is no delimiter.
func TestMarkupRefusesACodeSpanThatReopensABoldRun(t *testing.T) {
	bad := "**The tests send no forged** `x-forwarded-for`**.**\n"
	if got := rules(MarkupFaults(bad)); !slices.Contains(got, "bold-after-code") {
		t.Fatalf("the reopened bold run: rules %v, want bold-after-code", got)
	}
	for _, good := range []string{
		"**Never move an epic to `Done`** unless the merge landed.\n",
		"**The tests send no forged `x-forwarded-for`.**\n",
		"It reads `packages/db/**` and nothing else.\n",
	} {
		if got := MarkupFaults(good); len(got) != 0 {
			t.Fatalf("%q: faults %v, want none", good, got)
		}
	}
}

// Rule 2: one bold span to a paragraph or a list item. A table row and a
// blockquote are outside the count, and a blank line ends a block.
func TestMarkupRefusesTwoBoldSpansInOneBlock(t *testing.T) {
	bad := "Intro.\n\n**One** lead-in, and then\na second **bold** phrase.\n"
	got := MarkupFaults(bad)
	if len(got) != 1 || got[0].Rule != "bold-texture" || got[0].Line != 3 {
		t.Fatalf("two bold spans in the block at line 3: faults %v", got)
	}
	items := "- **One** item.\n- **Two** item.\n\n**Lead** paragraph.\n\n**Next** paragraph.\n"
	if got := MarkupFaults(items); len(got) != 0 {
		t.Fatalf("one bold span to each block: faults %v, want none", got)
	}
	rest := "| **a** | **b** |\n> **c** and **d**\n```\n**e** **f**\n```\n"
	if got := MarkupFaults(rest); len(got) != 0 {
		t.Fatalf("rows, quotes and fences: faults %v, want none", got)
	}
}

// Each unslop-lint rule and each ste-lint rule in steRefuse refuses, and a
// code span or a fenced block escapes it. A ste-lint rule that needs
// judgement refuses nothing.
func TestProseRefusesEachMechanicalRule(t *testing.T) {
	cases := []struct{ text, rule string }{
		{"a sentence — with a dash", "em-dash"},
		{"a range 1–2", "em-dash"},
		{"it said “hello”", "curly-quote"},
		{"it is crucial to delve in", "ai-vocabulary"},
		{"the module serves as a bridge", "fancy-is"},
		{"not just fast, but correct", "not-just"},
		{"in order to run it", "filler"},
		{"we utilize the parser", "fancy-word"},
		{"the substrate below", "metaphor-noun"},
		{"I hope this helps", "chatbot"},
		{"## Title Case Heading Here", "title-case"},
		{"## Known limitations, recorded rather than hidden", "self-praise"},
		{"## Warning ⚠️", "emoji-heading"},
		{"one clause; another clause", "semicolon"},
		{"we spin up a server", "phrasal-verb"},
		{"a seamless change", "marketing-adjective"},
		{"we perform an analysis of it", "nominalization"},
	}
	for _, c := range cases {
		if got := rules(proseOf(t, c.text)); !slices.Contains(got, c.rule) {
			t.Errorf("%q: rules %v, want %s", c.text, got, c.rule)
		}
	}
	clean := "The parser rejects a bad date. It exits with code 2.\n" +
		"It runs `a; b — c` in a span.\n```\nx; y — z\n```\n" +
		"The panel was removed and has been tested. " + strings.Repeat("word ", 40) + "end.\n"
	if got := proseOf(t, clean); len(got) != 0 {
		t.Fatalf("clean prose, spans, fences, passive voice and a long sentence: faults %v, want none", got)
	}
}

// The gate names every fault at once: the budget, the markup and the prose.
func TestCheckNamesEveryFaultAtOnce(t *testing.T) {
	body := words(701) + "\nA dash — here.\n\n**a** and **b**\n"
	got := checkPR(t, body)
	for _, want := range []string{"budget 700 words, actual 708", "bold-texture", "em-dash"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal does not name %q:\n%s", want, got)
		}
	}
	if got := checkPR(t, "## Contracts\n\nC1: `pr open` refuses a body over budget. TestX proves it.\n"); got != "" {
		t.Fatalf("a clean body refused:\n%s", got)
	}
}

func proseOf(t *testing.T, body string) []Fault {
	t.Helper()
	fs, err := ProseFaults(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func checkPR(t *testing.T, body string) string {
	t.Helper()
	why, err := CheckPR(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	return why
}
