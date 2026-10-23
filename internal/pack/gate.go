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

func (ix *index) command(toks []string) [][2]string {
	if len(toks) == 0 || placeholder(toks[0]) || strings.HasPrefix(toks[0], "-") {
		return nil
	}
	var paths []string
	rest := toks[1:]
	switch {
	case ix.verbs[toks[0]] != nil && ix.verbs[toks[0]].TopLevel:
		paths = []string{toks[0]}
	case ix.nouns[toks[0]]:
		if len(toks) < 2 || placeholder(toks[1]) || strings.HasPrefix(toks[1], "-") {
			return nil // a mention of the noun, or of its verbs in general
		}
		for _, v := range strings.Split(toks[1], "|") {
			paths = append(paths, toks[0]+" "+v)
		}
		rest = toks[2:]
	default:
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
	files, err := markdown(src)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		raw, err := fs.ReadFile(src, f.source)
		if err != nil {
			return nil, err
		}
		text := string(raw)
		out = append(out, ix.text(f.source, text)...)
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
// skill root receives it, and the stage of a runbook.
type packFile struct {
	source, injected, stage string
}

func markdown(src fs.FS) ([]packFile, error) {
	var out []packFile
	runbooks, err := fs.Glob(src, "runbooks/*.md")
	if err != nil {
		return nil, err
	}
	for _, r := range runbooks {
		stage := strings.TrimSuffix(path.Base(r), ".md")
		out = append(out, packFile{r, Injected(stage) + "/SKILL.md", stage})
	}
	err = fs.WalkDir(src, "skills", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".md") {
			return err
		}
		out = append(out, packFile{name, strings.TrimPrefix(name, "skills/"), ""})
		return nil
	})
	return out, err
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

func frontmatter(file, dir, text string) []Finding {
	bad := func(format string, args ...any) []Finding {
		return []Finding{{File: file, Line: 1, Kind: "frontmatter", Detail: fmt.Sprintf(format, args...)}}
	}
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return bad("%s opens with no front matter; it needs name: %s and a description", file, dir)
	}
	head, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return bad("the front matter of %s does not close", file)
	}
	fields := map[string]string{}
	for _, l := range strings.Split(head, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok {
			fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if fields["name"] != dir {
		return bad("front matter names %q, and the skill directory is %s", fields["name"], dir)
	}
	if fields["description"] == "" {
		return bad("the front matter of %s holds no description", dir)
	}
	return nil
}

func runbookFindings(file string, rb Runbook, text string) []Finding {
	var out []Finding
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
