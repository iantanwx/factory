package pack_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
	packsrc "github.com/iantanwx/loop/pack"
)

const glossarySkill = "skills/loop-sdlc-glossary/SKILL.md"

// phase is one entry of the glossary's phase list.
type phase struct {
	name     string
	optional bool
}

// wantPhases are the phases of the epic body, in order
// (docs/prd/loop-vocabulary.md § US-01). The design phase and its doc
// review sit between the ADR and Plan phases (spec factorio-a4yq, C25 to
// C28).
var wantPhases = []phase{
	{"Ideate", true}, {"Brainstorm", false}, {"Doc review", false}, {"ADR", true}, {"Design", true},
	{"Doc review", true}, {"Plan", false}, {"Doc review", false}, {"Decompose", false}, {"Build", false}, {"Retro", false},
}

// wantClosedBugRule is the operator's rule for a closed bug, one sentence
// for each terminal bug state (docs/plans/factorio-vlle-loop-vocabulary-spec.md
// § Decisions, 2026-10-31).
var wantClosedBugRule = map[string]string{
	"shaped":    "A shaped bug closes when its shaped ticket reaches `done`.",
	"canceled":  "A canceled bug closes at once.",
	"duplicate": "A duplicate bug never closes",
}

var (
	phaseLine    = regexp.MustCompile(`^(\d+)\. \*\*([^*]+)\*\*( \(optional\))?: `)
	termLine     = regexp.MustCompile(`^- \*\*([^*]+)\*\*: (.*)$`)
	sentenceEnd  = regexp.MustCompile(`\.(\s|$)`)
	closesAState = regexp.MustCompile("(?i)\\bclos\\w*\\b.*`(shaped|duplicate|canceled)`")
	glossaryCode = regexp.MustCompile("`([^`]+)`")
	// qaAfterReview finds the QA run after the code-reviewer in the build
	// phase.
	qaAfterReview = regexp.MustCompile(`code-reviewer.*\bQA\b`)
	// simplifyBeforeReview finds the simplify run between the implementer
	// and the code-reviewer in the build phase.
	simplifyBeforeReview = regexp.MustCompile(`implementer.*\bsimplify run\b.*code-reviewer`)
)

// glossarySections splits text at each "## " heading, and returns each
// section's lines by its title.
func glossarySections(text string) map[string][]string {
	out := map[string][]string{}
	title := ""
	for _, l := range strings.Split(text, "\n") {
		if t, ok := strings.CutPrefix(l, "## "); ok {
			title = t
			continue
		}
		out[title] = append(out[title], l)
	}
	return out
}

// glossaryTerms returns each bold term that a section defines, with its
// definition on one line.
func glossaryTerms(lines []string) map[string]string {
	out := map[string]string{}
	term := ""
	for _, l := range lines {
		if m := termLine.FindStringSubmatch(l); m != nil {
			term = m[1]
			out[term] = m[2]
			continue
		}
		if term != "" && strings.HasPrefix(l, "  ") {
			out[term] += " " + strings.TrimSpace(l)
			continue
		}
		term = ""
	}
	return out
}

// sentences splits a definition at each full stop.
func sentences(def string) []string {
	var out []string
	for _, s := range sentenceEnd.Split(def, -1) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s+".")
		}
	}
	return out
}

// ticketStates returns each state that the ticket line of the loop states
// lists.
func ticketStates(lines []string) []string {
	var out []string
	for _, m := range glossaryCode.FindAllStringSubmatch(glossaryTerms(lines)["Ticket"], -1) {
		out = append(out, m[1])
	}
	return out
}

// buildPhase returns the text of the build phase, on one line.
func buildPhase(lines []string) string {
	build := ""
	for _, l := range lines {
		if m := phaseLine.FindStringSubmatch(l); m != nil {
			if build != "" {
				break
			}
			if m[2] == "Build" {
				build = l
			}
			continue
		}
		if build != "" && strings.HasPrefix(l, "   ") {
			build += " " + strings.TrimSpace(l)
		}
	}
	return build
}

