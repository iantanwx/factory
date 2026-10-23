package gates

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Commits refuses a commit on the branch whose message breaks a rule:
//
//	prefix          a Conventional Commits prefix, type(scope): with the
//	                scope optional and the type from a closed list.
//	subject_length  72 characters at most, git's own wrap width.
//	paragraphs      one body paragraph at most. A trailing footer block,
//	                such as Co-Authored-By, is not a paragraph.
//	mood            the description opens with a verb that names the edit.
//
// The gate is the enforcement and a commit-msg hook is not: a rebase
// replays a commit without the hook, and --no-verify turns it off. It
// skips a commit with more than one parent, because git or GitHub writes
// every merge message and CI checks out a merge as HEAD. A subject that
// git revert wrote is exempt from the prefix, the length and the mood.
func Commits(ctx context.Context, r *Repo) ([]Finding, error) {
	if _, err := r.Git(ctx, "rev-parse", "--verify", "--quiet", r.Base+"^{commit}"); err != nil {
		return nil, fmt.Errorf("%s does not resolve, so the gate cannot tell which commits the branch adds; fetch it and run again", r.Base)
	}
	out, err := r.Git(ctx, "log", "--format=%H%x1f%P%x1f%B%x1e", r.Base+"..HEAD")
	if err != nil {
		return nil, err
	}
	commits := parseLog(out)
	// git log reads newest first, and an author rewrites from the oldest.
	slices.Reverse(commits)
	var findings []Finding
	var offenders []string
	for _, c := range commits {
		if len(c.parents) > 1 {
			continue
		}
		fs := checkMessage(c.message)
		if len(fs) > 0 {
			offenders = append(offenders, c.sha)
		}
		for _, f := range fs {
			f.Message = fmt.Sprintf("%.8s %q: %s", c.sha, subjectOf(c.message), f.Message)
			findings = append(findings, f)
		}
	}
	if len(offenders) > 0 {
		head, err := r.Git(ctx, "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintln(r.Out, remedy(offenders, strings.TrimSpace(head), r.Base))
	}
	return findings, nil
}

type logCommit struct {
	sha     string
	parents []string
	message string
}

// parseLog reads git log --format=%H%x1f%P%x1f%B%x1e. No commit message
// holds either separator byte.
func parseLog(out string) []logCommit {
	var commits []logCommit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		sha, rest, _ := strings.Cut(rec, "\x1f")
		parents, message, _ := strings.Cut(rest, "\x1f")
		commits = append(commits, logCommit{sha: sha, parents: strings.Fields(parents), message: message})
	}
	return commits
}

// remedy names the command for the case: one amend when the only offender
// is HEAD, else a rebase. The gate runs after the commit, so it cannot
// repair one.
func remedy(offenders []string, head, base string) string {
	fix := "  git rebase -i " + base + "\n  mark each commit below as \"reword\", then write each message with -F"
	if len(offenders) == 1 && offenders[0] == head {
		fix = "  git commit --amend -F <message-file>"
	}
	return "Rewrite each message below.\n\n" + fix + "\n\n" +
		"Write the message to a file and pass -F. A shell mangles a $, a quote or a backtick in an argument."
}

// commitTypes is the Angular and commitlint set. A closed list is what
// makes the prefix worth checking: an open one accepts "Fix:" and "update:".
var commitTypes = []string{"build", "chore", "ci", "docs", "feat", "fix", "perf", "refactor", "revert", "style", "test"}

// subjectMax is git's wrap width. git log indents by four, so an
// 80-column terminal shows 76; 50 is the target a writer aims at, and the
// gate refuses the width at which a display breaks.
const subjectMax = 72

var (
	// commitPrefix leaves the scope open: a closed list would refuse a new
	// package on the day it lands. A trailing ! marks a breaking change.
	commitPrefix = regexp.MustCompile(`^(` + strings.Join(commitTypes, "|") + `)(\([a-z0-9][a-z0-9-]*\))?!?: `)
	// revertSubject is the subject git revert writes. The original passed
	// this gate, and the quoted copy is the only link back to it.
	revertSubject = regexp.MustCompile(`^Revert "`)
	// footerStart is one footer line, per Conventional Commits 1.0.0 rules
	// 8, 9, 12 and 16: a token with - for a space, then ": " or " #".
	footerStart = regexp.MustCompile(`^(BREAKING CHANGE|[A-Za-z0-9]+(-[A-Za-z0-9]+)*)(: | #)`)
)

// checkMessage lists every rule one message breaks.
func checkMessage(message string) []Finding {
	var fs []Finding
	subject := subjectOf(message)
	generated := revertSubject.MatchString(subject)
	if !generated && !commitPrefix.MatchString(subject) {
		fs = append(fs, Finding{Rule: "prefix", Message: `the subject needs a Conventional Commits prefix, "type: " or "type(scope): " with type one of ` + strings.Join(commitTypes, ", ")})
	}
	if n := len([]rune(subject)); !generated && n > subjectMax {
		fs = append(fs, Finding{Rule: "subject_length", Message: fmt.Sprintf("the subject is %d characters, and the cap is %d", n, subjectMax)})
	}
	// mood reads the description, which exists only when the prefix
	// matched, so one mistake never collects two findings.
	if m := moodFinding(subject); m != nil {
		fs = append(fs, Finding{Rule: "mood", Message: m.detail})
	}
	if n := len(bodyBlocks(message)); n > 1 {
		fs = append(fs, Finding{Rule: "paragraphs", Message: fmt.Sprintf("the body holds %d paragraphs, and the cap is 1. A footer such as Co-Authored-By is not a paragraph", n)})
	}
	return fs
}

