package shape_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/shape"
)

// words is n words of prose.
func words(n int) string { return strings.TrimSpace(strings.Repeat("w ", n)) }

// items is n contract items of size words each, the list marker left out.
func items(n, size int) string {
	var lines []string
	for i := 1; i <= n; i++ {
		lines = append(lines, fmt.Sprintf("- C%d. %s", i, words(size-1)))
	}
	return strings.Join(lines, "\n")
}

// fence is a fenced block of n words between its two marker lines.
func fence(n int) string { return "```go\n" + words(n) + "\n```" }

type part struct{ heading, text string }

func body(parts []part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.heading != "" {
			b.WriteString("## " + p.heading + "\n\n")
		}
		b.WriteString(p.text + "\n\n")
	}
	return b.String()
}

// set replaces the text under heading, and adds the section when it is absent.
func set(parts []part, heading, text string) []part {
	out := append([]part(nil), parts...)
	for i := range out {
		if out[i].heading == heading {
			out[i].text = text
			return out
		}
	}
	return append(out, part{heading, text})
}

func without(parts []part, headings ...string) []part {
	var out []part
	for _, p := range parts {
		drop := false
		for _, h := range headings {
			drop = drop || p.heading == h
		}
		if !drop {
			out = append(out, p)
		}
	}
	return out
}

func faults(s shape.Shape, b string) []string {
	var out []string
	for _, f := range shape.CheckBody(s, b) {
		out = append(out, f.String())
	}
	return out
}

// ticketParts is a ticket body of 120 words outside Contracts to prove.
func ticketParts() []part {
	return []part{
		{"Source", words(20)},
		{"Change", words(50)},
		{"Contracts to prove", items(2, 10)},
		{"Files", words(10)},
		{"Done means", words(20)},
		{"Out of scope", words(20)},
	}
}

// C6: a ticket body is refused for each missing section, all at once.
func TestCheckBodyRefusesATicketForEachMissingSection(t *testing.T) {
	base := ticketParts()
	cases := []struct {
		name  string
		parts []part
		want  []string
	}{
		{"every section present", base, nil},
		{"no Source", without(base, "Source"), []string{"missing section: Source"}},
		{"no Change", without(base, "Change"), []string{"missing section: Change"}},
		{"no Contracts to prove", without(base, "Contracts to prove"), []string{"missing section: Contracts to prove"}},
		{"no Files", without(base, "Files"), []string{"missing section: Files"}},
		{"no Done means", without(base, "Done means"), []string{"missing section: Done means"}},
		{"no Out of scope", without(base, "Out of scope"), []string{"missing section: Out of scope"}},
		{
			"two sections missing",
			without(base, "Change", "Files"),
			[]string{"missing section: Change", "missing section: Files"},
		},
		{
			"Contracts to prove names no id",
			set(base, "Contracts to prove", "- Each refusal names the rule."),
			[]string{"Contracts to prove: names no contract id, such as C1"},
		},
		{"C10 is an id", set(base, "Contracts to prove", "- C10. The rule."), nil},
		{"C1a is no id", set(base, "Contracts to prove", "- C1a. The rule."), []string{"Contracts to prove: names no contract id, such as C1"}},
		{
			"a heading matches by its trimmed text, ignoring case",
			append(without(base, "Change"), part{"  change  ", words(10)}),
			nil,
		},
		{
			"a level 3 heading is no section",
			set(without(base, "Change"), "Source", "### Change\n\n"+words(10)),
			[]string{"missing section: Change"},
		},
		{
			"a heading inside a fenced block is no section",
			set(without(base, "Change"), "Source", "```\n## Change\n```"),
			[]string{"missing section: Change"},
		},
		{"the order of sections does not matter", append(base[3:], base[:3]...), nil},
		{"another heading is allowed", set(base, "Stop conditions", words(10)), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := faults(shape.Ticket, body(c.parts)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("CheckBody(Ticket) = %q, want %q", got, c.want)
			}
		})
	}
}

// C7: the ticket budget passes at each limit and fails at one more.
func TestCheckBodyHoldsTheTicketBudget(t *testing.T) {
	base := ticketParts()
	const overBody = "body: 401 words outside Contracts to prove, limit 400"
	cases := []struct {
		name  string
		parts []part
		want  []string
	}{
		{"body at 400", set(base, "Source", words(300)), nil},
		{"body at 401", set(base, "Source", words(301)), []string{overBody}},
		{
			"the text before the first heading counts",
			append([]part{{"", words(1)}}, set(base, "Source", words(300))...),
			[]string{overBody},
		},
		{
			"Contracts to prove never counts toward the body",
			set(set(base, "Source", words(300)), "Contracts to prove", items(8, 80)),
			nil,
		},
		{"Change at 150", set(base, "Change", words(150)), nil},
		{"Change at 151", set(base, "Change", words(151)), []string{"Change: 151 words, limit 150"}},
		{"Done means at 60", set(base, "Done means", words(60)), nil},
		{"Done means at 61", set(base, "Done means", words(61)), []string{"Done means: 61 words, limit 60"}},
		{"Out of scope at 60", set(base, "Out of scope", words(60)), nil},
		{"Out of scope at 61", set(base, "Out of scope", words(61)), []string{"Out of scope: 61 words, limit 60"}},
		{"8 contract items", set(base, "Contracts to prove", items(8, 10)), nil},
		{"9 contract items", set(base, "Contracts to prove", items(9, 10)), []string{"Contracts to prove: 9 contracts, limit 8"}},
		{"a contract item at 80", set(base, "Contracts to prove", items(1, 80)), nil},
		{
			"a contract item at 81",
			set(base, "Contracts to prove", items(1, 10)+"\n"+items(1, 81)),
			[]string{"Contracts to prove, item 2: 81 words, limit 80"},
		},
		{
			"a nested line belongs to its contract item",
			set(base, "Contracts to prove", items(1, 80)+"\n  - "+words(1)),
			[]string{"Contracts to prove, item 1: 82 words, limit 80"},
		},
		{
			"every fault at once",
			set(set(base, "Change", words(151)), "Done means", words(61)),
			[]string{"Change: 151 words, limit 150", "Done means: 61 words, limit 60"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := faults(shape.Ticket, body(c.parts)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("CheckBody(Ticket) = %q, want %q", got, c.want)
			}
		})
	}
}

