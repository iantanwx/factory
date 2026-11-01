package pack_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
)

const contentLens = "skills/loop-content-lens/SKILL.md"

// lenses returns the title and the text of each level-3 heading in the
// level-2 section whose title starts with prefix, in document order.
func lenses(t *testing.T, text, prefix string) (titles, bodies []string) {
	t.Helper()
	s := pack.Section(text, 2, func(title string) bool { return strings.HasPrefix(title, prefix) })
	if s == "" {
		t.Fatalf("the content lens holds no section %q", prefix)
	}
	lines := strings.Split(s, "\n")
	for i, level := range pack.Headings(lines) {
		switch {
		case level == 3:
			titles = append(titles, strings.TrimSpace(lines[i][4:]))
			bodies = append(bodies, "")
		case level == 0 && len(bodies) > 0:
			bodies[len(bodies)-1] += lines[i] + "\n"
		}
	}
	return titles, bodies
}

// C13: the fork comment names each upstream with the commit the fork
// read, in the shape of loop-unslop: an upstream line, then a commit line
// of 40 hex characters.
func TestContentLensForkCommentNamesEachUpstreamAndItsCommit(t *testing.T) {
	text := read(t, contentLens)
	_, rest, ok := strings.Cut(text, "<!--")
	comment, _, closed := strings.Cut(rest, "-->")
	if !ok || !closed {
		t.Fatal("the content lens holds no fork comment")
	}
	for repo, want := range map[string]string{
		"EveryInc/compound-engineering-plugin": "8f7eea958c8f1318147db6c93320cae3930ad09a",
		"cursor/plugins":                       "a58628271271837ef5f386adca29c0812683a19a",
	} {
		re := regexp.MustCompile(`upstream: +https://github\.com/` + regexp.QuoteMeta(repo) + `/\S*\n +commit: +([0-9a-f]{40})\n`)
		m := re.FindStringSubmatch(comment)
		if m == nil {
			t.Errorf("the fork comment names no upstream %s with a 40-character commit", repo)
			continue
		}
		if m[1] != want {
			t.Errorf("the fork comment pins %s at %s, want %s", repo, m[1], want)
		}
	}
	if !strings.Contains(comment, "pstack/skills/architect/references/design-red-flags.md") {
		t.Error("the fork comment does not name the design red flags file of cursor/plugins")
	}
}

// C13: coherence and feasibility run on every document, by the
// operator's "2 always, rest by signal" answer.
func TestContentLensRunsCoherenceAndFeasibilityAlways(t *testing.T) {
	titles, _ := lenses(t, read(t, contentLens), "Lenses that always run")
	if want := []string{"Coherence", "Feasibility"}; !slices.Equal(titles, want) {
		t.Errorf("the lenses that always run are %q, want %q", titles, want)
	}
}

// C13: each other lens names the signal that turns it on.
func TestContentLensTurnsEachOtherLensOnBySignal(t *testing.T) {
	titles, bodies := lenses(t, read(t, contentLens), "Lenses that run on a signal")
	if want := []string{"Product", "Design", "Security", "Scope", "Adversarial"}; !slices.Equal(titles, want) {
		t.Fatalf("the lenses that run on a signal are %q, want %q", titles, want)
	}
	signal := regexp.MustCompile(`(?m)^\*\*Signal:\*\* \S`)
	for i, body := range bodies {
		if !signal.MatchString(body) {
			t.Errorf("lens %s names no signal on a line that starts with **Signal:**", titles[i])
		}
	}
}

// C13: the design red flags check a spec and an ADR, and a PRD gets none,
// because a PRD names no module design.
func TestContentLensAppliesTheRedFlagsToASpecAndAnADROnly(t *testing.T) {
	text := read(t, contentLens)
	titles, _ := lenses(t, text, "Design red flags")
	if len(titles) != 8 {
		t.Errorf("the content lens holds %d design red flags, want 8: %q", len(titles), titles)
	}
	s := strings.Join(strings.Fields(pack.Section(text, 2, func(title string) bool {
		return strings.HasPrefix(title, "Design red flags")
	})), " ")
	for _, want := range []string{"a spec and an ADR", "A PRD gets no red flag"} {
		if !strings.Contains(s, want) {
			t.Errorf("the design red flags section does not hold %q", want)
		}
	}
}

