package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/iantanwx/loop/internal/hexid"
)

// BaseReader reads a project's base branch. The GitHub service supplies
// the reader over the project's bare mirror. Admission calls it only outside the store's
// write lock and outside the machine's item lock.
type BaseReader interface {
	// Files returns a content id, such as a git blob id, for every file on
	// the project's base branch whose path starts with one of prefixes. A
	// prefix that matches no file adds nothing.
	Files(ctx context.Context, project string, prefixes []string) (map[string]string, error)
}

// Task is what one work item's retry identity covers.
type Task struct {
	Title string
	Body  string
	// Head is the head of the item's open pull request. Only a code
	// review reads it, and it is empty for every other stage.
	Head string
}

// inputsHash is the retry identity of R10: the item's title and body, the
// content of its declared paths and cited documents on the base, and the
// loop release version. A code review adds the head of the item's pull
// request, so a push offers a held review again (admission-input.ts,
// alpha). Nothing else enters it, so a change of harness or model, or a
// merge that changes no declared path, keeps it.
func inputsHash(t Task, files map[string]string, release string) string {
	pairs := make([][2]string, 0, len(files))
	for p, id := range files {
		pairs = append(pairs, [2]string{p, id})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	raw, _ := json.Marshal([]any{t.Title, t.Body, pairs, release, t.Head})
	return hexid.SHA256(raw)
}

// computeInputs reads the base for the task's declared paths and returns
// its retry identity. It makes an outside read, so no caller holds a lock.
func computeInputs(ctx context.Context, base BaseReader, project string, t Task, release string) (string, error) {
	paths := DeclaredPaths(t.Body)
	files := map[string]string{}
	if len(paths) > 0 {
		if base == nil {
			return "", fmt.Errorf("admission: %d declared paths and no base reader", len(paths))
		}
		var err error
		if files, err = base.Files(ctx, project, paths); err != nil {
			return "", fmt.Errorf("admission: read the declared paths on the base: %w", err)
		}
	}
	return inputsHash(t, files, release), nil
}

var (
	backtickRe = regexp.MustCompile("`([^`]+)`")
	labelRe    = regexp.MustCompile(`^(?:\*\*(May change|Must not change):\*\*|(May change|Must not change):)\s*(.*)$`)
	bulletRe   = regexp.MustCompile(`^\s*[*-]\s`)
	// citedRe finds a relative Markdown path with at least one directory.
	// The character before it must not continue a word, a path or a URL.
	citedRe = regexp.MustCompile("(?:^|[\\s`(\\[<\"'])((?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+\\.md)\\b")
)

// DeclaredPaths returns the path prefixes the retry identity reads on the
// base: every path in the "May change" list of the body's "## Files"
// section, and every relative Markdown path the body cites. A glob is cut
// at its first glob character, so the prefix covers what the glob covers.
// Ported in part from footprint in scripts/sdlc/ticket-files.ts (alpha).
func DeclaredPaths(body string) []string {
	var out []string
	add := func(p string) {
		p = strings.TrimPrefix(p, "./")
		if i := strings.IndexAny(p, "*?["); i >= 0 {
			p = p[:i]
		}
		if p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, line := range mayChange(body) {
		for _, m := range backtickRe.FindAllStringSubmatch(line, -1) {
			tok := m[1]
			if strings.ContainsAny(tok, " \t") || !strings.ContainsAny(tok, "/.*") {
				continue
			}
			for _, p := range expandBraces(tok) {
				add(p)
			}
		}
	}
	for _, m := range citedRe.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	sort.Strings(out)
	return out
}

// The labels of the two lists in a body's "## Files" section.
const (
	MayChange     = "May change"
	MustNotChange = "Must not change"
)

// mayChange returns the bullet lines of the "May change" list inside the
// "## Files" section. A label may hold its first entry on its own line.
func mayChange(body string) []string {
	lines := strings.Split(body, "\n")
	var out []string
	for _, i := range FileList(lines, MayChange) {
		out = append(out, lines[i])
	}
	return out
}

// FileList returns the indexes of the lines of one list, MayChange or
// MustNotChange, inside the "## Files" section of a body split into lines.
// A label that holds its first entry on its own line is in the list. The
// triage run's scope release reads both lists with it, so the release and the
// retry identity read one list the same way.
func FileList(lines []string, label string) []int {
	start := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "## Files" })
	if start < 0 {
		return nil
	}
	var out []int
	in := false
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "## ") {
			break
		}
		if m := labelRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			in = m[1] == label || m[2] == label
			if in && m[3] != "" {
				out = append(out, i)
			}
			continue
		}
		if in && (bulletRe.MatchString(line) || strings.TrimSpace(line) != "" && len(out) > 0) {
			out = append(out, i)
		} else if in && strings.TrimSpace(line) == "" && len(out) > 0 {
			in = false
		}
	}
	return out
}

// expandBraces expands the first brace group of a pattern, then the rest.
func expandBraces(p string) []string {
	open := strings.IndexByte(p, '{')
	if open < 0 {
		return []string{p}
	}
	depth, last := 0, open
	var parts []string
	for i := open; i < len(p); i++ {
		switch p[i] {
		case '{':
			depth++
		case ',':
			if depth == 1 {
				parts = append(parts, p[last+1:i])
				last = i
			}
		case '}':
			depth--
			if depth == 0 {
				parts = append(parts, p[last+1:i])
				var out []string
				for _, part := range parts {
					out = append(out, expandBraces(p[:open]+part+p[i+1:])...)
				}
				return out
			}
		}
	}
	return []string{p}
}
