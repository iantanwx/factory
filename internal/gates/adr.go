package gates

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// adrDir is where a project keeps its decision records, NNNN-<slug>.md.
// The server reads the same place to join an ADR to its architecture epic.
const adrDir = "docs/adr"

// adrSections are the headings a reader looks for in every ADR, in the
// order the template sets.
var adrSections = []string{"Context", "Decision", "Consequences", "Acceptance criteria"}

var (
	adrName     = regexp.MustCompile(`^(\d{4})-`)
	adrHeading  = regexp.MustCompile(`(?m)^#\s+ADR\s+(\d{4}):`)
	adrStatus   = regexp.MustCompile(`(?m)^\*\*Status:\*\*\s*(?:Proposed|Accepted|Superseded by ADR \d{4})\s*$`)
	adrDate     = regexp.MustCompile(`(?m)^\*\*Date:\*\*\s*\d{4}-\d{2}-\d{2}\s*$`)
	adrTicket   = regexp.MustCompile(`(?m)^\*\*Ticket:\*\*\s*(\S+)`)
	adrNone     = regexp.MustCompile(`(?i)^none\b`)
	adrTemplate = regexp.MustCompile(`(?m)^>\s*Copy this file to`)
	adrCite     = regexp.MustCompile(`docs/adr/(\d{4})`)
	adrListItem = regexp.MustCompile(`(?m)^\s*[-*]\s+\S`)
	// rejection is a phrase that says an alternative was weighed and dropped.
	rejection = regexp.MustCompile(`(?i)\b(?:reject|rejected|instead of|rather than|we did not adopt)\b`)
	// itemID is a work-item id as beads writes it: the project prefix, a
	// dash, a base-36 hash, and a .n for each level of child.
	itemID = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}-[a-z0-9]+(?:\.[0-9]+)*$`)
)

// ADR checks what is true of a decision record the moment it is written.
// It does not compare an ADR's claims with the code: an ADR describes what
// will exist, so that check would fail every new one.
//
// Each ADR holds its number in the filename and the heading, a Status,
// Date and Ticket header, the four sections, an alternative that Decision
// rejected and a list item under Acceptance criteria, and no template
// instructions. The Ticket line names the architecture epic, or says none.
// No two ADRs share a number, and each docs/adr/NNNN it cites exists.
func ADR(_ context.Context, r *Repo) ([]Finding, error) {
	files, err := r.glob([]string{adrDir + "/*.md"})
	if err != nil {
		return nil, err
	}
	var out []Finding
	texts := make([]string, len(files))
	owner := map[string]string{}
	for i, f := range files {
		raw, err := os.ReadFile(filepath.Join(r.Root, f))
		if err != nil {
			return nil, err
		}
		texts[i] = string(raw)
		out = append(out, checkADR(f, texts[i])...)
		n := adrNumber(f)
		if n == "" {
			continue
		}
		if first, ok := owner[n]; ok {
			out = append(out, Finding{File: f, Rule: "duplicate", Message: "number " + n + " is already " + path.Base(first) + "; never reuse a number"})
			continue
		}
		owner[n] = f
	}
	// A citation resolves only once every number is known.
	for i, f := range files {
		seen := map[string]bool{}
		for _, m := range adrCite.FindAllStringSubmatch(texts[i], -1) {
			n := m[1]
			if seen[n] || n == adrNumber(f) {
				continue
			}
			seen[n] = true
			if _, ok := owner[n]; !ok {
				out = append(out, Finding{File: f, Rule: "citation", Message: "cites docs/adr/" + n + ", and no ADR has that number"})
			}
		}
	}
	return out, nil
}

// adrNumber is the NNNN an ADR's filename starts with, or "".
func adrNumber(file string) string {
	if m := adrName.FindStringSubmatch(path.Base(file)); m != nil {
		return m[1]
	}
	return ""
}

// checkADR is every check that reads one ADR alone.
func checkADR(file, text string) []Finding {
	var out []Finding
	add := func(rule, msg string) { out = append(out, Finding{File: file, Rule: rule, Message: msg}) }

	name := adrNumber(file)
	if name == "" {
		add("name", "the filename must start with a four-digit number, as NNNN-<slug>.md")
	}
	if m := adrHeading.FindStringSubmatch(text); m == nil {
		add("heading", "the first heading must read `# ADR NNNN: <the decision, in one line>`")
	} else if name != "" && name != m[1] {
		add("number", "the filename says "+name+" and the heading says "+m[1])
	}

	if !adrStatus.MatchString(text) {
		add("status", "**Status:** must be Proposed, Accepted, or Superseded by ADR NNNN")
	}
	if !adrDate.MatchString(text) {
		add("date", "**Date:** must be one ISO date, as YYYY-MM-DD")
	}
	// An absent line and `none` look alike to a reader, and only one of
	// them is a decision.
	if m := adrTicket.FindStringSubmatch(text); m == nil {
		add("ticket", "**Ticket:** is required; the server joins an ADR to its architecture epic through it")
	} else if id := strings.TrimRight(m[1], ".,;"); !itemID.MatchString(id) && !adrNone.MatchString(m[1]) {
		add("ticket", "**Ticket:** \""+m[1]+"\" is neither a work-item id nor \"none\"")
	}

	if adrTemplate.MatchString(text) {
		add("template", "the template's instruction block is still here; delete it before you commit")
	}
	for _, h := range adrSections {
		if _, ok := sectionBody(text, h); !ok {
			add("section", "## "+h+" is missing")
		}
	}
	if body, ok := sectionBody(text, "Decision"); ok && !rejection.MatchString(body) {
		add("rejection", "## Decision names nothing it rejected; a choice with no alternative is an assumption")
	}
	if body, ok := sectionBody(text, "Acceptance criteria"); ok && !adrListItem.MatchString(body) {
		add("criteria", "## Acceptance criteria holds no item; each line must be able to fail")
	}
	return out
}

// sectionBody is the text under `## heading` up to the next `## `. ok is
// false when the heading is absent, which an empty section is not.
func sectionBody(text, heading string) (body string, ok bool) {
	start := regexp.MustCompile(`(?m)^##\s+` + regexp.QuoteMeta(heading) + `[ \t]*$`).FindStringIndex(text)
	if start == nil {
		return "", false
	}
	after := text[start[1]:]
	if next := nextSection.FindStringIndex(after); next != nil {
		return after[:next[0]], true
	}
	return after, true
}

var nextSection = regexp.MustCompile(`(?m)^##\s`)
