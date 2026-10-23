package gates

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/pack"
)

// proseOf returns a document with each fenced block and each inline code
// span blanked, and each line kept in place. A wrong example belongs in
// code, so the claim checks below read prose only.
func proseOf(text string) string {
	lines := strings.Split(text, "\n")
	in := false
	for i, l := range lines {
		if fenceOpen.MatchString(l) {
			in = !in
			lines[i] = ""
			continue
		}
		if in {
			lines[i] = ""
			continue
		}
		lines[i] = codeSpans.ReplaceAllStringFunc(l, func(m string) string { return strings.Repeat(" ", len(m)) })
	}
	return strings.Join(lines, "\n")
}

// lineAt is the line of byte offset i in text.
func lineAt(text string, i int) int { return strings.Count(text[:i], "\n") + 1 }

// countNoun is a thing that a document may count, and the files that are
// the whole set of it.
type countNoun struct {
	noun, words, glob string
}

// countNouns are the sets a document counts. A set with no file in the
// repository is not checked: the word then names something else, such as
// the loop's own runbooks.
var countNouns = []countNoun{
	{noun: "runbooks", words: `runbooks`, glob: "docs/agents/*.md"},
	{noun: "policies", words: `policies|policy files`, glob: ".loop/policy/*.md"},
}

var numberWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	"eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17,
	"eighteen": 18, "nineteen": 19, "twenty": 20,
}

// totalBefore makes a count a claim about the whole set. It is short on
// purpose: "three runbooks review work" counts a subset, and is true.
var totalBefore = regexp.MustCompile(`(?i)\b(?:all|all of the|every one of the|each of the|there are|there were)\s+(?:the\s+)?$`)

func countPattern(words string) *regexp.Regexp {
	nums := make([]string, 0, len(numberWords))
	for w := range numberWords {
		nums = append(nums, w)
	}
	slices.Sort(nums)
	return regexp.MustCompile(`(?i)\b(\d{1,3}|` + strings.Join(nums, "|") + `)\s+(?:` + words + `)\b`)
}

// countFindings reports a stated count that the repository contradicts. A
// total that differs is wrong, and so is a subset larger than the whole.
// The words may wrap across a line break.
func countFindings(file, text string, counts map[string]int) []Finding {
	prose := proseOf(text)
	var out []Finding
	for _, n := range countNouns {
		actual := counts[n.noun]
		if actual == 0 {
			continue
		}
		after := regexp.MustCompile(`(?i)^\s+in\s+` + regexp.QuoteMeta(path.Dir(n.glob)) + `\b`)
		for _, m := range countPattern(n.words).FindAllStringSubmatchIndex(prose, -1) {
			word := strings.ToLower(prose[m[2]:m[3]])
			stated, ok := numberWords[word]
			if !ok {
				stated, _ = strconv.Atoi(word)
			}
			total := totalBefore.MatchString(prose[:m[0]]) || after.MatchString(prose[m[1]:])
			switch {
			case total && stated != actual:
				out = append(out, Finding{File: file, Line: lineAt(prose, m[0]), Rule: "count",
					Message: fmt.Sprintf("claims %d %s, and %s holds %d", stated, n.noun, path.Dir(n.glob), actual)})
			case !total && stated > actual:
				out = append(out, Finding{File: file, Line: lineAt(prose, m[0]), Rule: "count",
					Message: fmt.Sprintf("counts %d %s, and %s holds only %d", stated, n.noun, path.Dir(n.glob), actual)})
			}
		}
	}
	return out
}

// agentClaim is one claim that a document states about what an agent
// reads.
type agentClaim struct {
	file     string
	line     int
	agent    string
	artifact string
	reads    bool
}

func (c agentClaim) says() string {
	if c.reads {
		return "the " + c.agent + " reads the " + c.artifact
	}
	return "the " + c.agent + " never reads the " + c.artifact
}

