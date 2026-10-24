package pack

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
)

// The gate (R39) reads every command reference in the pack and checks it
// against the schema the binary prints, built in process from a registry.
// A reference is a `loop ...` invocation in an inline code span or on a
// line of a fenced block, the same two places runbook-check.ts (alpha)
// read. Prose names no command, so the gate cannot see an instruction that
// names none. It checks:
//
//   - each verb, each alternative of an alternation such as fire|defer, and
//     each flag against the verb's schema, plus each literal value of an
//     enum flag
//   - each refusal code: a code span that holds only an upper-case code, a
//     "code" key in a fenced block, and any code written bare in prose
//   - vocabulary that belongs to one project (R37): owner names, tracker
//     state names, absolute host paths and one project's tooling
//   - the manifest against the files, each runbook's front matter, each
//     relative pack path, each skill a runbook reads named by its path,
//     each policy file, and no step in an unattended runbook that asks a
//     person
//   - no hand-back in the coordinator's runbook or in a skill or agent it
//     reads: no step that sends the operator to merge, upgrade or install,
//     or to run a loop verb the coordinator may call (handback.go)
//   - each agent: its front matter, tools that only read, a loop command
//     only where the schema marks the verb a read the coordinator may
//     call, a runbook that dispatches it and names it, and each path from
//     the agent directory to the skill root
//
// It does not check positional arguments, a required flag missing from a
// mention, or whether a sentence describes what a verb does.

// Finding is one fault the gate reports.
type Finding struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", f.File, f.Line, f.Kind, f.Detail)
}

// index is the slice of the schema the gate reads.
type index struct {
	verbs  map[string]*cli.Verb // by path
	nouns  map[string]bool
	global map[string]cli.Flag
	codes  map[string]bool // refusal codes and exit names
	// prose skips the word loop where it is no command: an unknown word
	// after it, with loop out of command position, as in "the loop for
	// stalls". A project's documents use loop as a word.
	prose bool
}

func newIndex(reg *cli.Registry) (*index, error) {
	s, err := reg.Schema()
	if err != nil {
		return nil, fmt.Errorf("pack: read the schema: %w", err)
	}
	ix := &index{verbs: map[string]*cli.Verb{}, nouns: map[string]bool{}, global: map[string]cli.Flag{}, codes: map[string]bool{}}
	for _, v := range s.Verbs {
		ix.verbs[v.Path] = v
		if !v.TopLevel {
			ix.nouns[v.Noun] = true
		}
	}
	for _, f := range s.GlobalFlags {
		ix.global[f.Name] = f
	}
	for _, c := range s.RefusalCodes {
		ix.codes[c.Code] = true
	}
	for _, e := range s.ExitCodes {
		ix.codes[e.Name] = true
	}
	return ix, nil
}

// span is one piece of code in a Markdown file.
type span struct {
	line   int
	text   string
	fenced bool
}

var fence = regexp.MustCompile("^\\s*(```|~~~)")

// split returns the code of a Markdown file and its prose lines with every
// inline code span blanked. A fenced line is no prose line.
func split(text string) (code, prose []span) {
	in := false
	for i, raw := range strings.Split(text, "\n") {
		n := i + 1
		if fence.MatchString(raw) {
			in = !in
			continue
		}
		if in {
			code = append(code, span{n, raw, true})
			continue
		}
		var b strings.Builder
		rest := raw
		for {
			open := strings.IndexByte(rest, '`')
			if open < 0 {
				b.WriteString(rest)
				break
			}
			end := strings.IndexByte(rest[open+1:], '`')
			if end < 0 {
				b.WriteString(rest)
				break
			}
			code = append(code, span{n, rest[open+1 : open+1+end], false})
			b.WriteString(rest[:open] + " ")
			rest = rest[open+1+end+1:]
		}
		prose = append(prose, span{n, b.String(), false})
	}
	return code, prose
}