// messageBlocks splits a message on blank lines. A run of blank lines, or
// a line of spaces, ends one block.
func messageBlocks(text string) [][]string {
	var out [][]string
	var cur []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// subjectOf is the subject as git log %s reports it: the whole first
// block joined by spaces, so a missing blank line makes one long subject.
func subjectOf(message string) string {
	b := messageBlocks(message)
	if len(b) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(b[0], " "))
}

// bodyBlocks is the blocks after the subject, less the footer blocks at
// the end. A block mid-body that opens with a token is still a paragraph,
// because the spec puts every footer after the body.
func bodyBlocks(message string) [][]string {
	all := messageBlocks(message)
	if len(all) > 0 {
		all = all[1:]
	}
	end := len(all)
	for end > 0 && footerStart.MatchString(all[end-1][0]) {
		end--
	}
	return all[:end]
}

// The mood rule is a proxy for "one predicate": an imperative clause holds
// one finite verb, so the gate reads the opening token alone. It is sound,
// not complete. Each clause refuses only a shape that cannot open an
// imperative, so a pass is no claim that the subject reads well, and a
// bare lowercase noun ("reclaim removes a room") still passes.
//
// It holds no list of allowed verbs: a new verb arrives every few commits,
// and an allowlist gives a timed agent one escape, to edit the gate.

// determiners open a noun phrase and never an imperative. English adds
// none, so the list cannot go stale.
var determiners = wordSet("a all an both each eight eleven every fifteen five four its nine no one our seven six some " +
	"ten that the their these this those three twelve twenty two")

// toolNames are programs a project runs. Each must not double as an
// English verb a subject opens with, or the clause refuses a correct
// imperative: make is out for that reason.
var toolNames = wordSet("bd bun cargo deno gh git go loop node npm npx orca pip pnpm pnpx python uv yarn")

// versEndingInS are base-form verbs that the third-person clause must not
// refuse. The list fails open: a missing verb costs one reword, and never
// lets a declarative through.
var versEndingInS = wordSet("access address bias bypass canvas compress cross discuss dismiss express focus guess miss " +
	"pass press process stress toss")

var (
	// leadingID is a beads id at the head of the description, used as an
	// elided subject: the project prefix, then a hash that holds a digit
	// or a .N child suffix. An all-letter hash is left alone, so a
	// hyphenated verb such as fast-forward is never stripped.
	leadingID   = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}-(?:[a-z0-9]*[0-9][a-z0-9]*(?:\.[0-9]+)*|[a-z0-9]+(?:\.[0-9]+)+)\s+`)
	plainWord   = regexp.MustCompile(`^[a-z]+(-[a-z]+)*$`)
	thirdPerson = regexp.MustCompile(`^[a-z]+s$`)
)

const rewrite = `Open with a base-form verb, so that "This commit will <description>" reads as English: ` +
	`"the sweeper runs hourly" becomes "run the sweeper hourly".`

type mood struct {
	clause, token, detail string
}

// descriptionOf is the subject without its prefix and a leading beads id.
// It is empty when no prefix matched, which is how the mood rule declines
// a subject the prefix rule refused, git's revert subject included.
func descriptionOf(subject string) string {
	m := commitPrefix.FindString(subject)
	if m == "" {
		return ""
	}
	return leadingID.ReplaceAllString(subject[len(m):], "")
}

// moodFinding is nil when the rule accepts the subject.
func moodFinding(subject string) *mood {
	fields := strings.Fields(descriptionOf(subject))
	if len(fields) == 0 {
		return nil
	}
	token := strings.TrimRight(fields[0], ",:;.")
	if token == "" {
		return nil
	}
	lower := strings.ToLower(token)
	var clause, why string
	switch {
	case determiners[lower]:
		clause, why = "determiner", "opens a noun phrase, so the description is a statement about the system rather than an imperative naming the change"
	case token[0] >= '0' && token[0] <= '9' || strings.HasSuffix(token, "'s"):
		clause, why = "noun-phrase", "is a number or a possessive, so the description names a thing rather than an imperative action"
	case toolNames[lower]:
		clause, why = "tool-name", "is a program the project runs, so the description is a sentence about that program rather than an imperative. Name what the change does to it"
	case thirdPerson.MatchString(token) && !versEndingInS[lower]:
		clause, why = "third-person", fmt.Sprintf("reads as a verb in the third person singular, which makes the description a report rather than an imperative. If %q is a base-form verb, add it to versEndingInS in internal/gates/commits.go", token)
	case !plainWord.MatchString(token):
		clause, why = "identifier", "is a filename, a flag or an identifier, so it is the subject of a sentence rather than an imperative verb. Name the action first, then the identifier"
	default:
		return nil
	}
	return &mood{clause: clause, token: token, detail: fmt.Sprintf("%q %s. %s", token, why, rewrite)}
}

func wordSet(words string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Fields(words) {
		set[w] = true
	}
	return set
}