// The doc-reviewer's run receives the skill, and its runbook names the
// path and leaves the lenses to the reader.
func TestDocReviewerReceivesTheContentLens(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Runbooks["doc-reviewer"].Skills, "loop-content-lens") {
		t.Error("the doc-reviewer manifest entry does not list loop-content-lens")
	}
	s := strings.Join(strings.Fields(section(t, read(t, "runbooks/doc-reviewer.md"), "1. Before you review")), " ")
	for _, want := range []string{"`../loop-content-lens/SKILL.md`", "You do not apply them yourself"} {
		if !strings.Contains(s, want) {
			t.Errorf("the doc-reviewer runbook § 1 does not hold %q", want)
		}
	}
}

// reviewSection returns the level-2 section of loop-sdlc-review whose
// heading starts with prefix, with each run of white space as one space,
// so a wrapped line still matches.
func reviewSection(t *testing.T, prefix string) string {
	t.Helper()
	text := read(t, reviewSkill)
	_, rest, ok := strings.Cut(text, "\n## "+prefix)
	if !ok {
		t.Fatalf("%s holds no section %q", reviewSkill, prefix)
	}
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return strings.Join(strings.Fields(rest), " ")
}

// TestReviewSkillLetsSubagentsFindAndOnlyTheReviewerJudge proves C10 of
// factorio-4mb8 (A14, A15). A doc review starts readers as subagents, so
// the old ban on a subagent that reviews goes. The verdict still has one
// author: the reviewer, who read all of the work and judged each finding.
func TestReviewSkillLetsSubagentsFindAndOnlyTheReviewerJudge(t *testing.T) {
	never := reviewSection(t, "9. Never")
	if strings.Contains(never, "Never start a subagent that reviews") {
		t.Errorf("§ 9 of %s still forbids a subagent that reviews: %s", reviewSkill, never)
	}
	holdsAll(t, "§ 9 of loop-sdlc-review", never, []string{
		"A subagent may find.",
		"Only the reviewer judges each finding and submits the verdict.",
		"The verdict needs one reader who read all of it, and that reader is the reviewer.",
	})
}

// TestReviewSkillLetsAConfirmedProseFindingRejectADocument proves C11 of
// factorio-4mb8, the operator's "Yes, may reject" answer at the PRD
// sign-off. A doc review has prose readers and no prose linter, so a prose
// finding that the reviewer confirmed may reject alone. The exception names
// the doc review, so the code-reviewer keeps the rule as it was.
func TestReviewSkillLetsAConfirmedProseFindingRejectADocument(t *testing.T) {
	judge := reviewSection(t, "6. Judge the work")
	holdsAll(t, "§ 6 of loop-sdlc-review", judge, []string{
		"Never reject on style that no linter flagged.",
		"In a doc review, a reader's prose finding may reject alone when you check it against the text and it holds.",
		"A code review has no such exception.",
	})
}

const (
	docReaders   = "skills/loop-doc-readers/SKILL.md"
	readerFile   = "skills/loop-doc-readers/reader.md"
	reportSchema = "skills/loop-doc-readers/report.schema.json"
	readerAgent  = "agents/loop-doc-reader.md"
)

// TestDocReaderHoldsReadGrepAndGlobOnly proves C7 of factorio-4mb8 (A6,
// A7). A reader with no Write, Edit or Bash can write no file and run no
// loop verb. The pack gate reads the agent's loop commands, so this test
// reads the tool list only.
func TestDocReaderHoldsReadGrepAndGlobOnly(t *testing.T) {
	text := read(t, readerAgent)
	head, ok := strings.CutPrefix(text, "---\n")
	head, _, closed := strings.Cut(head, "\n---\n")
	if !ok || !closed {
		t.Fatalf("%s opens with no front matter", readerAgent)
	}
	var tools []string
	for _, l := range strings.Split(head, "\n") {
		if v, ok := strings.CutPrefix(l, "tools:"); ok {
			for _, t := range strings.Split(v, ",") {
				tools = append(tools, strings.TrimSpace(t))
			}
		}
	}
	if want := []string{"Read", "Grep", "Glob"}; !slices.Equal(tools, want) {
		t.Errorf("the reader agent holds the tools %q, want %q and no other", tools, want)
	}
}

