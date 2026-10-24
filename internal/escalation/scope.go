package escalation

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/iantanwx/loop/internal/admission"
)

// The one edit a fixer may make to a ticket: a loop route releases one path
// out of the body's "Must not change" list and into "May change". It ports
// scripts/sdlc/fixer-scope.ts (alpha).
//
// A note alone does not work, and alpha measured it on TIC-70. A person
// widened that ticket's scope in a note and left the body alone. The
// implementer read the body, obeyed the list, and escalated again on the
// same finding 23 minutes later. It was right to: the implementer and the
// code-reviewer runbooks both read the body's list as the boundary. So the
// body is the one place scope lives, and this is how the body changes.
//
// It widens, and it never narrows. No function here adds an entry to
// "Must not change". A wrong widening costs a diff that the code-reviewer
// reads before anything merges. A wrong narrowing costs a ticket nobody can
// do.

// NeverReleased holds the paths no fixer releases, whatever the judges
// scored. Each one is a decision that belongs to a person, and each one is
// already a rule elsewhere. `.loop/` is the project's loop config: its gates,
// its policies and its secret names, so releasing it would let the loop
// widen what it may do next. `.github/` is CI, the gate the loop is measured
// by. `.gitmodules` and `.gitattributes` are UNSAFE_PATH, which only the
// operator pushes. `AGENTS.md` and `CLAUDE.md` are the repository's own
// rules for agents. A person signs a PRD off by its merge, and an ADR is a
// preference by the rubric.
//
// The list refuses a path and never grants one: a path absent from it still
// needs a panel that routes to the loop, and an entry in the ticket's own
// list.
var NeverReleased = []string{
	".loop/",
	".github/",
	".gitmodules",
	".gitattributes",
	"AGENTS.md",
	"CLAUDE.md",
	"docs/prd/",
	"docs/adr/",
}

// Release is what the fixer asks for, from the three flags of
// `escalation route`.
type Release struct {
	// Path is the narrower path the ticket may now change.
	Path string
	// Forbids is the entry of "Must not change" that covers Path, spelled
	// exactly as the ticket spells it.
	Forbids string
	// Condition is what the implementer may do at Path, and nothing wider.
	Condition string
	// Escalation is the escalation the release answers, for the sentence
	// the edit writes into the body.
	Escalation int64
}

// ReleaseRefusal says why a release writes nothing. Rule names the case.
type ReleaseRefusal struct {
	Rule string
	Hint string
}

// RuleNeverReleased is the refusal that sends the escalation to a person
// rather than back to the fixer.
const RuleNeverReleased = "never_released"

var (
	tickRe   = regexp.MustCompile("`([^`]+)`")
	markerRe = regexp.MustCompile(`^\s*[*-]\s`)
)

// CleanPath returns a path as the loop reads it: with no leading "./",
// no empty or "." segment, and each ".." resolved, so `lib/../.github/x`
// is `.github/x`. A trailing slash stays, because it makes a directory. A
// path that is absolute, or that climbs out of the checkout, returns "".
// The guards below compare clean paths, or a spelling that climbs out of
// a forbidden glob passes them and lands on a path they refuse.
func CleanPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || strings.HasPrefix(p, "/") {
		return ""
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return ""
	}
	if strings.HasSuffix(p, "/") {
		c += "/"
	}
	return c
}

// plain reports whether text holds no backtick and no control character,
// such as a line break. The loop reads every backticked token of "May
// change" as a path, and a line break starts a bullet of its own. Text
// that holds either one writes a path the guards never read.
func plain(text string) bool {
	return !strings.ContainsFunc(text, func(r rune) bool { return r == '`' || unicode.IsControl(r) })
}

// prefixOf is the literal prefix of a glob: everything before its first
// glob character.
func prefixOf(glob string) string {
	if i := strings.IndexAny(glob, "*?[{"); i >= 0 {
		return glob[:i]
	}
	return glob
}

// directory reports whether an entry names more than itself: a glob, or a
// directory that ends in a slash.
func directory(entry string) bool {
	return prefixOf(entry) != entry || strings.HasSuffix(entry, "/")
}

// covers reports whether the forbidden entry covers the released path. The
// literal prefix answers it: `lib/**` covers `lib/parse.go`, and
// `app/**` does not. An entry that names one file covers that file alone.
func covers(forbids, path string) bool {
	if !directory(forbids) {
		return forbids == path
	}
	return strings.HasPrefix(path, prefixOf(forbids)) && path != forbids
}

// neverReleased returns the entry of NeverReleased that path reaches, or
// "". A glob that covers an entry reaches it too, so `**` releases nothing.
func neverReleased(path string) string {
	for _, p := range NeverReleased {
		if strings.HasPrefix(path, p) || path == strings.TrimSuffix(p, "/") ||
			directory(path) && strings.HasPrefix(p, prefixOf(path)) {
			return p
		}
	}
	return ""
}

func backticked(line string) []string {
	var out []string
	for _, m := range tickRe.FindAllStringSubmatch(line, -1) {
		out = append(out, m[1])
	}
	return out
}

// names reports whether a line names the clean path in backticks, in any
// spelling.
func names(line, clean string) bool {
	return slices.ContainsFunc(backticked(line), func(tok string) bool { return CleanPath(tok) == clean })
}

// releasedSentence is the sentence the released bullet ends with. It ends
// in a period, so escalation 7 is not escalation 70.
func releasedSentence(escalation int64) string {
	return fmt.Sprintf("Released from %q by the fixer for escalation %d.", admission.MustNotChange, escalation)
}