// The closed vocabulary of a claim. The documents are written in STE, so a
// claim has a fixed shape: "The implementer never opens the spec".
var (
	claimArtifacts = []string{"spec", "ticket", "PRD", "epic", "runbook", "handoff", "pull request", "brief", "policy"}
	claimVerbs     = []string{"reads", "read", "opens", "open"}
	claimMods      = []string{"does not", "must not", "no longer", "cannot", "never", "not", "always", "then", "also", "must", "may", "only"}
	claimDenial    = regexp.MustCompile(`(?i)\b(?:not|never|cannot)\b`)
)

// agentForms maps each prose spelling of an agent to its name: each stage
// of the loop's pack, and each agent that the project's own documents
// name. "the code reviewer" spells code-reviewer.
func agentForms(docs []string) map[string]string {
	var names []string
	if m, err := pack.ReadManifest(pack.Source()); err == nil {
		for stage := range m.Runbooks {
			names = append(names, stage)
		}
	}
	for _, d := range matchFiles("", docs, []string{"docs/agents/*.md", ".claude/agents/*.md", ".loop/policy/*.md"}) {
		names = append(names, strings.TrimSuffix(path.Base(d), ".md"))
	}
	forms := map[string]string{}
	for _, n := range names {
		forms[n] = n
		forms[strings.ReplaceAll(n, "-", " ")] = n
	}
	return forms
}

func claimPattern(forms map[string]string) *regexp.Regexp {
	names := make([]string, 0, len(forms))
	for f := range forms {
		names = append(names, regexp.QuoteMeta(f))
	}
	slices.SortFunc(names, func(a, b string) int { return len(b) - len(a) })
	return regexp.MustCompile(`(?i)\b(?:the|a|an|your|one|each|every|no)\s+(` + strings.Join(names, "|") + `)\s+` +
		`(?:(` + strings.Join(claimMods, "|") + `)\s+)?(?:` + strings.Join(claimVerbs, "|") + `)\s+` +
		`(?:the|a|an|your|its|their|one|that|this|every|no|same)\s+(` + strings.Join(claimArtifacts, "|") + `)\b`)
}

// claimsIn returns each claim of the fixed shape in a document's prose.
func claimsIn(file, text string, forms map[string]string) []agentClaim {
	if len(forms) == 0 {
		return nil
	}
	prose := proseOf(text)
	var out []agentClaim
	for _, m := range claimPattern(forms).FindAllStringSubmatchIndex(prose, -1) {
		agent, ok := forms[strings.ToLower(prose[m[2]:m[3]])]
		if !ok {
			continue
		}
		mod := ""
		if m[4] >= 0 {
			mod = prose[m[4]:m[5]]
		}
		out = append(out, agentClaim{file: file, line: lineAt(prose, m[0]), agent: agent,
			artifact: strings.ToLower(prose[m[6]:m[7]]), reads: !claimDenial.MatchString(mod)})
	}
	return out
}

// claimFindings reports each claim that another claim of the same agent
// and artifact contradicts. Two documents that give one fact opposite
// answers are one fault: one of them is wrong. A claim in the agent's own
// document is the answer, so only the claims that disagree with it are
// reported.
func claimFindings(claims []agentClaim) []Finding {
	type pair struct{ agent, artifact string }
	byPair := map[pair][]agentClaim{}
	var order []pair
	for _, c := range claims {
		p := pair{c.agent, c.artifact}
		if byPair[p] == nil {
			order = append(order, p)
		}
		byPair[p] = append(byPair[p], c)
	}
	var out []Finding
	for _, p := range order {
		cs := byPair[p]
		owner := slices.IndexFunc(cs, func(c agentClaim) bool {
			return strings.TrimSuffix(path.Base(c.file), ".md") == c.agent
		})
		for _, c := range cs {
			var other agentClaim
			switch {
			case owner >= 0 && cs[owner].reads != c.reads:
				other = cs[owner]
			case owner < 0:
				i := slices.IndexFunc(cs, func(o agentClaim) bool { return o.reads != c.reads })
				if i < 0 {
					continue
				}
				other = cs[i]
			default:
				continue
			}
			out = append(out, Finding{File: c.file, Line: c.line, Rule: "claim",
				Message: fmt.Sprintf("states that %s, and %s:%d states that %s. One of the two is wrong", c.says(), other.file, other.line, other.says())})
		}
	}
	return out
}