// CheckText gates one Markdown file of the pack: commands, codes and
// vocabulary.
func CheckText(file, text string, reg *cli.Registry) ([]Finding, error) {
	ix, err := newIndex(reg)
	if err != nil {
		return nil, err
	}
	return ix.text(file, text), nil
}

// CheckCommands gates the `loop` commands of any Markdown file, such as a
// project's agent-facing documents: each verb, flag and enum value, and
// each refusal code on a line that runs loop. It leaves out the rules that
// bind the pack alone: vocabulary (R37), and an upper-case word on a line
// that runs no loop command, which a project's own names fill.
func CheckCommands(file, text string, reg *cli.Registry) ([]Finding, error) {
	ix, err := newIndex(reg)
	if err != nil {
		return nil, err
	}
	ix.prose = true
	var out []Finding
	code, _ := split(text)
	runs := map[int]bool{}
	for _, s := range code {
		for _, f := range ix.commands(s) {
			out = append(out, Finding{File: file, Line: s.line, Kind: f[0], Detail: f[1]})
		}
		if len(ix.calls(s)) > 0 {
			runs[s.line] = true
		}
	}
	for _, s := range code {
		if runs[s.line] {
			ix.codeRefs(s, func(c string) {
				out = append(out, Finding{File: file, Line: s.line, Kind: "code", Detail: c + " is no refusal code or exit name in the schema"})
			})
		}
	}
	slices.SortStableFunc(out, func(a, b Finding) int { return a.Line - b.Line })
	return out, nil
}