// TestEachReaderStartsClean proves C8 of factorio-4mb8 (A4). The prompt of
// a reader holds four facts, and nothing of the reviewer's context. One
// agent type starts every reader, and the runbook starts the readers
// before its own read of each kind of document.
func TestEachReaderStartsClean(t *testing.T) {
	text := read(t, docReaders)
	start := section(t, text, "2. Start the readers")
	holdsAll(t, "§ 2 of loop-doc-readers", start, []string{
		"Start each reader as an agent of type `loop-doc-reader`. That agent type is the one way to start a reader.",
		"Lens: <unslop | asd-ste100 | content-lens>",
		"Document: <path> at <head sha>, kind <prd | spec | adr>",
		"Follow .claude/skills/loop-doc-readers/reader.md, and return one ReaderReport.",
		"The prompt holds no reviewer note, no earlier round and no other reader's output.",
	})
	for _, name := range config.DocReaderNames {
		if !strings.Contains(start, "`"+name+"`") {
			t.Errorf("§ 2 of loop-doc-readers does not name the reader `%s`", name)
		}
	}
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range m.Agents {
		if a != "loop-doc-reader" && strings.Contains(text, a) {
			t.Errorf("loop-doc-readers names agent %s, and a reader starts only as loop-doc-reader", a)
		}
	}
	runbook := read(t, "runbooks/doc-reviewer.md")
	for _, prefix := range []string{"2. Review a PRD", "3. Review a spec", "4. Review an ADR", "5. Review a design record"} {
		s := section(t, runbook, prefix)
		begin := strings.Index(s, "Start the readers first, by `../loop-doc-readers/SKILL.md` § 2, when the brief names readers. Then read the document")
		check := strings.Index(s, " 1. ")
		if begin < 0 || check < 0 || begin > check {
			t.Errorf("§ %s of the doc-reviewer runbook does not start the readers before its first check", prefix)
		}
	}
}

