package gates

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
)

// Standards catches the silencing of a fault rather than the fault: when an
// agent writes the code and nobody reads the diff, the cheapest fix for a
// red gate is a suppression, and a suppressed error looks like no error.
//
//	escape_hatch        a type-checker escape, such as a TypeScript ignore
//	                    comment. Forbidden outright.
//	new_suppression     more lint suppressions of a rule in a file than
//	                    .loop/suppressions.json allows.
//	stale_baseline      fewer than it allows, so the baseline only ratchets
//	                    down and a removed suppression leaves no slack.
//	focused_test        a focused test, which runs one case and reports green.
//	unreachable_commit  a document cites a commit id that Base cannot reach.
//
// The needles live in standards.json, which the scan never reads, so this
// file and its test do not match their own patterns.
func Standards(ctx context.Context, r *Repo) ([]Finding, error) {
	files, err := r.trackedFiles(ctx)
	if err != nil {
		return nil, err
	}
	langs, err := loadNeedles()
	if err != nil {
		return nil, err
	}
	var findings []Finding
	found := Counts{}
	for _, f := range files {
		lang := langFor(langs, f)
		if lang == nil {
			continue
		}
		text, ok, err := r.readFile(f)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		hits, counts := lang.scan(f, text)
		findings = append(findings, hits...)
		for k, n := range counts {
			found[k] += n
		}
	}
	baseline, exists, err := readCounts(r.Root, suppressionsPath)
	if err != nil {
		return nil, err
	}
	if r.Update {
		if baseline, err = updateCounts(r.Root, suppressionsPath, found, baseline, exists); err != nil {
			return nil, err
		}
	}
	findings = append(findings, ratchet(found, baseline, suppressionsPath, "new_suppression", "suppressions")...)
	anchors, err := staleAnchors(ctx, r, files)
	if err != nil {
		return nil, err
	}
	return append(findings, anchors...), nil
}

//go:embed standards.json
var needlesJSON []byte

// suppressionsPath is the baseline of lint suppressions, keyed
// "<file>::<rule>" with the exact count allowed. Raising a count is a
// reviewable edit to a file whose purpose is to be argued with, rather than
// a line buried in a large diff.
const suppressionsPath = config.SuppressionsPath

type language struct {
	Name         string   `json:"name"`
	Extensions   []string `json:"extensions"`
	Escapes      []string `json:"escapes"`
	Suppressions []string `json:"suppressions"`
	Focus        []string `json:"focus"`

	escapes, suppressions, focus []*regexp.Regexp
}

func loadNeedles() ([]*language, error) {
	var data struct {
		Languages []*language `json:"languages"`
	}
	if err := json.Unmarshal(needlesJSON, &data); err != nil {
		return nil, fmt.Errorf("standards.json: %w", err)
	}
	for _, l := range data.Languages {
		for _, set := range []struct {
			src []string
			dst *[]*regexp.Regexp
		}{{l.Escapes, &l.escapes}, {l.Suppressions, &l.suppressions}, {l.Focus, &l.focus}} {
			for _, s := range set.src {
				re, err := regexp.Compile(s)
				if err != nil {
					return nil, fmt.Errorf("standards.json %s: %w", l.Name, err)
				}
				*set.dst = append(*set.dst, re)
			}
		}
	}
	return data.Languages, nil
}

func langFor(langs []*language, file string) *language {
	ext := path.Ext(file)
	for _, l := range langs {
		if slices.Contains(l.Extensions, ext) {
			return l
		}
	}
	return nil
}

// scan returns the escape and focus findings of one file, and its count of
// suppressions by "<file>::<rule>".
func (l *language) scan(file, text string) ([]Finding, map[string]int) {
	var findings []Finding
	counts := map[string]int{}
	for i, line := range strings.Split(text, "\n") {
		for _, re := range l.escapes {
			if m := re.FindString(line); m != "" {
				findings = append(findings, Finding{File: file, Line: i + 1, Rule: "escape_hatch", Message: m + " silences the type checker; fix the type instead"})
			}
		}
		for _, re := range l.focus {
			if re.MatchString(line) {
				findings = append(findings, Finding{File: file, Line: i + 1, Rule: "focused_test", Message: "a focused test runs one case and still reports green"})
			}
		}
		for _, re := range l.suppressions {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				for _, rule := range suppressedRules(m[1]) {
					counts[file+"::"+rule]++
				}
			}
		}
	}
	return findings, counts
}