// glossaryFaults names each way text breaks C21, C35 and C15 of
// factorio-1r94.
func glossaryFaults(text string) []string {
	var faults []string
	sections := glossarySections(text)

	var phases []phase
	for _, l := range sections["Phases"] {
		if m := phaseLine.FindStringSubmatch(l); m != nil {
			if m[1] != fmt.Sprint(len(phases)+1) {
				faults = append(faults, fmt.Sprintf("phase %q is number %s, want %d", m[2], m[1], len(phases)+1))
			}
			phases = append(phases, phase{m[2], m[3] != ""})
		}
	}
	if !slices.Equal(phases, wantPhases) {
		faults = append(faults, fmt.Sprintf("the phases are %v, want %v", phases, wantPhases))
	}

	work := glossaryTerms(sections["Work"])
	if !strings.Contains(work["Unit of work"], "an epic or a bug") {
		faults = append(faults, fmt.Sprintf("a unit of work is %q, want an epic or a bug", work["Unit of work"]))
	}
	if t := work["Ticket"]; !strings.Contains(t, "slice of a unit of work") || !strings.Contains(t, "never a unit by itself") {
		faults = append(faults, fmt.Sprintf("a ticket is %q, want one slice of a unit of work and never a unit by itself", t))
	}
	var kinds []string
	if s := sentences(work["Work kinds"]); len(s) > 0 {
		for _, m := range glossaryCode.FindAllStringSubmatch(s[0], -1) {
			kinds = append(kinds, m[1])
		}
	}
	if slices.Sort(kinds); !slices.Equal(kinds, []string{"bug", "epic", "ticket"}) {
		faults = append(faults, fmt.Sprintf("the work kinds are %v, want bug, epic and ticket only", kinds))
	}

	inbox := glossaryTerms(sections["Inbox"])
	for _, term := range []string{"Decision", "Inbox hold", "Hold on an item", "Escalation", "Triage rubric"} {
		if inbox[term] == "" {
			faults = append(faults, fmt.Sprintf("the inbox section defines no %q", term))
		}
	}
	if !strings.Contains(inbox["Inbox hold"], "`needs_human`") {
		faults = append(faults, "an inbox hold does not name a decision in `needs_human`")
	}

	rows := map[string]int{}
	triage, qa, simplify := "", "", ""
	for _, r := range pack.Table(text, "stage", "what it does", "item states it takes") {
		rows[pack.Bare(r.Cells[0])]++
		switch pack.Bare(r.Cells[0]) {
		case "triage":
			triage = r.Cells[1]
		case "qa":
			qa = r.Cells[2]
		case "simplify":
			simplify = r.Cells[2]
		}
	}
	for _, role := range []string{"reviser", "planner", "triage", "unattended-brainstorm", "retro"} {
		if rows[role] != 1 {
			faults = append(faults, fmt.Sprintf("the stage table defines %s %d times, want once", role, rows[role]))
		}
	}
	for _, stage := range []string{"qa", "simplify"} {
		if rows[stage] != 1 {
			faults = append(faults, fmt.Sprintf("the stage table defines %s %d times, want once", stage, rows[stage]))
		}
	}
	if !strings.Contains(qa, "`in_qa`") {
		faults = append(faults, fmt.Sprintf("the qa stage takes %q, want ticket `in_qa`", qa))
	}
	if !strings.Contains(simplify, "`in_simplify`") {
		faults = append(faults, fmt.Sprintf("the simplify stage takes %q, want ticket `in_simplify`", simplify))
	}
	states := ticketStates(sections["Loop states"])
	for _, state := range []string{"in_qa", "in_simplify"} {
		if !slices.Contains(states, state) {
			faults = append(faults, fmt.Sprintf("the ticket states do not list `%s`", state))
		}
	}
	build := buildPhase(sections["Phases"])
	if !qaAfterReview.MatchString(build) {
		faults = append(faults, fmt.Sprintf("the build phase is %q, want the QA run after the code-reviewer", build))
	}
	if !simplifyBeforeReview.MatchString(build) {
		faults = append(faults, fmt.Sprintf("the build phase is %q, want the simplify run between the implementer and the code-reviewer", build))
	}
	if !strings.Contains(triage, "triage rubric") {
		faults = append(faults, fmt.Sprintf("the triage stage is %q, want the run that applies the triage rubric", triage))
	}
	if room := glossaryTerms(sections["People and agents"])["A room"]; !strings.Contains(room, "Its kinds are brainstorm,") {
		faults = append(faults, fmt.Sprintf("a room is %q, want brainstorm among its kinds", room))
	}

	retro := glossaryTerms(sections["Retro"])
	for _, term := range []string{"Retro", "Epic done", "Bug closed", "Closed bug"} {
		if retro[term] == "" {
			faults = append(faults, fmt.Sprintf("the retro section defines no %q", term))
		}
	}
	for term, event := range map[string]string{"Epic done": "`epic.done`", "Bug closed": "`bug.closed`"} {
		if !strings.HasPrefix(retro[term], event) {
			faults = append(faults, fmt.Sprintf("%s is %q, want it to name %s", term, retro[term], event))
		}
	}
	rule := sentences(retro["Closed bug"])
	for state, want := range wantClosedBugRule {
		var got []string
		for _, s := range rule {
			if strings.HasPrefix(s, "A "+state+" bug ") {
				got = append(got, s)
			}
		}
		if len(got) != 1 || !strings.HasPrefix(got[0], want) {
			faults = append(faults, fmt.Sprintf("the rule for a %s bug is %q, want %q", state, got, want))
		}
	}
	for _, s := range rule {
		if closesAState.MatchString(s) {
			faults = append(faults, fmt.Sprintf("the rule closes a bug by its own state: %q", s))
		}
	}
	return faults
}