func (ix *index) text(file, text string) []Finding {
	var out []Finding
	add := func(line int, kind, format string, args ...any) {
		out = append(out, Finding{File: file, Line: line, Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
	code, prose := split(text)
	for _, s := range code {
		for _, f := range ix.commands(s) {
			add(s.line, f[0], "%s", f[1])
		}
		ix.codeRefs(s, func(c string) { add(s.line, "code", "%s is no refusal code or exit name in the schema", c) })
	}
	for _, p := range prose {
		for _, m := range bareCode.FindAllString(p.text, -1) {
			add(p.line, "code", "write %s in a code span, so the gate can check it", m)
		}
	}
	for i, line := range strings.Split(text, "\n") {
		for _, v := range vocabulary {
			if m := v.re.FindString(line); m != "" {
				add(i+1, "vocabulary", "%s is %s; the pack names roles and loop states only (R37)", strings.TrimSpace(m), v.what)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Finding) int { return a.Line - b.Line })
	return out
}

var (
	codeSpan  = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*$`)
	codeKey   = regexp.MustCompile(`"code"\s*:\s*"([A-Z][A-Z0-9_]*)"`)
	bareCode  = regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b`)
	loopStart = regexp.MustCompile("(?:^|[\\s(|&;`])loop\\s+")
)

func (ix *index) codeRefs(s span, bad func(string)) {
	if !s.fenced {
		t := strings.TrimSpace(s.text)
		if len(t) >= 4 && codeSpan.MatchString(t) && !ix.codes[t] {
			bad(t)
		}
		return
	}
	for _, m := range codeKey.FindAllStringSubmatch(s.text, -1) {
		if !ix.codes[m[1]] {
			bad(m[1])
		}
	}
}

// commands checks every `loop` invocation in one piece of code. Each
// result is a kind and a detail.
func (ix *index) commands(s span) [][2]string {
	var out [][2]string
	for _, toks := range ix.calls(s) {
		out = append(out, ix.command(toks)...)
	}
	return out
}

// commandAt matches the text before a word in command position: the start,
// a prompt, a shell operator or a shell keyword.
var commandAt = regexp.MustCompile(`(?:^\s*(?:\$\s*)?|[|&;(` + "`" + `]\s*|\b(?:if|then|else|do|while|until|time|exec|xargs|sudo)\s+|!\s*)$`)

// calls returns the words of each `loop` invocation in one piece of code.
func (ix *index) calls(s span) [][]string {
	text := s.text
	if s.fenced {
		text = stripComment(text)
	}
	var out [][]string
	for _, loc := range loopStart.FindAllStringIndex(text, -1) {
		toks := tokens(text[loc[1]:])
		at := loc[0] + strings.Index(text[loc[0]:loc[1]], "loop")
		if ix.prose && !ix.known(toks) && !commandAt.MatchString(text[:at]) {
			continue
		}
		out = append(out, toks)
	}
	return out
}

// known reports words that start with a noun or a top-level verb.
func (ix *index) known(toks []string) bool {
	return len(toks) > 0 && (ix.nouns[toks[0]] || ix.verbs[toks[0]] != nil && ix.verbs[toks[0]].TopLevel)
}

// stripComment drops a shell comment from a fenced line.
func stripComment(line string) string {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return ""
	}
	if i := strings.Index(line, " #"); i >= 0 {
		return line[:i]
	}
	return line
}

// tokens splits a command line into words up to the first shell operator.
// Quotes group a word and are dropped. Brackets that mark an optional part
// are dropped.
func tokens(line string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	has := false
	flush := func() bool {
		if !has {
			return true
		}
		t := strings.Trim(cur.String(), "[]")
		cur.Reset()
		has = false
		end := false
		for _, suffix := range []string{")", ";"} {
			if strings.HasSuffix(t, suffix) && !strings.Contains(t, "(") {
				t, end = strings.TrimSuffix(t, suffix), true
			}
		}
		switch {
		case t == "|" || t == "||" || t == "&&" || t == "&" || t == ";" || strings.HasPrefix(t, ">") ||
			strings.HasPrefix(t, "2>") || t == "<" || strings.HasPrefix(t, "#"):
			return false
		}
		if t != "" {
			out = append(out, t)
		}
		return !end
	}
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			has = true
		case r == '"' || r == '\'':
			quote, has = r, true
		case r == ' ' || r == '\t':
			if !flush() {
				return out
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	flush()
	return out
}

// placeholder reports a word the reader fills in: <item>, $sha, {x}, ...
func placeholder(t string) bool {
	return strings.ContainsAny(t, "<>${}") || strings.Contains(t, "...")
}

// resolve returns the verb paths the words of one invocation name, each
// alternative of an alternation apart, and the words after them. It
// returns no path for a mention of a noun alone, and unknown for a first
// word that is no noun or top-level verb.
func (ix *index) resolve(toks []string) (paths, rest []string, unknown bool) {
	if len(toks) == 0 || placeholder(toks[0]) || strings.HasPrefix(toks[0], "-") {
		return nil, nil, false
	}
	switch {
	case ix.verbs[toks[0]] != nil && ix.verbs[toks[0]].TopLevel:
		return []string{toks[0]}, toks[1:], false
	case ix.nouns[toks[0]]:
		if len(toks) < 2 || placeholder(toks[1]) || strings.HasPrefix(toks[1], "-") {
			return nil, nil, false // a mention of the noun, or of its verbs in general
		}
		for _, v := range strings.Split(toks[1], "|") {
			paths = append(paths, toks[0]+" "+v)
		}
		return paths, toks[2:], false
	}
	return nil, nil, true
}

func (ix *index) command(toks []string) [][2]string {
	paths, rest, unknown := ix.resolve(toks)
	if unknown {
		return [][2]string{{"verb", fmt.Sprintf("`loop %s` names no noun or top-level verb in the schema", toks[0])}}
	}
	var out [][2]string
	for _, p := range paths {
		v := ix.verbs[p]
		if v == nil {
			out = append(out, [2]string{"verb", fmt.Sprintf("`loop %s` is no verb in the schema", p)})
			continue
		}
		out = append(out, ix.flags(v, rest)...)
	}
	return out
}

func (ix *index) flags(v *cli.Verb, toks []string) [][2]string {
	var out [][2]string
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if !strings.HasPrefix(t, "--") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(t, "--"), "=")
		if placeholder(name) {
			continue
		}
		f, ok := ix.flag(v, name)
		if !ok {
			out = append(out, [2]string{"flag", fmt.Sprintf("`loop %s` takes no flag --%s", v.Path, name)})
			continue
		}
		if f.Type == cli.FlagBool {
			continue
		}
		if !hasValue && i+1 < len(toks) && !strings.HasPrefix(toks[i+1], "--") {
			value, hasValue = toks[i+1], true
			i++
		}
		if !hasValue || len(f.Enum) == 0 || placeholder(value) {
			continue
		}
		for _, one := range strings.Split(value, "|") {
			if !slices.Contains(f.Enum, one) {
				out = append(out, [2]string{"value", fmt.Sprintf("`loop %s --%s` takes %s, not %q", v.Path, name, strings.Join(f.Enum, "|"), one)})
			}
		}
	}
	return out
}

func (ix *index) flag(v *cli.Verb, name string) (cli.Flag, bool) {
	for _, f := range v.Flags {
		if f.Name == name {
			return f, true
		}
	}
	f, ok := ix.global[name]
	return f, ok
}

type term struct {
	re   *regexp.Regexp
	what string
}

// vocabulary is what the pack never holds (R37). The patterns match case
// as written, so ordinary words such as "done" and "linear" stay usable.
var vocabulary = []term{
	{regexp.MustCompile(`\b(Ian|Willis|Septin)\b`), "an owner's name"},
	{regexp.MustCompile(`\b(Needs Human|In Review|In Progress|Ready to Merge|PRD Needed|PRD Drafting|PRD in Review|PRD Sign-off|` +
		`Spec Needed|Spec Drafting|Spec in Review|Spec Sign-off|Spec Approved|Bug Reported|Todo)\b`), "a tracker state name"},
	{regexp.MustCompile(`"(Backlog|Done|Canceled|Execution|Decomposing|Merging|Repair|Architecture|Reproducing|Triage)"`), "a tracker state name"},
	{regexp.MustCompile(`(?:^|[^A-Za-z0-9_.~/-])/(?:home|Users|root|opt|srv|var|etc|usr|mnt|Volumes|private|nix|snap)/`), "an absolute host path"},
	{regexp.MustCompile(`(?:^|[^A-Za-z0-9_.~/-])~/`), "a path in a home directory"},
	{regexp.MustCompile(`\b(pnpm|Linear|orca|TickerTown|TIC-[0-9]+)\b`), "one project's tooling or content"},
}

// Check gates a whole pack source against a registry's schema.
func Check(src fs.FS, reg *cli.Registry) ([]Finding, error) {
	ix, err := newIndex(reg)
	if err != nil {
		return nil, err
	}
	m, err := ReadManifest(src)
	if err != nil {
		return []Finding{{File: ManifestFile, Kind: "manifest", Detail: err.Error()}}, nil
	}
	out := manifestFindings(src, m)
	tree, err := Layout(src)
	if err != nil {
		return append(out, Finding{File: ManifestFile, Kind: "manifest", Detail: err.Error()}), nil
	}
	if _, err := Agents(src); err != nil {
		return append(out, Finding{File: ManifestFile, Kind: "manifest", Detail: err.Error()}), nil
	}
	files, err := markdown(src)
	if err != nil {
		return nil, err
	}
	held := handBackFiles(m)
	for _, f := range files {
		raw, err := fs.ReadFile(src, f.source)
		if err != nil {
			return nil, err
		}
		text := string(raw)
		out = append(out, ix.text(f.source, text)...)
		if heldToHandBack(held, f.source) {
			out = append(out, ix.handBack(f.source, text)...)
		}
		if f.agent != "" {
			out = append(out, agentPaths(tree, f.source, text)...)
			out = append(out, ix.agentFindings(f.source, f.agent, text)...)
			continue
		}
		out = append(out, packPaths(tree, f, text)...)
		if path.Base(f.injected) == "SKILL.md" {
			out = append(out, frontmatter(f.source, path.Dir(f.injected), text)...)
		}
		if f.stage != "" {
			out = append(out, runbookFindings(f.source, m.Runbooks[f.stage], text)...)
		}
	}
	return out, nil
}

// packFile is one Markdown file: where the source holds it, where a run's
// skill root receives it, the stage of a runbook, and the name of an
// agent. An agent lands outside the skill root, so it has no injected
// path.
type packFile struct {
	source, injected, stage, agent string
}

func markdown(src fs.FS) ([]packFile, error) {
	var out []packFile
	runbooks, err := fs.Glob(src, "runbooks/*.md")
	if err != nil {
		return nil, err
	}
	for _, r := range runbooks {
		stage := strings.TrimSuffix(path.Base(r), ".md")
		out = append(out, packFile{source: r, injected: Injected(stage) + "/SKILL.md", stage: stage})
	}
	err = fs.WalkDir(src, "skills", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".md") {
			return err
		}
		out = append(out, packFile{source: name, injected: strings.TrimPrefix(name, "skills/")})
		return nil
	})
	if err != nil {
		return nil, err
	}
	agents, err := fs.Glob(src, "agents/*.md")
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		out = append(out, packFile{source: a, agent: strings.TrimSuffix(path.Base(a), ".md")})
	}
	return out, nil
}