// bugParts is a bug body of 105 words outside its Contract section and the
// fenced block in Reproduction.
func bugParts() []part {
	return []part{
		{"Contract C-bug-1", words(20)},
		{"Source", words(20)},
		{"Reproduction", words(10) + "\n\n" + fence(30)},
		{"What was seen", words(20)},
		{"Priority", words(5)},
		{"Files", words(10)},
		{"Done means", words(20)},
		{"Out of scope", words(20)},
	}
}

// C8: a bug body is refused for each missing section.
func TestCheckBodyRefusesABugForEachMissingSection(t *testing.T) {
	base := bugParts()
	cases := []struct {
		name  string
		parts []part
		want  []string
	}{
		{"every section present, and no Change or Contracts to prove", base, nil},
		{"no Contract section", without(base, "Contract C-bug-1"), []string{"missing section: Contract C-bug-<n>"}},
		{
			"a ticket's contract heading is no bug contract",
			append(without(base, "Contract C-bug-1"), part{"Contract C1", words(5)}),
			[]string{"missing section: Contract C-bug-<n>"},
		},
		{"Contract C-bug-12 is a contract", append(without(base, "Contract C-bug-1"), part{"Contract C-bug-12", words(5)}), nil},
		{"no Source", without(base, "Source"), []string{"missing section: Source"}},
		{"no Reproduction", without(base, "Reproduction"), []string{"missing section: Reproduction"}},
		{"no What was seen", without(base, "What was seen"), []string{"missing section: What was seen"}},
		{"no Priority", without(base, "Priority"), []string{"missing section: Priority"}},
		{"no Files", without(base, "Files"), []string{"missing section: Files"}},
		{"no Done means", without(base, "Done means"), []string{"missing section: Done means"}},
		{"no Out of scope", without(base, "Out of scope"), []string{"missing section: Out of scope"}},
		{
			"two sections missing",
			without(base, "Contract C-bug-1", "Priority"),
			[]string{"missing section: Contract C-bug-<n>", "missing section: Priority"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := faults(shape.Bug, body(c.parts)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("CheckBody(Bug) = %q, want %q", got, c.want)
			}
		})
	}
}

// C9: the bug budget leaves the test fence out.
func TestCheckBodyHoldsTheBugBudgetWithoutTheTestFence(t *testing.T) {
	// Source at 315 words brings the body to 400.
	base := set(bugParts(), "Source", words(315))
	overBody := func(n int) []string {
		return []string{fmt.Sprintf("body: %d words outside Contract C-bug-<n> and the fenced blocks of Reproduction, limit 400", n)}
	}
	cases := []struct {
		name  string
		parts []part
		want  []string
	}{
		{"body at 400", base, nil},
		{"body at 401", set(base, "Source", words(316)), overBody(401)},
		{"a Reproduction fence of 500 words", set(base, "Reproduction", words(10)+"\n\n"+fence(500)), nil},
		{"the words around the fence count", set(base, "Reproduction", words(11)+"\n\n"+fence(30)), overBody(401)},
		{
			// 313 words and a fenced block of 3 words with its markers.
			"a fenced block in another section counts",
			set(base, "Source", words(313)+"\n\n```\nw\n```"),
			overBody(401),
		},
		{"the Contract section is outside the body", set(base, "Contract C-bug-1", words(80)), nil},
		{"a Contract section at 81", set(base, "Contract C-bug-1", words(81)), []string{"Contract C-bug-1: 81 words, limit 80"}},
		{"each Contract section has its limit", set(base, "Contract C-bug-2", words(81)), []string{"Contract C-bug-2: 81 words, limit 80"}},
		{"Done means at 61", set(bugParts(), "Done means", words(61)), []string{"Done means: 61 words, limit 60"}},
		{"Out of scope at 61", set(bugParts(), "Out of scope", words(61)), []string{"Out of scope: 61 words, limit 60"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := faults(shape.Bug, body(c.parts)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("CheckBody(Bug) = %q, want %q", got, c.want)
			}
		})
	}
}