// C21: the glossary holds the phases, the units of work, the holds,
// the triage words, the retro, its two events and the rule for a closed
// bug. C35: it names the qa stage, its state `in_qa` and the QA run of the
// build phase. C15 of factorio-1r94: it names the simplify stage, its state
// `in_simplify` and the simplify run of the build phase.
func TestC21GlossaryHoldsTheNewWords(t *testing.T) {
	if faults := glossaryFaults(read(t, glossarySkill)); len(faults) > 0 {
		t.Fatalf("the glossary breaks C21:\n%s", strings.Join(faults, "\n"))
	}
}

// C21: a glossary that counts a bug as closed by its terminal state, or
// lists a decision among the work kinds, or makes ideate a required phase,
// fails.
func TestC21GlossaryFaultsCatchAWrongGlossary(t *testing.T) {
	good := read(t, glossarySkill)
	start := strings.Index(good, "- **Closed bug**: ")
	if start < 0 {
		t.Fatal("the glossary holds no closed bug entry")
	}
	end := strings.Index(good[start:], "\n\n")
	if end < 0 {
		t.Fatal("the closed bug entry ends the glossary; want a blank line after it")
	}
	cases := map[string]string{
		"three terminal states": good[:start] + "- **Closed bug**: a bug closes when it reaches `shaped`, `duplicate` or `canceled`." + good[start+end:],
		"a closed duplicate": good[:start] + "- **Closed bug**: A shaped bug closes when its shaped ticket reaches `done`. " +
			"A canceled bug closes at once. A duplicate bug closes at once." + good[start+end:],
		"a decision as work":       strings.Replace(good, "- **Work kinds**: `epic`,", "- **Work kinds**: `decision`, `epic`,", 1),
		"ideate not optional":      strings.Replace(good, "1. **Ideate** (optional): ", "1. **Ideate**: ", 1),
		"no qa row":                noRow(good, "| `qa` |"),
		"no in_qa state":           strings.Replace(good, "`repair`, `in_qa`,", "`repair`,", 1),
		"no QA run in build":       strings.Replace(good, " In a project\n   that lists the `qa` stage, the QA run then drives the app of each reviewed ticket.", "", 1),
		"no simplify row":          noRow(good, "| `simplify` |"),
		"no in_simplify state":     strings.Replace(good, "`in_progress`, `in_simplify`,", "`in_progress`,", 1),
		"no simplify run in build": strings.Replace(good, "the simplify run makes its code simpler", "a person makes its code simpler", 1),
	}
	for name, text := range cases {
		if text == good {
			t.Fatalf("%s: the fixture changes nothing", name)
		}
		if len(glossaryFaults(text)) == 0 {
			t.Errorf("%s: the fixture glossary passes C21", name)
		}
	}
}

// noRow returns text with each line that starts with prefix removed.
func noRow(text, prefix string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// C9: AAR is a retired word, and no file of the pack holds it.
func TestC9PackHoldsNoAAR(t *testing.T) {
	if !slices.Contains(config.RetiredWords(), "AAR") {
		t.Errorf("RetiredWords() = %v, want AAR", config.RetiredWords())
	}
	if hits := retiredHits(t, packsrc.Files, []string{"AAR"}); len(hits) > 0 {
		t.Errorf("the pack says AAR:\n%s", strings.Join(hits, "\n"))
	}
}