// repairGlobs are the documents that the repair rule reads beyond the
// agent-facing ones: the plans of production work.
var repairGlobs = []string{"docs/plans/*.md", "docs/plans/*/*.md"}

// A sentence that holds back a production repair to keep evidence clean.
// FORBID with a STAKE forbids a restart or rerun for the sake of evidence.
// DISCOUNT says a restarted or manual run cannot count.
var (
	repairForbid   = regexp.MustCompile(`(?i)\b(?:do not|don't|never|must not|may not)\s+(?:\w+\s+){0,2}(?:restart|re-?run|manually start|repair)\w*`)
	repairStake    = regexp.MustCompile(`(?i)\b(?:evidence|nights?|receipts?|qualif\w*|counts?)\b`)
	repairDiscount = regexp.MustCompile(`(?i)\b(?:restart\w*|re-?run\w*|manual (?:run|start)\w*)\b[^.]{0,60}?\b(?:cannot|can't|does not|do not|never)\s+(?:add|count|qualify)`)
	sentenceEnd    = regexp.MustCompile(`[.!?]\s+`)
	sentenceBreak  = regexp.MustCompile(`^\s*(?:\||#)`)
)

// forbidsRepair reports whether one sentence holds back a repair.
func forbidsRepair(s string) bool {
	return repairForbid.MatchString(s) && repairStake.MatchString(s) || repairDiscount.MatchString(s)
}

type sentence struct {
	line int
	text string
}

// sentences splits prose into sentences, each with the line it starts on.
// A blank line, a table row or a heading ends a sentence.
func sentences(prose string) []sentence {
	var out []sentence
	var open *sentence
	closeOpen := func() {
		if open != nil && strings.TrimSpace(open.text) != "" {
			out = append(out, sentence{open.line, strings.TrimSpace(open.text)})
		}
		open = nil
	}
	for i, l := range strings.Split(prose, "\n") {
		if strings.TrimSpace(l) == "" || sentenceBreak.MatchString(l) {
			closeOpen()
			continue
		}
		parts := splitAfter(l)
		for _, p := range parts {
			if open == nil {
				open = &sentence{line: i + 1, text: p}
			} else {
				open.text += " " + p
			}
			if strings.ContainsAny(p[len(p)-1:], ".!?") {
				closeOpen()
			}
		}
	}
	closeOpen()
	return out
}

// splitAfter splits a line after each sentence end, and keeps the end.
func splitAfter(l string) []string {
	var out []string
	start := 0
	for _, loc := range sentenceEnd.FindAllStringIndex(l, -1) {
		out = append(out, strings.TrimSpace(l[start:loc[0]+1]))
		start = loc[1]
	}
	out = append(out, strings.TrimSpace(l[start:]))
	return slices.DeleteFunc(out, func(s string) bool { return s == "" })
}

// repairFindings reports each sentence of a document that holds back the
// repair of a production fault to keep evidence clean. Repair first, and
// record the evidence in the work item and the logs. A fenced block and a
// code span are no instruction, so a document can quote the bad shape.
func repairFindings(file, text string) []Finding {
	var out []Finding
	for _, s := range sentences(proseOf(text)) {
		if forbidsRepair(s.text) {
			out = append(out, Finding{File: file, Line: s.line, Rule: "repair",
				Message: fmt.Sprintf("%q holds back a production repair to keep evidence. Repair first, and record the evidence", s.text)})
		}
	}
	return out
}
