package shape

import (
	"fmt"
	"regexp"
	"strings"
)

// Need is one section that a shape requires.
type Need struct {
	Name    string         // the section's name, as a fault names it
	pattern *regexp.Regexp // the headings that match, or nil when Name matches ignoring case
}

func (n Need) matches(heading string) bool {
	if n.pattern != nil {
		return n.pattern.MatchString(heading)
	}
	return strings.EqualFold(heading, n.Name)
}

// Limit is one word limit of a shape.
type Limit struct {
	Section string // the Need whose sections it holds for, each one alone, or "" for the body total
	Words   int    // the most words in the section, or in each of its items when Items is set
	Items   int    // the most top-level list items in the section, or 0 for no item limit
}

// Shape is the rules of one kind of ticket body. Ticket and Bug are the only
// values.
type Shape struct {
	Name     string // the value of a ticket's shape fact
	Required []Need
	Limits   []Limit

	contracts Need           // the sections that hold the contracts, left out of the body total
	id        *regexp.Regexp // one contract id
	idHeading bool           // the ids are read from the contract headings, and not from the text under them
	fencesOut string         // the section whose fenced blocks the body total leaves out
}

var (
	ticketContracts = Need{Name: "Contracts to prove"}
	bugContract     = Need{Name: "Contract C-bug-<n>", pattern: regexp.MustCompile(`^(?i:contract) C-bug-[0-9]+$`)}
)

// Ticket is the body of a ticket under an epic, as the decomposer writes it.
var Ticket = Shape{
	Name: "ticket",
	Required: []Need{{Name: "Source"}, {Name: "Change"}, ticketContracts, {Name: "Files"},
		{Name: "Done means"}, {Name: "Out of scope"}},
	Limits: []Limit{{Words: 400}, {Section: "Change", Words: 150},
		{Section: ticketContracts.Name, Words: 80, Items: 8},
		{Section: "Done means", Words: 60}, {Section: "Out of scope", Words: 60}},
	contracts: ticketContracts,
	id:        regexp.MustCompile(`\bC[0-9]+\b`),
}

// Bug is the body of a shaped bug, as the reproducer writes it. Its
// Reproduction holds the test in a fenced block, and the fence holds no prose
// to cut, so the body total leaves it out.
var Bug = Shape{
	Name: "bug",
	Required: []Need{bugContract, {Name: "Source"}, {Name: "Reproduction"}, {Name: "What was seen"},
		{Name: "Priority"}, {Name: "Files"}, {Name: "Done means"}, {Name: "Out of scope"}},
	Limits: []Limit{{Words: 400}, {Section: bugContract.Name, Words: 80},
		{Section: "Done means", Words: 60}, {Section: "Out of scope", Words: 60}},
	contracts: bugContract,
	id:        regexp.MustCompile(`C-bug-[0-9]+`),
	idHeading: true,
	fencesOut: "Reproduction",
}

// ByName returns the shape whose Name is name. ok is false for any other name.
func ByName(name string) (s Shape, ok bool) {
	for _, s := range []Shape{Ticket, Bug} {
		if s.Name == name {
			return s, true
		}
	}
	return Shape{}, false
}

// FaultKind names why a body fails its shape.
type FaultKind string

const (
	Missing    FaultKind = "missing"
	Over       FaultKind = "over"
	NoContract FaultKind = "no_contract"
)

// Fault is one reason a body fails its shape.
type Fault struct {
	Kind    FaultKind
	Section string // "" for the body total
	Count   int    // words, or contracts, found
	Limit   int
	unit    string // what Count counts, as String names it
}

// String is the text a refusal's hint holds for the fault.
func (f Fault) String() string {
	switch f.Kind {
	case Missing:
		return "missing section: " + f.Section
	case NoContract:
		return f.Section + ": names no contract id, such as C1"
	}
	name := f.Section
	if name == "" {
		name = "body"
	}
	return fmt.Sprintf("%s: %d %s, limit %d", name, f.Count, f.unit, f.Limit)
}

// CheckBody returns every fault of body against s, or none when body holds
// its shape. A section is a "## " heading, and the sections may come in any
// order. Words are strings.Fields over the lines under a heading.
func CheckBody(s Shape, body string) []Fault {
	secs := sections(body)
	var out []Fault
	for _, n := range s.Required {
		if len(matching(secs, n)) == 0 {
			out = append(out, Fault{Kind: Missing, Section: n.Name})
		}
	}
	if !s.idHeading && len(matching(secs, s.contracts)) > 0 && len(ContractIDs(s, body)) == 0 {
		out = append(out, Fault{Kind: NoContract, Section: s.contracts.Name})
	}
	for _, l := range s.Limits {
		if l.Section == "" {
			if n := s.bodyWords(secs); n > l.Words {
				out = append(out, Fault{Kind: Over, Count: n, Limit: l.Words, unit: s.bodyUnit()})
			}
			continue
		}
		for _, sec := range matching(secs, s.need(l.Section)) {
			out = append(out, over(sec, l)...)
		}
	}
	return out
}

func over(sec section, l Limit) []Fault {
	if l.Items == 0 {
		if n := countWords(sec.Lines); n > l.Words {
			return []Fault{{Kind: Over, Section: sec.Heading, Count: n, Limit: l.Words, unit: "words"}}
		}
		return nil
	}
	var out []Fault
	items := entries(sec)
	if len(items) > l.Items {
		out = append(out, Fault{Kind: Over, Section: sec.Heading, Count: len(items), Limit: l.Items, unit: "contracts"})
	}
	for i, item := range items {
		// The list marker is no word.
		if n := countWords(item) - 1; n > l.Words {
			label := fmt.Sprintf("%s, item %d", sec.Heading, i+1)
			out = append(out, Fault{Kind: Over, Section: label, Count: n, Limit: l.Words, unit: "words"})
		}
	}
	return out
}

// bodyWords counts every line outside the contract sections, and outside the
// fenced blocks of fencesOut.
func (s Shape) bodyWords(secs []section) int {
	n := 0
	for _, sec := range secs {
		if s.contracts.matches(sec.Heading) {
			continue
		}
		fencesOut := s.fencesOut != "" && strings.EqualFold(sec.Heading, s.fencesOut)
		for _, l := range sec.Lines {
			if !fencesOut || !l.Fenced {
				n += len(strings.Fields(l.Text))
			}
		}
	}
	return n
}

func (s Shape) bodyUnit() string {
	unit := "words outside " + s.contracts.Name
	if s.fencesOut != "" {
		unit += " and the fenced blocks of " + s.fencesOut
	}
	return unit
}

func (s Shape) need(name string) Need {
	for _, n := range s.Required {
		if n.Name == name {
			return n
		}
	}
	return Need{Name: name}
}

func matching(secs []section, n Need) []section {
	var out []section
	for _, sec := range secs {
		if n.matches(sec.Heading) {
			out = append(out, sec)
		}
	}
	return out
}

// ContractIDs returns each contract id of body, in order, once each.
func ContractIDs(s Shape, body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, sec := range matching(sections(body), s.contracts) {
		texts := []string{sec.Heading}
		if !s.idHeading {
			texts = nil
			for _, l := range sec.Lines {
				if !l.Fenced {
					texts = append(texts, l.Text)
				}
			}
		}
		for _, t := range texts {
			for _, id := range s.id.FindAllString(t, -1) {
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
		}
	}
	return out
}

// Unnamed returns each id of ids that review does not hold as a whole token.
func Unnamed(ids []string, review string) []string {
	var out []string
	for _, id := range ids {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(id) + `\b`).MatchString(review) {
			out = append(out, id)
		}
	}
	return out
}
