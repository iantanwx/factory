package gates

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/release"
)

// Runbooks checks that each agent-facing document of the project describes
// the system that exists. It ports runbook-check.ts (alpha). An agent that
// runs unattended cannot ask, so a stale command or path in a document is a
// defect. It checks:
//
//   - each `loop` command against the schema of this binary: verb, flag,
//     enum value, and a refusal code on a line that runs loop
//   - each `loop gates --only <name>` names a loop gate or a config gate
//   - each `make` target and package script that a command names is in
//     the root Makefile or the root package.json
//   - each repository path that a code span or a fenced line names is
//     tracked by git, so an agent's worktree has it
//   - each stated count of runbooks or policies
//   - each claim about what an agent reads agrees with every other claim
//   - no sentence holds back a production repair to keep evidence
//
// It does not check whether a prose instruction names a command behind it.
// "Open a pull request" names none, so nothing here can compare it.
func Runbooks(ctx context.Context, r *Repo) ([]Finding, error) {
	files, tracked, err := r.listFiles(ctx)
	if err != nil {
		return nil, err
	}
	docs := matchFiles(r.Root, files, runbookGlobs)
	reg := cli.Surface()
	for _, c := range release.Refusals() {
		if err := reg.RegisterRefusal(c); err != nil {
			return nil, err
		}
	}
	gateNames := map[string]bool{}
	for _, g := range List(r.Config) {
		gateNames[g.Name] = true
	}
	counts := map[string]int{}
	for _, n := range countNouns {
		counts[n.noun] = len(matchFiles(r.Root, files, []string{n.glob}))
	}
	agents := agentForms(docs)
	proj, err := readProject(r.Root)
	if err != nil {
		return nil, err
	}
	var out []Finding
	var claims []agentClaim
	for _, file := range docs {
		raw, err := os.ReadFile(filepath.Join(r.Root, file))
		if err != nil {
			return nil, err
		}
		text := string(raw)
		shell := shellOnly(text)
		cmds, err := pack.CheckCommands(file, shell, reg)
		if err != nil {
			return nil, err
		}
		for _, f := range cmds {
			out = append(out, Finding{File: file, Line: f.Line, Rule: f.Kind, Message: f.Detail})
		}
		out = append(out, onlyFindings(file, shell, gateNames)...)
		out = append(out, projectFindings(file, shell, proj)...)
		out = append(out, pathFindings(file, text, tracked)...)
		out = append(out, countFindings(file, text, counts)...)
		claims = append(claims, claimsIn(file, text, agents)...)
	}
	out = append(out, claimFindings(claims)...)
	for _, file := range matchFiles(r.Root, files, append(slices.Clone(runbookGlobs), repairGlobs...)) {
		raw, err := os.ReadFile(filepath.Join(r.Root, file))
		if err != nil {
			return nil, err
		}
		out = append(out, repairFindings(file, string(raw))...)
	}
	slices.SortStableFunc(out, func(a, b Finding) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		return a.Line - b.Line
	})
	return out, nil
}

// runbookGlobs are the agent-facing documents. An AGENTS.md at any depth
// joins them: it briefs every agent that works below it.
var runbookGlobs = []string{
	"AGENTS.md", "CLAUDE.md", "CODING_STANDARDS.md", ".loop/policy/*.md",
	"docs/agents/*.md", "docs/commands/*.md", "docs/rules/*.md", "docs/templates/*.md",
	"skills/*/SKILL.md", ".claude/agents/*.md", "**/AGENTS.md",
}

