package gates

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/pack"
)

// The rules a ledger row answers (lessons-rules.ts, alpha).

const (
	// pending is the rung of a lesson nobody has decided yet. Its row names
	// the decision that will answer it.
	pending = "pending"
	// noRung is the rung of a document row: the amendment closed it.
	noRung = "-"
	// withPR is the one channel whose correction has a pull request.
	withPR = "review"
	// repoScope is the scope whose lesson reaches a rung.
	repoScope = "repository"
	// The three shapes of a row's rung.
	shapeNumbered = "numbered"
	shapeNone     = "none"
)

var (
	// decisionID is the id `loop decision file` prints: a beads id, the
	// project's prefix and a hash, with a .n for a child.
	decisionID = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}-[a-z0-9]+(?:\.[0-9]+)*$`)
	prCell     = regexp.MustCompile(`^#[0-9]+$`)
	dateCell   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	trigger    = regexp.MustCompile(`(?m)^\*\*Trigger\.\*\*\s*\S`)
	// stronger is the paragraph after a **Stronger rungs.** marker.
	stronger = regexp.MustCompile(`(?m)^\*\*Stronger rungs\.\*\*(.*(?:\n.+)*)`)
)

func finding(rule, format string, args ...any) Finding {
	return Finding{Rule: rule, Message: fmt.Sprintf(format, args...)}
}

// label names a row by its channel, its date and the words. Two rows that
// match all three are one correction.
func label(r ledgerRow) string {
	return fmt.Sprintf("%s %s %q", r.channel, r.date, r.correction)
}

// shape is the shape of a row's rung, or "" when its scope cannot take it.
func shape(r ledgerRow, l ladder) string {
	if r.scope != repoScope {
		if r.rung == noRung {
			return shapeNone
		}
		return ""
	}
	if r.rung == pending {
		return pending
	}
	for _, x := range l.rungs {
		if strconv.Itoa(x.strength) == r.rung {
			return shapeNumbered
		}
	}
	return ""
}

// citation asks whether a later reader can reach the correction. A review
// comment keeps a permalink, so its row names the pull request. A chat
// keeps nothing, so the date and the words are the whole citation, and a
// row that names a pull request for it sends the reader to the wrong
// thread.
func citation(r ledgerRow, id string) []Finding {
	var out []Finding
	if !dateCell.MatchString(r.date) {
		out = append(out, finding("date", "%s reads date %q, and a date is YYYY-MM-DD", id, r.date))
	}
	if r.correction == "" || r.correction == "-" {
		out = append(out, finding("words", "%s quotes nothing, and the words are the record", id))
	}
	switch {
	case r.channel == withPR && !prCell.MatchString(r.pr):
		out = append(out, finding("source", "%s is a %s correction and names %q, not a pull request as #n", id, withPR, r.pr))
	case r.channel != withPR && (r.pr != "-" || r.at != "-"):
		out = append(out, finding("source", "%s is a %s correction, and it names %s %s; write - in both", id, r.channel, r.pr, r.at))
	}
	return out
}

// home checks what a row's Where it lives cell holds, given its shape. A
// numbered rung names files in the checkout, or gates the project runs:
// a gate of .loop/config.yaml, or one of the loop's own.
func home(r ledgerRow, sh, id string, repo *Repo, gateNames map[string]bool) []Finding {
	switch sh {
	case shapeNone:
		if len(r.holds) > 0 {
			return []Finding{finding("home", "%s bound one document and names %s; the amendment closed it, so write -", id, strings.Join(r.holds, ", "))}
		}
		return nil
	case pending:
		if len(r.holds) == 1 && decisionID.MatchString(r.holds[0]) {
			return nil
		}
		names := strings.Join(r.holds, ", ")
		if names == "" {
			names = "nothing"
		}
		return []Finding{finding("home", "%s is pending and must name the one decision id that `loop decision file` printed, not %s", id, names)}
	}
	if len(r.holds) == 0 {
		return []Finding{finding("home", "%s reached rung %s and names no file and no gate", id, r.rung)}
	}
	var out []Finding
	for _, n := range r.holds {
		if gateNames[n] {
			continue
		}
		if filepath.IsLocal(n) {
			if _, err := os.Stat(filepath.Join(repo.Root, n)); err == nil {
				continue
			}
		}
		if !strings.ContainsAny(n, "/.") {
			out = append(out, finding("wired", "%s names %s, which is no file and no gate of .loop/config.yaml or the loop", id, n))
			continue
		}
		out = append(out, finding("home", "%s names %s, and the checkout has no such file", id, n))
	}
	return out
}