// validate checks v against the subset of JSON Schema that
// report.schema.json uses: type, required, properties, items and
// minLength. It returns the first fault it finds, or "".
func validate(schema map[string]any, v any, at string) string {
	switch schema["type"] {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return at + " is no object"
		}
		for _, r := range schema["required"].([]any) {
			if _, ok := obj[r.(string)]; !ok {
				return at + " lacks " + r.(string)
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for k, sub := range props {
			if fv, ok := obj[k]; ok {
				if bad := validate(sub.(map[string]any), fv, at+"."+k); bad != "" {
					return bad
				}
			}
		}
	case "array":
		list, ok := v.([]any)
		if !ok {
			return at + " is no array"
		}
		for i, item := range list {
			if bad := validate(schema["items"].(map[string]any), item, fmt.Sprintf("%s[%d]", at, i)); bad != "" {
				return bad
			}
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			return at + " is no string"
		}
		if n, ok := schema["minLength"].(float64); ok && float64(len(s)) < n {
			return at + " is shorter than minLength"
		}
	}
	return ""
}

// examples returns the schema and each JSON example report of reader.md.
func examples(t *testing.T) (map[string]any, []map[string]any) {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal([]byte(read(t, reportSchema)), &schema); err != nil {
		t.Fatalf("%s: %v", reportSchema, err)
	}
	var out []map[string]any
	for _, block := range strings.Split(read(t, readerFile), "```json\n")[1:] {
		body, _, _ := strings.Cut(block, "```")
		var r map[string]any
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatalf("an example report of reader.md is no JSON: %v", err)
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		t.Fatal("reader.md holds no example report in a json block")
	}
	return schema, out
}

// mutate returns a deep copy of r, and the copy's first finding or
// question, by key.
func mutate(t *testing.T, r map[string]any, key string) (map[string]any, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(r)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	list, _ := c[key].([]any)
	if len(list) == 0 {
		return c, nil
	}
	return c, list[0].(map[string]any)
}

// TestAFindingWithNoPlaceRuleOrEditIsNoFinding proves C9 of factorio-4mb8
// (A5). The schema refuses a finding that lacks where, rule or edit, or
// holds one empty, and the reviewer drops and counts it.
func TestAFindingWithNoPlaceRuleOrEditIsNoFinding(t *testing.T) {
	schema, reports := examples(t)
	mutated := 0
	for i, r := range reports {
		if bad := validate(schema, r, "report"); bad != "" {
			t.Errorf("example %d of reader.md fails the schema: %s", i+1, bad)
		}
		for _, field := range []string{"where", "rule", "edit"} {
			for _, empty := range []bool{false, true} {
				c, f := mutate(t, r, "findings")
				if f == nil {
					continue
				}
				mutated++
				if empty {
					f[field] = ""
				} else {
					delete(f, field)
				}
				if validate(schema, c, "report") == "" {
					t.Errorf("example %d passes the schema with its first finding's %s gone (empty %v)", i+1, field, empty)
				}
			}
		}
	}
	if mutated == 0 {
		t.Error("no example report of reader.md holds a finding")
	}
	holdsAll(t, "§ 3 of loop-doc-readers", section(t, read(t, docReaders), "3. Read each report"), []string{
		"Drop each finding that fails the schema. It is no finding.",
		"Count the findings you drop for each reader, and name each count in the comment.",
	})
}

// TestALaterRoundDropsAFindingOutsideTheDiff proves C19 of factorio-4mb8.
// A reader reads the whole document in every round, so the reviewer keeps
// only what loop-sdlc-review § 4 lets a later round judge.
func TestALaterRoundDropsAFindingOutsideTheDiff(t *testing.T) {
	holdsAll(t, "§ 4 of loop-doc-readers", section(t, read(t, docReaders), "4. Judge each finding"), []string{
		"In a later round, keep only the findings inside the diff since the last reviewed head.",
		"Name the rest in the comment as outside this round, by `../loop-sdlc-review/SKILL.md` § 4.",
	})
}

// TestDocReviewerManifestAndRunbookAgreeOnTheReaders proves C20 of
// factorio-4mb8 (A1). The run receives each skill and the agent its
// runbook names, and the runbook passes the server's reader names in
// start order.
func TestDocReviewerManifestAndRunbookAgreeOnTheReaders(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Skills, "loop-doc-readers") {
		t.Error("the manifest's skills do not list loop-doc-readers")
	}
	rb := m.Runbooks["doc-reviewer"]
	runbook := read(t, "runbooks/doc-reviewer.md")
	for _, s := range []string{"loop-doc-readers", "loop-content-lens", "loop-unslop"} {
		if !slices.Contains(rb.Skills, s) {
			t.Errorf("the doc-reviewer manifest entry does not list skill %s", s)
		}
		if p := "../" + s + "/SKILL.md"; !strings.Contains(runbook, p) {
			t.Errorf("the doc-reviewer runbook never names %s", p)
		}
	}
	if !slices.Contains(rb.Agents, "loop-doc-reader") {
		t.Error("the doc-reviewer manifest entry does not list agent loop-doc-reader")
	}
	if !strings.Contains(runbook, "loop-doc-reader`") {
		t.Error("the doc-reviewer runbook never names agent loop-doc-reader")
	}
	submit := section(t, runbook, "6. Submit the verdict")
	holdsAll(t, "§ 6 of the doc-reviewer runbook", submit, []string{
		"--readers " + strings.Join(config.DocReaderNames, ","),
		"A reader that fails twice stops the review. Submit nothing. Report a loop fault with `loop fault report`, and finish `blocked`.",
		"When the tree is not clean, a reader wrote a file. Submit nothing. Report a loop fault with `loop fault report`, and finish `blocked`.",
	})
}

// TestEachLensFollowsItsOwnSkill proves C24 of factorio-4mb8. The lens
// table of reader.md binds each lens to one skill path, and no path to two
// lenses.
func TestEachLensFollowsItsOwnSkill(t *testing.T) {
	text := read(t, readerFile)
	row := regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\| `(\\.\\./loop-[a-z0-9-]+/SKILL\\.md)` \\|$")
	got := map[string]string{}
	paths := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(text, -1) {
		if _, ok := got[m[1]]; ok {
			t.Errorf("reader.md binds lens %s twice", m[1])
		}
		if lens, ok := paths[m[2]]; ok {
			t.Errorf("reader.md binds %s to lens %s and to lens %s", m[2], lens, m[1])
		}
		got[m[1]], paths[m[2]] = m[2], m[1]
	}
	want := map[string]string{
		"unslop":       "../loop-unslop/SKILL.md",
		"asd-ste100":   "../loop-asd-ste100/SKILL.md",
		"content-lens": "../loop-content-lens/SKILL.md",
	}
	if !maps.Equal(got, want) {
		t.Errorf("the lens table of reader.md binds %v, want %v", got, want)
	}
	holdsAll(t, "§ 2 of reader.md", section(t, text, "2. Read by the skill of your lens"), []string{
		"Read the document by the skill of your lens only.",
	})
}