// ReleasedBy reports whether the body's "May change" list holds the
// bullet that escalation's release of path wrote. A route that edited the
// body, and crashed before it recorded the route, finds its own edit by
// it. A release of the same path by another escalation is not this one.
func ReleasedBy(body, path string, escalation int64) bool {
	clean := CleanPath(path)
	if clean == "" {
		return false
	}
	lines := strings.Split(body, "\n")
	allowed := admission.FileList(lines, admission.MayChange)
	return slices.ContainsFunc(allowed, func(i int) bool {
		bullet := strings.Join(lines[i:bulletEnd(lines, allowed, i)+1], " ")
		return markerRe.MatchString(lines[i]) && names(lines[i], clean) &&
			strings.Contains(bullet, releasedSentence(escalation))
	})
}

// bulletEnd returns the last line of the bullet that starts at first, so
// the sentence lands after a wrapped bullet.
func bulletEnd(lines []string, within []int, first int) int {
	last := first
	for _, i := range within {
		if i <= first {
			continue
		}
		if markerRe.MatchString(lines[i]) {
			break
		}
		last = i
	}
	return last
}

// ReleasePath releases one path out of a body's "Must not change" list, or
// says why it cannot. It makes two edits, both or neither: the forbidden
// bullet gains a sentence that names the exception, and "May change" gains
// a bullet for the path. Either half alone leaves the two lists
// disagreeing, and the implementer and the code-reviewer read one list
// each. Nothing outside the "## Files" section changes.
func ReleasePath(body string, r Release) (string, *ReleaseRefusal) {
	path, forbids, condition := strings.TrimSpace(r.Path), strings.TrimSpace(r.Forbids), strings.TrimSpace(r.Condition)
	if condition == "" {
		return "", &ReleaseRefusal{"no_condition", "--condition names what the implementer may do at the path; " +
			"a released path with no condition is the whole entry"}
	}
	if !plain(path) || strings.ContainsFunc(path, unicode.IsSpace) {
		return "", &ReleaseRefusal{"path_not_plain", fmt.Sprintf("%q holds a backtick, a space or a line break; "+
			"name one path, and nothing else", path)}
	}
	if !plain(condition) {
		return "", &ReleaseRefusal{"condition_not_plain", "--condition holds a backtick or a line break, and the loop " +
			"reads each backticked word of \"May change\" as a path; state the condition in plain words on one line"}
	}
	clean := CleanPath(path)
	if clean == "" {
		return "", &ReleaseRefusal{"outside_checkout", fmt.Sprintf("`%s` is absolute or climbs out of the checkout; "+
			"name the path relative to the repository root", path)}
	}
	path = clean
	if floor := neverReleased(path); floor != "" {
		return "", &ReleaseRefusal{RuleNeverReleased, fmt.Sprintf("no fixer releases `%s`, because it reaches `%s`, "+
			"which a person alone may widen", path, floor)}
	}
	if !covers(CleanPath(forbids), path) {
		return "", &ReleaseRefusal{"not_covered", fmt.Sprintf("`%s` does not cover `%s`; name the entry of %q that "+
			"forbids the path, as the ticket spells it", forbids, path, admission.MustNotChange)}
	}
	lines := strings.Split(body, "\n")
	forbidden := admission.FileList(lines, admission.MustNotChange)
	allowed := admission.FileList(lines, admission.MayChange)
	if len(forbidden) == 0 || len(allowed) == 0 {
		return "", &ReleaseRefusal{"no_lists", fmt.Sprintf("the body's \"## Files\" section needs both lists, and it holds "+
			"%d %q lines and %d %q lines", len(allowed), admission.MayChange, len(forbidden), admission.MustNotChange)}
	}
	if slices.ContainsFunc(allowed, func(i int) bool { return names(lines[i], path) }) {
		return "", &ReleaseRefusal{"already_released", fmt.Sprintf("`%s` is already in %q, so this release writes "+
			"nothing; an earlier round made it, and the escalation has another cause", path, admission.MayChange)}
	}
	at := slices.IndexFunc(forbidden, func(i int) bool { return slices.Contains(backticked(lines[i]), forbids) })
	if at < 0 {
		return "", &ReleaseRefusal{"no_entry", fmt.Sprintf("%q holds no entry spelled `%s`; copy the entry out of "+
			"the ticket exactly", admission.MustNotChange, forbids)}
	}
	match := forbidden[at]
	end := bulletEnd(lines, forbidden, match)
	last := allowed[len(allowed)-1]
	marker := "- "
	if m := markerRe.FindString(lines[match]); m != "" {
		marker = m
	}
	out := slices.Clone(lines)
	// The new bullet does not name the forbidden entry in backticks: the
	// retry identity reads every backticked path of "May change".
	out[last] = fmt.Sprintf("%s\n%s`%s`: %s %s", lines[last], marker, path, condition, releasedSentence(r.Escalation))
	out[end] = fmt.Sprintf("%s The fixer released `%s` into %q for escalation %d, and nothing else here.",
		lines[end], path, admission.MayChange, r.Escalation)
	released := strings.Join(out, "\n")
	if wider := widens(body, released, path); wider != "" {
		return "", &ReleaseRefusal{"reads_wider", fmt.Sprintf("after the release the loop would also read `%s` as a "+
			"path the ticket may change; name no path in --condition but the released one", wider)}
	}
	return released, nil
}

// widens returns a path that the loop reads in the released body and not
// in the old one, other than the released path, or "". It checks the edit
// with the loop's own reader, so the guards above and that reader agree on
// what the release widened.
func widens(old, released, path string) string {
	before := admission.DeclaredPaths(old)
	want := admission.DeclaredPaths("## Files\n\n" + admission.MayChange + ":\n- `" + path + "`\n")
	for _, p := range admission.DeclaredPaths(released) {
		if !slices.Contains(before, p) && !slices.Contains(want, p) {
			return p
		}
	}
	return ""
}
