package escalation

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

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
	if floor := neverReleased(path); floor != "" {
		return "", &ReleaseRefusal{RuleNeverReleased, fmt.Sprintf("no fixer releases `%s`, because it reaches `%s`, "+
			"which a person alone may widen", path, floor)}
	}
	if !covers(forbids, path) {
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
	if slices.ContainsFunc(allowed, func(i int) bool { return slices.Contains(backticked(lines[i]), path) }) {
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
	out[last] = fmt.Sprintf("%s\n%s`%s`: %s Released from %q by the fixer for escalation %d.",
		lines[last], marker, path, condition, admission.MustNotChange, r.Escalation)
	out[end] = fmt.Sprintf("%s The fixer released `%s` into %q for escalation %d, and nothing else here.",
		lines[end], path, admission.MayChange, r.Escalation)
	return strings.Join(out, "\n"), nil
}