// suppressedRules reads the rule list of one directive. A directive that
// names no rule silences every rule, so it counts as "all" rather than
// nothing.
func suppressedRules(list string) []string {
	list, _, _ = strings.Cut(list, " -- ")
	var out []string
	for _, r := range strings.Split(list, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return []string{"all"}
	}
	return out
}

// skipDirs hold code the project does not write.
var skipDirs = []string{"vendor", "node_modules", "dist", "build", "gen", "testdata"}

// trackedFiles lists the files the scan reads: tracked and new files,
// less vendored, built and generated code.
func (r *Repo) trackedFiles(ctx context.Context) ([]string, error) {
	out, err := r.Git(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f == "" || strings.Contains(path.Base(f), "_generated") ||
			slices.ContainsFunc(strings.Split(path.Dir(f), "/"), func(d string) bool { return slices.Contains(skipDirs, d) }) {
			continue
		}
		files = append(files, f)
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

// readFile reads a file of the checkout. ok is false for a tracked file
// deleted from the working tree, a submodule, or a directory.
func (r *Repo) readFile(rel string) (text string, ok bool, err error) {
	raw, err := os.ReadFile(filepath.Join(r.Root, rel))
	if err != nil {
		var pe *fs.PathError
		if errors.Is(err, fs.ErrNotExist) || errors.As(err, &pe) && pe.Op == "read" {
			return "", false, nil
		}
		return "", false, err
	}
	return string(raw), true, nil
}

// citedCommit is a commit id as a document writes one: in backticks, 7 to
// 40 lowercase hexadecimal characters.
var citedCommit = regexp.MustCompile("`([0-9a-f]{7,40})`")

// anchorDoc is a document whose cited commit ids must stay reachable.
func anchorDoc(f string) bool {
	switch f {
	case "AGENTS.md", "CLAUDE.md", "CODING_STANDARDS.md":
		return true
	}
	return path.Ext(f) == ".md" && (strings.HasPrefix(f, "docs/") || strings.HasPrefix(f, ".loop/policy/"))
}

// staleAnchors refuses a commit id that a document cites and Base cannot
// reach. A rebase gives every commit a new id, so an id copied off a branch
// can die before the merge, and an agent told to read it reaches nothing.
// Code is not read: a test holds a commit id as fixture data.
//
// A shallow clone holds no history, and git then calls every id
// unreachable, so the rule says it cannot decide and reports nothing.
func staleAnchors(ctx context.Context, r *Repo, files []string) ([]Finding, error) {
	type citation struct {
		file string
		line int
		id   string
	}
	var cited []citation
	for _, f := range files {
		if !anchorDoc(f) {
			continue
		}
		text, ok, err := r.readFile(f)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for i, line := range strings.Split(text, "\n") {
			for _, m := range citedCommit.FindAllStringSubmatch(line, -1) {
				cited = append(cited, citation{f, i + 1, m[1]})
			}
		}
	}
	if len(cited) == 0 {
		return nil, nil
	}
	if why := r.undecidable(ctx); why != "" {
		_, _ = fmt.Fprintf(r.Out, "SKIP no commit id was checked: %s\n", why)
		return nil, nil
	}
	reach := map[string]bool{}
	var findings []Finding
	for _, c := range cited {
		ok, seen := reach[c.id]
		if !seen {
			_, err := r.Git(ctx, "merge-base", "--is-ancestor", c.id, r.Base)
			ok = err == nil
			reach[c.id] = ok
		}
		if !ok {
			findings = append(findings, Finding{File: c.file, Line: c.line, Rule: "unreachable_commit", Message: fmt.Sprintf("%s cannot reach commit %s, so an agent told to read it reaches nothing. Cite the pull request instead: it survives a rebase and leads to the commits", r.Base, c.id)})
		}
	}
	return findings, nil
}

// undecidable is why the commit rule cannot run here, or "".
func (r *Repo) undecidable(ctx context.Context) string {
	if out, err := r.Git(ctx, "rev-parse", "--is-shallow-repository"); err == nil && strings.TrimSpace(out) == "true" {
		return "this is a shallow clone, so it holds no history to reach a commit through"
	}
	if _, err := r.Git(ctx, "rev-parse", "--verify", "--quiet", r.Base+"^{commit}"); err != nil {
		return r.Base + " does not resolve here, so nothing can say what it reaches"
	}
	return ""
}
