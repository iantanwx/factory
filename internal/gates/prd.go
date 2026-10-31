package gates

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// prdDir is where a project keeps its PRDs, <slug>.md. The epic id comes
// from `loop prd submit`, after the PRD is written, so the name holds none.
const prdDir = "docs/prd"

// sizing is every line of a PRD's scope check, in the order the template
// sets.
var sizing = []string{"Stories", "Estimated total", "Lands independently?", "Promises provable alone?", "Verdict"}

var proceed = regexp.MustCompile(`(?i)^proceed\b`)

// PRD checks that each PRD answered its own scope question: the sizing
// block holds every line, no line holds the template's placeholder, and
// the verdict says proceed. A verdict of "too large" stops the PRD, and
// the doc-reviewer decides the seam. It cannot check that the answer is
// right; the doc-reviewer reads the requirements for that.
func PRD(_ context.Context, r *Repo) ([]Finding, error) {
	files, err := r.glob([]string{prdDir + "/*.md"})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(r.Root, f))
		if err != nil {
			return nil, err
		}
		out = append(out, checkPRD(f, string(raw))...)
	}
	return out, nil
}

// checkPRD is the sizing check of one PRD.
func checkPRD(file, text string) []Finding {
	var out []Finding
	add := func(rule, msg string) { out = append(out, Finding{File: file, Rule: rule, Message: msg}) }
	for _, field := range sizing {
		v, ok := fieldValue(text, field)
		switch {
		case !ok:
			add("sizing", "**"+field+"** is missing; the scope check is what decides a split")
		case v == "" || strings.Contains(v, "<"):
			add("placeholder", "**"+field+"** still holds the template placeholder")
		case field == "Verdict" && !proceed.MatchString(v):
			add("verdict", "the verdict reads \""+v+"\", so this epic is not ready for a spec; the doc-reviewer decides the seam and a brainstorm room splits it")
		}
	}
	return out
}

// fieldValue is the text after `**field:**` at the start of a line. The
// colon is optional: `**Stories:**` holds one and `**Lands independently?**`
// does not.
func fieldValue(text, field string) (string, bool) {
	m := regexp.MustCompile(`(?m)^\*\*` + regexp.QuoteMeta(field) + `:?\*\*[ \t]*(.*)$`).FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}
