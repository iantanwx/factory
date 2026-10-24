package pack

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The hand-back rule (factorio-8qm, ADR 0010): a step the loop can run is
// the loop's step. The operator gives the coordinator approvals and
// decisions, never commands to run. So the coordinator's runbook, and each
// skill and agent it reads, never sends the operator to merge, to upgrade
// or to install, never names `gh pr merge`, and never tells the operator
// to run a loop verb that the coordinator may call itself.

// handBackStage is the runbook whose files the rule holds.
const handBackStage = "coordinator"

var (
	// handBackAct is a person as the one who merges, upgrades or installs:
	// "the operator's merge is the sign-off", "the operator upgrades the
	// host", "tell the operator to merge".
	handBackAct = regexp.MustCompile(`(?i)\b(?:operator|person)(?:'s|’s)?\s+(?:(?:to|must|should|can|will)\s+)?(?:merges?|upgrades?|installs?)\b`)
	// handBackRuns is a person as the one who runs something: "the
	// operator runs", "ask the operator to run".
	handBackRuns = regexp.MustCompile(`(?i)\b(?:operator|person)\s+(?:(?:to|must|should|can|will|then)\s+)?(?:run|runs)\b`)
	ghPRMerge    = regexp.MustCompile(`\bgh\s+pr\s+merge\b`)
	// sentenceEnd ends a sentence outside a code span.
	sentenceEnd = regexp.MustCompile(`[.!?](?:\s|$)`)
	inlineCode  = regexp.MustCompile("`[^`\n]*`")
)

// handBackFiles lists the sources the rule holds: the coordinator's
// runbook, and each file of each skill and agent the manifest says it
// reads.
func handBackFiles(m *Manifest) map[string]bool {
	rb, ok := m.Runbooks[handBackStage]
	if !ok {
		return nil
	}
	out := map[string]bool{"runbooks/" + handBackStage + ".md": true}
	for _, s := range rb.Skills {
		out["skills/"+s+"/"] = true
	}
	for _, a := range rb.Agents {
		out["agents/"+a+".md"] = true
	}
	return out
}

func heldToHandBack(files map[string]bool, source string) bool {
	if files[source] {
		return true
	}
	for dir := range files {
		if strings.HasSuffix(dir, "/") && strings.HasPrefix(source, dir) {
			return true
		}
	}
	return false
}

// handBack reports each hand-back in one file: one finding for each
// sentence, and one for each fenced line that merges.
func (ix *index) handBack(file, text string) []Finding {
	var out []Finding
	add := func(line int, format string, args ...any) {
		out = append(out, Finding{File: file, Line: line, Kind: "hand-back", Detail: fmt.Sprintf(format, args...) +
			"; a step the loop can run is the loop's step, and the operator gives only the approval or the decision"})
	}
	in := false
	var para []string
	start := 0
	flush := func() {
		if len(para) > 0 {
			ix.handBackParagraph(start, strings.Join(para, "\n"), add)
		}
		para = nil
	}
	for i, raw := range strings.Split(text, "\n") {
		n := i + 1
		if fence.MatchString(raw) {
			flush()
			in = !in
			continue
		}
		if in {
			if ghPRMerge.MatchString(stripComment(raw)) {
				add(n, "`gh pr merge`: no agent merges, and the server merges a document on the operator's recorded approval")
			}
			continue
		}
		if strings.TrimSpace(raw) == "" {
			flush()
			continue
		}
		if len(para) == 0 {
			start = n
		}
		para = append(para, raw)
	}
	flush()
	return out
}

// handBackParagraph checks each sentence of one paragraph. A code span
// stays whole, so a period inside it ends no sentence.
func (ix *index) handBackParagraph(start int, para string, add func(int, string, ...any)) {
	var spans []string
	masked := inlineCode.ReplaceAllStringFunc(para, func(s string) string {
		spans = append(spans, strings.Trim(s, "`"))
		return fmt.Sprintf("\x00%d\x00", len(spans)-1)
	})
	from := 0
	for from < len(masked) {
		to := len(masked)
		if loc := sentenceEnd.FindStringIndex(masked[from:]); loc != nil {
			to = from + loc[1]
		}
		sentence := masked[from:to]
		line := start + strings.Count(masked[:from], "\n") + leadingNewlines(sentence)
		if d := ix.handBackSentence(sentence, spans); d != "" {
			add(line, "%s", d)
		}
		from = to
	}
}

func leadingNewlines(s string) int {
	return strings.Count(s[:len(s)-len(strings.TrimLeft(s, " \t\n"))], "\n")
}

// handBackSentence names the hand-back in one masked sentence, or "".
func (ix *index) handBackSentence(sentence string, spans []string) string {
	var code []string
	for i, s := range spans {
		if strings.Contains(sentence, fmt.Sprintf("\x00%d\x00", i)) {
			code = append(code, s)
		}
	}
	for _, c := range code {
		if ghPRMerge.MatchString(c) {
			return fmt.Sprintf("`%s`: no agent merges, and the server merges a document on the operator's recorded approval", c)
		}
	}
	if handBackRuns.MatchString(sentence) {
		for _, c := range code {
			for _, toks := range ix.calls(span{text: c}) {
				paths, _, _ := ix.resolve(toks)
				for _, p := range paths {
					if v := ix.verbs[p]; v != nil && slices.Contains(v.Callers, handBackStage) {
						return fmt.Sprintf("`loop %s` is a verb the coordinator calls itself, and the sentence has a person run it", p)
					}
				}
			}
		}
	}
	if m := handBackAct.FindString(sentence); m != "" {
		return fmt.Sprintf("%q: the coordinator records the operator's approval or decision, and the loop merges, upgrades or installs", strings.Join(strings.Fields(m), " "))
	}
	return ""
}
