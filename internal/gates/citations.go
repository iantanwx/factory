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
	"unicode"
	"unicode/utf8"

	docshape "github.com/iantanwx/loop/internal/shape"
)

var (
	// citeLine is `→ <path> [§ <heading>]`, or `→ § <heading>` for a
	// heading of the PRD itself, such as its story US-01.
	citeLine  = regexp.MustCompile(`^\s*→\s*(.*?)\s*$`)
	mdHeading = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)
	mdFence   = regexp.MustCompile("^\\s*(?:```|~~~)")
	// storyHeading is a heading that defines a story.
	storyHeading = regexp.MustCompile(`^#{1,6}\s+(US-[0-9]+)\b`)
)

// Citations checks that each PRD derives its requirements from a source
// and does not invent them. Under `## Requirements`, each top-level bullet
// carries a citation on a line beneath it:
//
//	→ <repository path> [§ <heading>]
//	→ § US-01
//
// A citation with no path names a heading of the PRD itself: a story,
// which quotes the operator's words. A citation resolves when the file
// exists and holds a heading that starts with the words given, in any
// case. Each PRD holds at least one citation. A fenced block holds
// examples, and the gate skips it.
//
// The story rules follow the server. Each PRD defines a story heading US-n,
// or `loop prd submit` refuses it. A label only in prose is no story. No
// two headings define one label. A label that a PRD on the base held stays
// in that PRD, because specs and tickets cite it.
func Citations(ctx context.Context, r *Repo) ([]Finding, error) {
	files, err := r.glob([]string{prdDir + "/*.md"})
	if err != nil {
		return nil, err
	}
	now := map[string][]string{}
	var out []Finding
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(r.Root, f))
		if err != nil {
			return nil, err
		}
		text := string(raw)
		out = append(out, citeFindings(r.Root, f, text)...)
		out = append(out, storyFindings(f, text)...)
		now[filepath.ToSlash(f)] = docshape.StoryLabels(text)
	}
	dropped, err := droppedStories(ctx, r, now)
	if err != nil {
		return nil, err
	}
	return append(out, dropped...), nil
}

// citeFindings checks the citations of one PRD.
func citeFindings(root, file, text string) []Finding {
	var out []Finding
	add := func(line int, rule, msg string) {
		out = append(out, Finding{File: file, Line: line, Rule: rule, Message: msg})
	}
	lines := unfenced(text)
	own := headings(text)
	inReq, count := false, 0
	pending, pendingText := 0, ""
	flush := func() {
		if pending > 0 {
			add(pending, "uncited_requirement", fmt.Sprintf("requirement has no → citation: %q", clip(pendingText, 70)))
		}
		pending = 0
	}
	for i, line := range lines {
		n := i + 1
		if m := mdHeading.FindStringSubmatch(line); m != nil {
			flush()
			inReq = strings.EqualFold(strings.TrimSpace(m[1]), "requirements")
			continue
		}
		if !inReq {
			continue
		}
		if m := citeLine.FindStringSubmatch(line); m != nil {
			pending = 0
			count++
			cited, want, hasHeading := parseCite(m[1])
			hs := own
			if cited != "" {
				target := filepath.Join(root, filepath.FromSlash(cited))
				if rel, err := filepath.Rel(root, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					add(n, "missing_file", cited+" is outside the repository")
					continue
				}
				raw, err := os.ReadFile(target)
				if err != nil {
					add(n, "missing_file", "no such file: "+cited)
					continue
				}
				hs = headings(string(raw))
			}
			if hasHeading && !headingMatches(hs, want) {
				src := cited
				if src == "" {
					src = "this PRD"
				}
				add(n, "missing_heading", fmt.Sprintf("%s has no heading starting %q", src, want))
			}
			continue
		}
		if body, ok := strings.CutPrefix(line, "- "); ok && !strings.HasPrefix(body, "→") {
			flush()
			pending, pendingText = n, body
		}
	}
	flush()
	if count == 0 {
		add(0, "no_citations", "no → citations found under ## Requirements")
	}
	return out
}