func manifestFindings(src fs.FS, m *Manifest) []Finding {
	var out []Finding
	add := func(format string, args ...any) {
		out = append(out, Finding{File: ManifestFile, Kind: "manifest", Detail: fmt.Sprintf(format, args...)})
	}
	runbooks, _ := fs.Glob(src, "runbooks/*")
	for _, r := range runbooks {
		stage := strings.TrimSuffix(path.Base(r), ".md")
		if _, ok := m.Runbooks[stage]; !ok || !strings.HasSuffix(r, ".md") {
			add("%s is a file the manifest names as no runbook", r)
		}
	}
	skills, _ := fs.ReadDir(src, "skills")
	for _, d := range skills {
		if !slices.Contains(m.Skills, d.Name()) {
			add("skills/%s is a skill the manifest does not list", d.Name())
		}
	}
	agents, _ := fs.ReadDir(src, "agents")
	for _, d := range agents {
		if name, ok := strings.CutSuffix(d.Name(), ".md"); !ok || d.IsDir() || !slices.Contains(m.Agents, name) {
			add("agents/%s is an agent the manifest does not list", d.Name())
		}
	}
	for _, a := range m.Agents {
		used := false
		for _, rb := range m.Runbooks {
			used = used || slices.Contains(rb.Agents, a)
		}
		if !used {
			add("agent %s is in no runbook's agents, so nothing dispatches it", a)
		}
	}
	for _, s := range m.Skills {
		out = append(out, flagFindings(src, m, s)...)
	}
	return out
}