// TestTheReviewerMergesAndRanks proves C25 of factorio-4mb8. Two readers
// can return one fault, and the comment names it once, worst first.
func TestTheReviewerMergesAndRanks(t *testing.T) {
	holdsAll(t, "§ 4 of loop-doc-readers", section(t, read(t, docReaders), "4. Judge each finding"), []string{
		"Make one finding of two findings on one place, and name each reader that returned it.",
		"Rank the confirmed findings by `../loop-sdlc-review/SKILL.md` § 3, the worst first, in the one comment.",
	})
}

// TestAReaderReturnsEachQuestionAndTheReviewerScoresIt proves C26 of
// factorio-4mb8 (A7). A reader decides nothing, so the schema requires its
// questions, and the reviewer scores each one.
func TestAReaderReturnsEachQuestionAndTheReviewerScoresIt(t *testing.T) {
	schema, reports := examples(t)
	asked := false
	for i, r := range reports {
		c, q := mutate(t, r, "questions")
		if q == nil {
			continue
		}
		asked = true
		delete(q, "where")
		if validate(schema, c, "report") == "" {
			t.Errorf("example %d passes the schema with a question that lacks where", i+1)
		}
		c, _ = mutate(t, r, "questions")
		c["questions"] = []any{}
		if bad := validate(schema, c, "report"); bad != "" {
			t.Errorf("example %d fails the schema with an empty list of questions: %s", i+1, bad)
		}
		delete(c, "questions")
		if validate(schema, c, "report") == "" {
			t.Errorf("example %d passes the schema with no questions field", i+1)
		}
	}
	if !asked {
		t.Error("no example report of reader.md holds a question")
	}
	holdsAll(t, "§ 4 of reader.md", section(t, read(t, readerFile), "4. Return each question"), []string{
		"Return each question you cannot decide in `questions`.",
		"Never answer a question in a finding.",
	})
	holdsAll(t, "§ 5 of loop-doc-readers", section(t, read(t, docReaders), "5. Score each question"), []string{
		"Score each question by `../loop-sdlc-triage/SKILL.md`.",
		"When the document or the repository answers it, answer it yourself.",
		"At level 4 or 5, escalate it by `../loop-doc-reviewer/SKILL.md` § 7.",
	})
}

// TestTheGlossaryDefinesAReader proves C21 of factorio-4mb8. A runbook that
// reads the word "reader" reads the rule of C10: a reader finds, and only
// the reviewer judges. Each doc review phase names the readers.
func TestTheGlossaryDefinesAReader(t *testing.T) {
	sections := glossarySections(read(t, glossarySkill))
	reader := glossaryTerms(sections["People and agents"])["A reader"]
	holdsAll(t, "the glossary's reader", reader, []string{
		"a subagent of the doc-reviewer",
		"A reader finds, and it never judges.",
		"`../loop-doc-readers/SKILL.md`",
	})
	var reviews []string
	for _, l := range sections["Phases"] {
		switch m := phaseLine.FindStringSubmatch(l); {
		case m != nil:
			reviews = append(reviews, "")
			if m[2] == "Doc review" || m[2] == "ADR" {
				reviews[len(reviews)-1] = l
			}
		case len(reviews) > 0 && reviews[len(reviews)-1] != "" && strings.HasPrefix(l, "   "):
			reviews[len(reviews)-1] += " " + strings.TrimSpace(l)
		}
	}
	n := 0
	for _, p := range reviews {
		if p == "" {
			continue
		}
		n++
		if !strings.Contains(p, "the doc-reviewer starts its readers and reviews") {
			t.Errorf("the phase %q does not name the reader step", p)
		}
	}
	if n != 4 {
		t.Errorf("the glossary holds %d phases with a doc review, want 4", n)
	}
}