// listFiles lists the tracked and new files of the checkout, and the set
// of tracked paths with every directory on the way to one. A path check
// reads git and not the disk: a worktree holds tracked files only.
func (r *Repo) listFiles(ctx context.Context) ([]string, map[string]bool, error) {
	cached, err := r.Git(ctx, "ls-files", "-z")
	if err != nil {
		return nil, nil, err
	}
	others, err := r.Git(ctx, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, nil, err
	}
	tracked := map[string]bool{}
	var files []string
	for _, f := range strings.Split(cached, "\x00") {
		if f == "" {
			continue
		}
		files = append(files, f)
		for p := f; p != "."; p = path.Dir(p) {
			tracked[p] = true
		}
	}
	for _, f := range strings.Split(others, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return slices.Compact(files), tracked, nil
}

// matchFiles returns the files that match globs. A "**/" glob matches its
// base name at any depth. With a root, a file deleted from the working
// tree under it is left out.
func matchFiles(root string, files, globs []string) []string {
	var out []string
	for _, f := range files {
		for _, g := range globs {
			ok, _ := path.Match(g, f)
			if rest, deep := strings.CutPrefix(g, "**/"); deep {
				ok = path.Base(f) == rest
			}
			if ok {
				out = append(out, f)
				break
			}
		}
	}
	if root == "" {
		return out
	}
	return slices.DeleteFunc(out, func(f string) bool {
		_, err := os.Stat(filepath.Join(root, f))
		return err != nil
	})
}

// shellFence holds the info strings of a fenced block whose lines are
// commands. A block of Go, YAML or JSON holds no `loop` command, and a
// word such as `loop :=` there is no call.
var shellFence = map[string]bool{"": true, "bash": true, "sh": true, "shell": true, "zsh": true, "console": true, "text": true, "txt": true}

var fenceOpen = regexp.MustCompile("^\\s*(```|~~~)\\s*([A-Za-z0-9_+-]*)")

// shellOnly blanks each line of a fenced block that is not a shell block.
// It keeps every fence and line, so each line number holds.
func shellOnly(text string) string {
	lines := strings.Split(text, "\n")
	in, skip := false, false
	for i, l := range lines {
		if m := fenceOpen.FindStringSubmatch(l); m != nil {
			if !in {
				skip = !shellFence[strings.ToLower(m[2])]
			}
			in = !in
			continue
		}
		if in && skip {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

var (
	gatesCall = regexp.MustCompile("(?:^|[\\s(|&;`])loop\\s+gates\\b([^|;&`]*)")
	onlyFlag  = regexp.MustCompile(`--only(?:=|\s+)["']?([^\s"']+)`)
	codeSpans = regexp.MustCompile("`([^`]*)`")
)

// onlyFindings checks that each `loop gates --only <name>` names a loop
// gate or a gate of the project config.
func onlyFindings(file, text string, names map[string]bool) []Finding {
	var out []Finding
	forEachCode(text, func(line int, code string) {
		for _, call := range gatesCall.FindAllStringSubmatch(code, -1) {
			m := onlyFlag.FindStringSubmatch(call[1])
			if m == nil || strings.ContainsAny(m[1], "<>${}") {
				continue
			}
			if !names[m[1]] {
				out = append(out, Finding{File: file, Line: line, Rule: "gate",
					Message: fmt.Sprintf("`loop gates --only %s` names no loop gate and no gate of .loop/config.yaml", m[1])})
			}
		}
	})
	return out
}

// forEachCode calls fn with each fenced line and each inline code span.
func forEachCode(text string, fn func(line int, code string)) {
	in := false
	for i, l := range strings.Split(text, "\n") {
		if fenceOpen.MatchString(l) {
			in = !in
			continue
		}
		if in {
			fn(i+1, l)
			continue
		}
		for _, m := range codeSpans.FindAllStringSubmatch(l, -1) {
			fn(i+1, m[1])
		}
	}
}

// pathRoots are the directories that a repository path can start with. A
// path elsewhere, such as a URL or a Go import path, is no claim.
const pathRoots = `docs|\.loop|scripts|skills|packages|apps|cmd|internal|pkg|tools`

var (
	spanPath   = regexp.MustCompile("`(?:\\./)?((?:" + pathRoots + ")/[^`]*)`")
	barePath   = regexp.MustCompile(`(?:^|[^A-Za-z0-9_./~-])(?:\./)?((?:` + pathRoots + `)/[A-Za-z0-9._<>*{}$/-]*)`)
	lineSuffix = regexp.MustCompile(`:\d.*$`)
)

// runFiles are the paths that the loop writes into a run's checkout and
// excludes from git. A document may name them.
var runFiles = []string{brief.BriefJSON, brief.BriefMD, brief.PromptFile, brief.TokenFile, brief.PendingFile, brief.PendingLock}

// pathRef is one repository path that a document names.
type pathRef struct {
	line int
	ref  string
}

// pathsIn returns each repository path that a document names: in a code
// span anywhere, and bare on a fenced line. A path in plain prose is no
// promise. Each is returned once per line.
func pathsIn(text string) []pathRef {
	var out []pathRef
	in := false
	for i, l := range strings.Split(text, "\n") {
		if fenceOpen.MatchString(l) {
			in = !in
			continue
		}
		var refs []string
		for _, m := range spanPath.FindAllStringSubmatch(l, -1) {
			refs = append(refs, m[1])
		}
		if in {
			for _, m := range barePath.FindAllStringSubmatch(l, -1) {
				refs = append(refs, m[1])
			}
		}
		slices.Sort(refs)
		for _, r := range slices.Compact(refs) {
			out = append(out, pathRef{i + 1, r})
		}
	}
	return out
}

// concreteOf cuts a path down to what must exist: the part before the
// first space, and before the first segment that holds a placeholder or a
// glob. A line number or an anchor after the path is no part of it.
func concreteOf(ref string) string {
	ref, _, _ = strings.Cut(strings.TrimSpace(ref), " ")
	ref, _, _ = strings.Cut(ref, "\t")
	ref, _, _ = strings.Cut(ref, "#")
	ref = lineSuffix.ReplaceAllString(ref, "")
	var keep []string
	for _, p := range strings.Split(ref, "/") {
		if strings.ContainsAny(p, "<>*${}") || strings.Contains(p, "...") {
			break
		}
		keep = append(keep, p)
	}
	c := strings.TrimRight(strings.Join(keep, "/"), "/.,;:")
	if strings.HasSuffix(c, ")") && !strings.Contains(path.Base(c), "(") {
		c = strings.TrimRight(strings.TrimSuffix(c, ")"), "/.,;:")
	}
	return c
}

// pathFindings reports each named path that git does not track. An ADR is
// cited by its number, so docs/adr/0009 matches docs/adr/0009-<title>.md.
func pathFindings(file, text string, tracked map[string]bool) []Finding {
	var out []Finding
	for _, p := range pathsIn(text) {
		c := concreteOf(p.ref)
		if !strings.Contains(c, "/") {
			continue // a bare root, such as docs/*.md cut at its glob
		}
		if tracked[c] || slices.Contains(runFiles, c) || numbered(c, tracked) {
			continue
		}
		out = append(out, Finding{File: file, Line: p.line, Rule: "path",
			Message: fmt.Sprintf("%q is not tracked by git, so an agent's worktree will not have it", p.ref)})
	}
	return out
}

// numbered reports a path such as docs/adr/0009 that is the numbered
// prefix of a tracked file. The hyphen keeps the match narrow.
func numbered(c string, tracked map[string]bool) bool {
	for t := range tracked {
		if strings.HasPrefix(t, c+"-") {
			return true
		}
	}
	return false
}