// codexPolicy is the agents/openai.yaml of a flagged skill, byte for byte.
// Codex ignores disable-model-invocation and reads this file instead, so a
// flagged skill holds both, and the gate compares bytes and parses no YAML.
const codexPolicy = "policy:\n  allow_implicit_invocation: false\n"

// flagFindings checks one listed skill's disable-model-invocation flag. A
// flagged skill is out of every harness's skill list, so a run reads it
// only when a runbook lists it, and Codex leaves it out only when the skill
// also holds codexPolicy.
func flagFindings(src fs.FS, m *Manifest, skill string) []Finding {
	var out []Finding
	add := func(format string, args ...any) {
		out = append(out, Finding{File: ManifestFile, Kind: "manifest", Detail: fmt.Sprintf(format, args...)})
	}
	raw, err := fs.ReadFile(src, "skills/"+skill+"/SKILL.md")
	if err != nil {
		return nil
	}
	fields, _ := frontFields(string(raw))
	flagged := fields["disable-model-invocation"] == "true"
	codex, err := fs.ReadFile(src, "skills/"+skill+"/agents/openai.yaml")
	held := err == nil
	if flagged {
		read := false
		for _, rb := range m.Runbooks {
			read = read || slices.Contains(rb.Skills, skill)
		}
		if !read {
			add("skill %s holds disable-model-invocation: true and is in no runbook's skills, so no run reads it", skill)
		}
	}
	switch {
	case flagged && !held:
		add("skill %s holds disable-model-invocation: true and no agents/openai.yaml, so each Codex run lists it", skill)
	case !flagged && held:
		add("skill %s holds agents/openai.yaml and not disable-model-invocation: true, so Codex hides what Claude Code lists", skill)
	case flagged && string(codex) != codexPolicy:
		add("skill %s holds an agents/openai.yaml other than %q", skill, codexPolicy)
	}
	return out
}

