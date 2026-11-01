package shape

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// SpecSections are the template's "## " headings, in order.
var SpecSections = []string{"Source", "Scope check", "Decisions", "Assumptions", "Approach",
	"Domain model", "Interfaces", "Data", "Flows", "Work", "Out of scope", "Contracts",
	"Observable behaviour", "Open questions"}

// TemplateLines are the trimmed lines of the spec template that no spec may
// keep: the instruction block's first line, each line outside a fence that
// holds a <...> placeholder, and the example markers. A test holds this list
// equal to pack/skills/loop-sdlc-documents/spec.md.
var TemplateLines = []string{
	"> Copy this file to `docs/specs/<epic id>-<slug>-spec.md`. You write it from the epic's source: a",
	"# <Title of the epic>: technical spec",
	"- **Epic:** <epic id>",
	"- **Source:** `docs/prd/<slug>.md` for a feature epic. For an architecture epic, list",
	"each `docs/adr/NNNN-<slug>.md` whose `**Ticket:**` line names this epic.",
	"- **Stories covered:** <US-nn, US-nn>, or none for an architecture epic, which no story asks for.",
	"**Components:** <one, or two. A second one carries a line that says why it cannot be its own epic>",
	"**Not in this spec:** <the neighbours, and which spec owns each>",
	"- **<Decision>.** We chose <X> over <Y>, because <reason>.",
	"- <Assumption>. This is wrong if <what would falsify it>.",
	"<the schema change, or None>",
	"1. **<Title>.** <what changes>",
	"- Files: <paths or globs>",
	"- Needs: <an earlier entry, or nothing>",
	"- Proves: <the contracts, by number>",
	"- Done when: <an outcome that someone can observe>",
	"- <Thing>. It belongs to <an epic, a story, or never>.",
	"**C1. <Kind>: <short name>.** Proves <A-n>. <The promise, stated so that it is true or false.>",
	"*<How a test proves it.>*",
	"- <Question> This blocks <which work entry>.",
	"**Good**",
	"> **Good**",
	"> **Bad, and why**",
	"> **Bad.** ~~\"Performance will be acceptable.\"~~ Nothing could falsify it, so nobody can check it.",
	"> **Bad.** No diagram, because \"the flow is obvious\". When it is obvious, the diagram takes two",
	"> **Bad.** ~~\"UI\"~~ names no screen and no owner.",
	"> **Bad.** ~~\"Is this the right approach?\"~~ Nobody can answer this. Ask about one choice.",
}

// SpecFinding is one fault of a spec file. Rule is section, empty, template or name.
type SpecFinding struct {
	Line    int // 0 when the fault has no line, such as a missing heading
	Rule    string
	Message string
}

var epicLine = regexp.MustCompile(`^(?:[-*+]\s+)?\*\*Epic:\*\*\s*(\S*)`)

// CheckSpec returns every fault of the spec at file, in line order. A
// missing heading has no line, and comes first.
func CheckSpec(file, text string) []SpecFinding {
	var out []SpecFinding
	add := func(n int, rule, format string, a ...any) {
		out = append(out, SpecFinding{Line: n, Rule: rule, Message: fmt.Sprintf(format, a...)})
	}
	all := sections(text)
	seen := map[int]bool{}
	last := -1
	for _, s := range all[1:] {
		i := slices.IndexFunc(SpecSections, func(h string) bool { return strings.EqualFold(h, s.Heading) })
		switch {
		case i < 0:
			continue
		case seen[i]:
			add(s.Line, "section", "## %s is repeated; the template holds each heading once", SpecSections[i])
		case i < last:
			add(s.Line, "section", "## %s comes after ## %s; the template orders them the other way", SpecSections[i], SpecSections[last])
		}
		seen[i] = true
		last = max(last, i)
		if !slices.ContainsFunc(s.Lines, func(l line) bool { return strings.TrimSpace(l.Text) != "" }) {
			add(s.Line, "empty", "## %s holds nothing; write the section, or the word None and why", SpecSections[i])
		}
	}
	for i, h := range SpecSections {
		if !seen[i] {
			add(0, "section", "## %s is missing; the template holds 14 headings, in order", h)
		}
	}
	epic, at := "", 0
	for _, s := range all {
		for _, l := range s.Lines {
			t := strings.TrimSpace(l.Text)
			if l.Fenced {
				continue
			}
			if slices.Contains(TemplateLines, t) {
				add(l.N, "template", "this line is the template's; replace it with the spec's own text, or delete it")
			}
			if m := epicLine.FindStringSubmatch(t); m != nil && at == 0 {
				epic, at = m[1], l.N
			}
		}
	}
	switch name := path.Base(file); {
	case at == 0:
		add(0, "name", "no **Epic:** line; the filename must start with the epic id and a dash")
	case !strings.HasPrefix(name, epic+"-"):
		add(at, "name", "%s does not start with %q; name the file docs/specs/<epic id>-<slug>-spec.md", name, epic+"-")
	}
	slices.SortStableFunc(out, func(a, b SpecFinding) int { return a.Line - b.Line })
	return out
}