// parseCite splits what follows the arrow into the path and the heading.
// The path is empty for a heading of the PRD itself.
func parseCite(s string) (cited, heading string, hasHeading bool) {
	if h, ok := strings.CutPrefix(s, "§"); ok {
		return "", strings.TrimSpace(h), true
	}
	cited, rest, _ := strings.Cut(s, " ")
	if h, ok := strings.CutPrefix(strings.TrimSpace(rest), "§"); ok {
		return cited, strings.TrimSpace(h), true
	}
	return cited, "", false
}

// unfenced splits text into lines, with each line of a fenced block
// blanked. A citation or a heading in a fence is an example, not a source.
func unfenced(text string) []string {
	lines := strings.Split(text, "\n")
	in := false
	for i, l := range lines {
		if mdFence.MatchString(l) {
			in = !in
			lines[i] = ""
		} else if in {
			lines[i] = ""
		}
	}
	return lines
}

// headings lists the text of each Markdown heading, in lower case.
func headings(text string) []string {
	var out []string
	for _, line := range unfenced(text) {
		if m := mdHeading.FindStringSubmatch(line); m != nil {
			out = append(out, strings.ToLower(m[1]))
		}
	}
	return out
}

// headingMatches reports whether a heading starts with want, in any case,
// and ends a word there. A citation names a heading by its opening words,
// and a source heading often trails a gloss. The word end keeps §US-1
// off US-10.
func headingMatches(hs []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	return want != "" && slices.ContainsFunc(hs, func(h string) bool {
		rest, ok := strings.CutPrefix(h, want)
		if !ok || rest == "" {
			return ok
		}
		r, _ := utf8.DecodeRuneInString(rest)
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
	})
}

// storyFindings checks the story labels of one PRD.
func storyFindings(file, text string) []Finding {
	if len(docshape.StoryLabels(text)) == 0 {
		return []Finding{{File: file, Rule: "no_stories", Message: "the PRD defines no story heading; head each story US-01, US-02 and on, or `loop prd submit` refuses it"}}
	}
	var out []Finding
	first := map[string]int{}
	for i, line := range unfenced(text) {
		m := storyHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if at, ok := first[m[1]]; ok {
			out = append(out, Finding{File: file, Line: i + 1, Rule: "duplicate_story", Message: fmt.Sprintf("%s is already the story at line %d; a label names one story", m[1], at)})
			continue
		}
		first[m[1]] = i + 1
	}
	return out
}

// droppedStories compares each PRD on the merge base of HEAD and Base with
// the same file now. A PRD on the base is signed off, and the specs and
// tickets of its epic cite its labels, so a label it held never goes
// away. The epic records its PRD by path, so a PRD that moved or went away
// drops every label it held. A checkout with no merge base has no earlier
// state to compare.
func droppedStories(ctx context.Context, r *Repo, now map[string][]string) ([]Finding, error) {
	mb, err := r.Git(ctx, "merge-base", "HEAD", r.Base)
	if err != nil {
		// No merge base: there is no earlier state to compare.
		return nil, nil
	}
	base := strings.TrimSpace(mb)
	list, err := r.Git(ctx, "ls-tree", "-r", "--name-only", base, "--", prdDir)
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, f := range strings.Fields(list) {
		if path.Dir(f) != prdDir || !strings.HasSuffix(f, ".md") {
			continue
		}
		text, err := r.Git(ctx, "show", base+":"+f)
		if err != nil {
			return nil, err
		}
		held, ok := now[f]
		for _, l := range docshape.StoryLabels(text) {
			switch {
			case !ok:
				findings = append(findings, Finding{File: f, Rule: "dropped_story", Message: fmt.Sprintf("%s was in %s at %s, and the file is gone; its epic reads its PRD by this path", l, f, short(base))})
			case !slices.Contains(held, l):
				findings = append(findings, Finding{File: f, Rule: "dropped_story", Message: fmt.Sprintf("%s was in this PRD at %s, and is gone; specs and tickets cite it", l, short(base))})
			}
		}
	}
	return findings, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