var (
	packPath = regexp.MustCompile(`\.\./(loop-[a-z0-9-]+)((?:/[A-Za-z0-9._-]+)*)/?`)
	asksAll  = regexp.MustCompile(`(?i)\b(?:ask|wait for|check with)\s+(?:the\s+|a\s+)?(?:operator|person|user|owner|human)\b`)
)

// packPaths checks each ../loop-* path against the laid-out tree. A path
// is relative to the directory that receives the file, so every injected
// loop-* directory is a sibling.
func packPaths(tree fs.FS, f packFile, text string) []Finding {
	var out []Finding
	for i, line := range strings.Split(text, "\n") {
		for _, m := range packPath.FindAllString(line, -1) {
			target := path.Join(path.Dir(f.injected), strings.TrimSuffix(m, "/"))
			if _, err := fs.Stat(tree, target); err != nil {
				out = append(out, Finding{File: f.source, Line: i + 1, Kind: "path",
					Detail: fmt.Sprintf("%s names nothing in the pack", m)})
			}
		}
	}
	return out
}

// frontFields reads the fields of a file's front matter, or says why it
// has none.
func frontFields(text string) (map[string]string, string) {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, "the file opens with no front matter"
	}
	head, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return nil, "the front matter does not close"
	}
	fields := map[string]string{}
	for _, l := range strings.Split(head, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok {
			fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return fields, ""
}

func frontmatter(file, dir, text string) []Finding {
	bad := func(format string, args ...any) []Finding {
		return []Finding{{File: file, Line: 1, Kind: "frontmatter", Detail: fmt.Sprintf(format, args...)}}
	}
	fields, why := frontFields(text)
	if why != "" {
		return bad("%s: %s; it needs name: %s and a description", file, why, dir)
	}
	if fields["name"] != dir {
		return bad("front matter names %q, and the skill directory is %s", fields["name"], dir)
	}
	if fields["description"] == "" {
		return bad("the front matter of %s holds no description", dir)
	}
	return nil
}

// readTools are the harness tools an agent of the pack may hold. Bash
// runs any command, so the gate also reads each loop command the agent
// names.
var readTools = []string{"Read", "Grep", "Glob", "Bash"}

// agentPath is a path from the checkout root into the skill root. Agents
// ship only to Claude Code, and a subagent runs at the checkout root with
// no word of where its definition lives.
var (
	agentPath    = regexp.MustCompile(`\.claude/skills/(loop-[a-z0-9-]+(?:/[A-Za-z0-9._-]+)*)/?`)
	agentRelPath = regexp.MustCompile(`\.\./(?:skills/)?loop-[a-z0-9-]+(?:/[A-Za-z0-9._-]+)*/?`)
)

// agentPaths checks each path an agent names into the skill root. A path
// relative to the agent file names nothing for the subagent that reads it.
func agentPaths(tree fs.FS, file, text string) []Finding {
	var out []Finding
	for i, line := range strings.Split(text, "\n") {
		for _, m := range agentPath.FindAllStringSubmatch(line, -1) {
			if _, err := fs.Stat(tree, m[1]); err != nil {
				out = append(out, Finding{File: file, Line: i + 1, Kind: "path", Detail: fmt.Sprintf("%s names nothing in the pack", m[0])})
			}
		}
		for _, m := range agentRelPath.FindAllString(line, -1) {
			out = append(out, Finding{File: file, Line: i + 1, Kind: "path",
				Detail: fmt.Sprintf("%s names nothing from the checkout root; an agent names a skill as .claude/skills/loop-*", m)})
		}
	}
	return out
}

// agentFindings checks one agent: front matter that names it, tools that
// only read, and each loop command a read the coordinator may call. The
// coordinator dispatches each agent, so the agent's commands run with
// its token.
func (ix *index) agentFindings(file, name, text string) []Finding {
	add := func(out []Finding, line int, kind, format string, args ...any) []Finding {
		return append(out, Finding{File: file, Line: line, Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
	fields, bad := frontFields(text)
	if bad != "" {
		return add(nil, 1, "frontmatter", "%s; it needs name: %s, a description and tools", bad, name)
	}
	var out []Finding
	if fields["name"] != name {
		out = add(out, 1, "frontmatter", "front matter names %q, and the agent file is %s.md", fields["name"], name)
	}
	if fields["description"] == "" {
		out = add(out, 1, "frontmatter", "the front matter of %s holds no description", name)
	}
	if fields["tools"] == "" {
		out = add(out, 1, "agent", "%s names no tools, so it inherits every tool, and some of them write", name)
	}
	for _, t := range strings.Split(fields["tools"], ",") {
		if t = strings.TrimSpace(t); t != "" && !slices.Contains(readTools, t) {
			out = add(out, 1, "agent", "tool %s is no read; an agent of the pack holds only %s", t, strings.Join(readTools, ", "))
		}
	}
	code, _ := split(text)
	for _, s := range code {
		for _, toks := range ix.calls(s) {
			paths, _, _ := ix.resolve(toks)
			for _, p := range paths {
				v := ix.verbs[p]
				switch {
				case v == nil:
				case v.Write:
					out = add(out, s.line, "agent", "`loop %s` writes; an agent only reads, and the coordinator does the write", p)
				case v.Side != cli.SideServer || !slices.Contains(v.Callers, "coordinator") && !slices.Contains(v.Callers, "anyone"):
					out = add(out, s.line, "agent", "`loop %s` is no server read the coordinator may call, and an agent runs with its token", p)
				}
			}
		}
	}
	return out
}

func runbookFindings(file string, rb Runbook, text string) []Finding {
	var out []Finding
	for _, a := range rb.Agents {
		if !strings.Contains(text, a) {
			out = append(out, Finding{File: file, Kind: "agent", Detail: fmt.Sprintf("the runbook dispatches agent %s and never names it", a)})
		}
	}
	for _, s := range rb.Skills {
		if p := "../" + s + "/SKILL.md"; !strings.Contains(text, p) {
			out = append(out, Finding{File: file, Kind: "skill", Detail: fmt.Sprintf("the runbook reads %s and never names %s", s, p)})
		}
	}
	if rb.Policy != "" && !strings.Contains(text, PolicyPath(rb.Policy)) {
		out = append(out, Finding{File: file, Kind: "policy", Detail: fmt.Sprintf("the runbook never names its policy file %s", PolicyPath(rb.Policy))})
	}
	if rb.Attended {
		return out
	}
	_, prose := split(text)
	for _, p := range prose {
		if m := asksAll.FindString(p.text); m != "" {
			out = append(out, Finding{File: file, Line: p.line, Kind: "attended",
				Detail: fmt.Sprintf("%q: no person is present in an unattended run; decide, or escalate with a score", m)})
		}
	}
	return out
}