// lessonSection checks a lesson's section: a pending lesson states its
// trigger, and a numbered one refuses each stronger rung. Rung 1 has no
// stronger rung to refuse.
func lessonSection(text, lesson, rungCell, sh string) []Finding {
	if sh == shapeNone {
		return nil // the Scope cell is the whole record of a document row
	}
	body := pack.Section(text, 3, func(t string) bool { return slugOf(t) == lesson })
	if body == "" {
		return []Finding{finding("lesson", "lesson %s has no ### section", lesson)}
	}
	if sh == pending {
		if trigger.MatchString(body) {
			return nil
		}
		return []Finding{finding("trigger", "lesson %s is pending and its section states no **Trigger.**", lesson)}
	}
	stated := ""
	if m := stronger.FindStringSubmatch(body); m != nil {
		stated = strings.ToLower(m[1])
	}
	var out []Finding
	n, _ := strconv.Atoi(rungCell)
	for above := 1; above < n; above++ {
		if !regexp.MustCompile(`\brung ` + strconv.Itoa(above) + `\b`).MatchString(stated) {
			out = append(out, finding("stronger", "lesson %s reached rung %s and its **Stronger rungs.** never refuses rung %d", lesson, rungCell, above))
		}
	}
	return out
}

// checkLedger holds the ledger to the ladder, the checkout and itself.
func checkLedger(repo *Repo, text string, l ladder, led ledger) []Finding {
	var out []Finding
	add := func(line int, fs ...Finding) {
		for _, f := range fs {
			f.File, f.Line = LedgerPath, line
			out = append(out, f)
		}
	}
	gateNames := map[string]bool{}
	for _, g := range List(repo.Config) {
		gateNames[g.Name] = true
	}
	seen, named, answers := map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, r := range led.rows {
		id := label(r)
		named[r.lesson] = true
		if seen[id] {
			add(r.line, finding("twice", "%s appears in two rows", id))
		}
		seen[id] = true
		if !slices.Contains(l.channels, r.channel) {
			add(r.line, finding("channel", "%s is channel %q, and the ladder defines %s", id, r.channel, strings.Join(l.channels, ", ")))
			continue
		}
		add(r.line, citation(r, id)...)
		if !slices.Contains(l.scopes, r.scope) {
			add(r.line, finding("scope", "%s is scope %q, and the ladder defines %s", id, r.scope, strings.Join(l.scopes, ", ")))
			continue
		}
		sh := shape(r, l)
		if sh == "" {
			add(r.line, finding("rung", "%s is %s and reads rung %q, which that scope cannot take", id, r.scope, r.rung))
			continue
		}
		add(r.line, home(r, sh, id, repo, gateNames)...)
		answer := r.scope + "/" + r.rung + "/" + strings.Join(r.holds, ",")
		first, ok := answers[r.lesson]
		switch {
		case !ok:
			answers[r.lesson] = answer
			add(r.line, lessonSection(text, r.lesson, r.rung, sh)...)
		case first != answer:
			add(r.line, finding("agree", "lesson %s records %q and %q, and one lesson has one answer", r.lesson, answer, first))
		}
	}
	for _, s := range led.sections {
		if !named[s] {
			add(0, finding("orphan", "section %s is a lesson that no row names", s))
		}
	}
	return out
}
